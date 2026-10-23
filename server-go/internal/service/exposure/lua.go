package exposure

import "github.com/redis/go-redis/v9"

// Lua for the risk book. Every mutation of the counters runs inside one of
// these scripts, so a check and the reservation it guards, or a trade and the
// counters it moves, are a single atomic step in Redis.
//
// Keys are built inside the scripts (ARGV[1] is the book namespace), which is
// fine on a standalone Redis but would need hash tags on Redis Cluster.
//
// Encodings:
//
//	exp:asset:<a>     hash  upStake downStake upProfit downProfit openTrades symbol updatedAt
//	                        + bLiab (sum of bucket liabilities) and nb (bucket count)
//	exp:user:<u>      hash  <assetId> -> open stake, total
//	<ns>:bkt:<a>      hash  <bucketStartMs> -> "uS|dS|uP|dP|n"
//	<ns>:ot:<u>       hash  <tradeId> -> "assetId|dir|stake|profit|bucket"  (committed trades)
//	<ns>:done:<t>     string, TTL 3 days: the trade's exposure was released
//	<ns>:rsv          hash  <tradeId> -> "assetId|userId|dir|stake|profit|bucket|symbol|deadlineMs"
//	<ns>:rsvdl        zset  <tradeId> scored by deadline
//	<ns>:state        hash  status gen gross liab users builtAt ...
//	<ns>:assets       set   asset ids in the book
//	<ns>:users        hash  <userId> -> total (mirror of exp:user:<u> total)
//	<ns>:journal:<g>  list  mutations made while generation g was being rebuilt
//	<ns>:tmp:<g>:<k>  the rebuilt copy of live key k
const luaPrelude = `
local NS = ARGV[1]
local TMP = nil
local REG = {}
local function I(x) return string.format('%d', x) end
local function N(v)
  if not v then return 0 end
  local n = tonumber(v)
  if not n then return 0 end
  return n
end
local function RK(k) if TMP then return TMP .. k end return k end
local function WK(k)
  if TMP then
    if not REG[k] then REG[k] = true; redis.call('SADD', TMP .. 'keys', k) end
    return TMP .. k
  end
  return k
end
local function split(s)
  local out, i = {}, 1
  while true do
    local j = string.find(s, '|', i, true)
    if not j then out[#out + 1] = string.sub(s, i); return out end
    out[#out + 1] = string.sub(s, i, j - 1)
    i = j + 1
  end
end
local function L(uS, dS, uP, dP)
  local m = 0
  if uP - dS > m then m = uP - dS end
  if dP - uS > m then m = dP - uS end
  return m
end
local K_STATE = NS .. ':state'
local K_ASSETS = NS .. ':assets'
local K_USERS = NS .. ':users'
local K_RSV = NS .. ':rsv'
local K_RSVDL = NS .. ':rsvdl'
local K_TMPGENS = NS .. ':tmpgens'
local DONE_TTL = 259200
local function kAsset(a) return 'exp:asset:' .. a end
local function kUser(u) return 'exp:user:' .. u end
local function kBkt(a) return NS .. ':bkt:' .. a end
local function kOT(u) return NS .. ':ot:' .. u end
local function kDone(t) return NS .. ':done:' .. t end
local function kJournal(g) return NS .. ':journal:' .. g end
local function kTmp(g) return NS .. ':tmp:' .. g .. ':' end

-- A live counter went somewhere it cannot: stop trusting the book until it
-- is rebuilt from MongoDB.
local function flag(reason, now)
  if TMP then return end
  if redis.call('HGET', K_STATE, 'status') == 'READY' then
    redis.call('HSET', K_STATE, 'status', 'DIRTY', 'reason', reason, 'dirtyAt', now)
  end
end

-- Moves one committed trade into (sg = 1) or out of (sg = -1) every counter.
local function applyTrade(a, u, d, s, p, b, sym, sg, now)
  local ak = WK(kAsset(a))
  if d == 'UP' then
    redis.call('HINCRBY', ak, 'upStake', I(sg * s))
    redis.call('HINCRBY', ak, 'upProfit', I(sg * p))
  else
    redis.call('HINCRBY', ak, 'downStake', I(sg * s))
    redis.call('HINCRBY', ak, 'downProfit', I(sg * p))
  end
  if redis.call('HINCRBY', ak, 'openTrades', I(sg)) < 0 then flag('openTrades:' .. a, now) end

  local bk = kBkt(a)
  local raw = redis.call('HGET', RK(bk), b)
  local uS, dS, uP, dP, n = 0, 0, 0, 0, 0
  local before = 0
  if raw then
    local f = split(raw)
    uS, dS, uP, dP, n = N(f[1]), N(f[2]), N(f[3]), N(f[4]), N(f[5])
    before = L(uS, dS, uP, dP)
  end
  if d == 'UP' then uS = uS + sg * s; uP = uP + sg * p else dS = dS + sg * s; dP = dP + sg * p end
  n = n + sg
  local after, dnb = 0, 0
  if n <= 0 then
    if raw then redis.call('HDEL', WK(bk), b); dnb = -1 end
    if n < 0 or uS ~= 0 or dS ~= 0 or uP ~= 0 or dP ~= 0 then flag('bucket:' .. a, now) end
  else
    redis.call('HSET', WK(bk), b, I(uS) .. '|' .. I(dS) .. '|' .. I(uP) .. '|' .. I(dP) .. '|' .. I(n))
    after = L(uS, dS, uP, dP)
    if not raw then dnb = 1 end
  end
  redis.call('HINCRBY', ak, 'bLiab', I(after - before))
  redis.call('HINCRBY', ak, 'nb', I(dnb))
  if sym and sym ~= '' then redis.call('HSET', ak, 'symbol', sym) end
  redis.call('HSET', ak, 'updatedAt', now)
  redis.call('SADD', WK(K_ASSETS), a)
  if not TMP then redis.call('SADD', 'exp:assets', a) end

  local uk = WK(kUser(u))
  local ua = redis.call('HINCRBY', uk, a, I(sg * s))
  if ua == 0 then redis.call('HDEL', uk, a) elseif ua < 0 then flag('user:' .. u, now) end
  local tot = redis.call('HINCRBY', uk, 'total', I(sg * s))
  local had = redis.call('HEXISTS', RK(K_USERS), u) == 1
  if tot <= 0 then
    if tot < 0 then flag('user:' .. u, now) end
    redis.call('DEL', uk)
    if had then
      redis.call('HDEL', WK(K_USERS), u)
      redis.call('HINCRBY', WK(K_STATE), 'users', -1)
    end
  else
    redis.call('HSET', WK(K_USERS), u, I(tot))
    if not had then redis.call('HINCRBY', WK(K_STATE), 'users', 1) end
  end
  redis.call('HINCRBY', WK(K_STATE), 'gross', I(sg * s))
  redis.call('HINCRBY', WK(K_STATE), 'liab', I(after - before))
end

-- Exactly once per trade id: a second open is 'dup', an open after the trade
-- was already released is 'closed' (unless forced by a repair from MongoDB).
local function openRecord(t, a, u, d, s, p, b, sym, now, force)
  if not force and redis.call('EXISTS', kDone(t)) == 1 then return 'closed' end
  local ok = kOT(u)
  if redis.call('HEXISTS', RK(ok), t) == 1 then return 'dup' end
  redis.call('HSET', WK(ok), t, a .. '|' .. d .. '|' .. I(s) .. '|' .. I(p) .. '|' .. b)
  applyTrade(a, u, d, s, p, b, sym, 1, now)
  return 'opened'
end

local function closeRecord(t, u, now)
  local ok = kOT(u)
  local rec = redis.call('HGET', RK(ok), t)
  if not rec then return 'missing' end
  redis.call('HDEL', WK(ok), t)
  local f = split(rec)
  applyTrade(f[1], u, f[2], N(f[3]), N(f[4]), f[5], nil, -1, now)
  return 'closed'
end

-- A release that carries no trade id: any open trade of the user with the
-- same asset, side, stake and profit moves the same counters; the earliest
-- expiry bucket is the likeliest to be the one settling.
local function matchOpen(u, a, d, s, p)
  local all = redis.call('HGETALL', RK(kOT(u)))
  local best, bestB = nil, nil
  for i = 1, #all, 2 do
    local f = split(all[i + 1])
    if f[1] == a and f[2] == d and N(f[3]) == s and N(f[4]) == p then
      local bb = N(f[5])
      if best == nil or bb < bestB then best, bestB = all[i], bb end
    end
  end
  return best
end

-- While a rebuild runs, every mutation is also journalled so the swap can
-- replay it onto the rebuilt book.
local function journal(entry)
  local st = redis.call('HMGET', K_STATE, 'status', 'gen')
  if st[1] == 'REBUILDING' and st[2] then redis.call('RPUSH', kJournal(st[2]), entry) end
end

local function dropTmp(g)
  local pre = kTmp(g)
  local keys = redis.call('SMEMBERS', pre .. 'keys')
  for _, k in ipairs(keys) do redis.call('DEL', pre .. k) end
  redis.call('DEL', pre .. 'keys', pre .. K_STATE, kJournal(g))
  redis.call('SREM', K_TMPGENS, g)
end
`

func script(body string) *redis.Script { return redis.NewScript(luaPrelude + body) }

// reserve: ARGV 2 tradeId 3 assetId 4 userId 5 dir 6 stake 7 profit 8 bucket
// 9 symbol 10 deadline 11 now 12 userLimit 13 assetGrossLimit 14 netLimit
// 15 platformGrossLimit 16 concentrationPercent 17 expiryLiabilityLimit
// 18 assetLiabilityLimit 19 platformLiabilityLimit (liability 0 = off).
//
// Every limit is checked against committed counters PLUS every live
// reservation, taken in its worst case: stakes add up on every gross measure,
// a reservation on the same side moves the net figure, and for liability each
// reservation is assumed to commit only when that hurts (its profit counts on
// its winning side, its stake is never credited against the other side). So
// whichever subset of reservations finally commits, committed exposure stays
// within the limits that held when each was reserved.
var reserveScript = script(`
local t, a, u, d = ARGV[2], ARGV[3], ARGV[4], ARGV[5]
local s, p = N(ARGV[6]), N(ARGV[7])
local b, sym, dl, now = ARGV[8], ARGV[9], ARGV[10], ARGV[11]
local limUser, limGross, limNet, limPlat = N(ARGV[12]), N(ARGV[13]), N(ARGV[14]), N(ARGV[15])
local concPct = N(ARGV[16])
local limBkt, limAL, limPL = N(ARGV[17]), N(ARGV[18]), N(ARGV[19])
local limSide = N(ARGV[20])

local st = redis.call('HMGET', K_STATE, 'status', 'gross', 'liab', 'users')
if st[1] ~= 'READY' then return {'UNCERTAIN', st[1] or 'MISSING'} end
local function dirty(reason)
  redis.call('HSET', K_STATE, 'status', 'DIRTY', 'reason', reason, 'dirtyAt', now)
  return {'UNCERTAIN', 'DIRTY', reason}
end
if redis.call('HEXISTS', K_RSV, t) == 1 then return {'OK', 'dup'} end
if redis.call('HEXISTS', kOT(u), t) == 1 then return {'CONFLICT', 'committed'} end
if redis.call('EXISTS', kDone(t)) == 1 then return {'CONFLICT', 'closed'} end

-- Integrity. The state hash carries the platform totals and every mutation
-- moves them together with the per-asset counters, so a deleted or evicted
-- key shows up as a mismatch instead of being read as an empty book.
local stGross, stLiab, stUsers = N(st[2]), N(st[3]), N(st[4])
local sumGross, sumLiab = 0, 0
for _, m in ipairs(redis.call('SMEMBERS', K_ASSETS)) do
  local v = redis.call('HMGET', kAsset(m), 'upStake', 'downStake', 'bLiab', 'nb')
  sumGross = sumGross + N(v[1]) + N(v[2])
  sumLiab = sumLiab + N(v[3])
  if N(v[4]) ~= redis.call('HLEN', kBkt(m)) then return dirty('buckets:' .. m) end
end
if sumGross ~= stGross or sumLiab ~= stLiab then return dirty('platform') end
if redis.call('HLEN', K_USERS) ~= stUsers then return dirty('users') end
if redis.call('SISMEMBER', K_ASSETS, a) == 0 then
  local v = redis.call('HMGET', kAsset(a), 'upStake', 'downStake', 'openTrades')
  if N(v[1]) ~= 0 or N(v[2]) ~= 0 or N(v[3]) ~= 0 then return dirty('asset:' .. a) end
end
local uh = redis.call('HGETALL', kUser(u))
local userTotal, userFields = 0, {}
for i = 1, #uh, 2 do
  if uh[i] == 'total' then userTotal = N(uh[i + 1]) else userFields[uh[i]] = N(uh[i + 1]) end
end
local recSum, recBy = 0, {}
for _, r in ipairs(redis.call('HVALS', kOT(u))) do
  local f = split(r)
  recSum = recSum + N(f[3])
  recBy[f[1]] = (recBy[f[1]] or 0) + N(f[3])
end
if N(redis.call('HGET', K_USERS, u)) ~= userTotal or recSum ~= userTotal then return dirty('user:' .. u) end
for k, v in pairs(userFields) do if v ~= 0 and recBy[k] ~= v then return dirty('user:' .. u) end end
for k, v in pairs(recBy) do if userFields[k] ~= v then return dirty('user:' .. u) end end
local userAsset = userFields[a] or 0

-- Live reservations.
local pUser, pUserAsset, pAsset, pUp, pDown, pPlat = 0, 0, 0, 0, 0, 0
local pUpP, pDownP = 0, 0
local groups = {}
local pend = redis.call('HGETALL', K_RSV)
for i = 1, #pend, 2 do
  local f = split(pend[i + 1])
  local pa, pu, pd, ps, pp, pb = f[1], f[2], f[3], N(f[4]), N(f[5]), f[6]
  pPlat = pPlat + ps
  if pu == u then
    pUser = pUser + ps
    if pa == a then pUserAsset = pUserAsset + ps end
  end
  if pa == a then
    pAsset = pAsset + ps
    if pd == 'UP' then pUp = pUp + ps; pUpP = pUpP + pp else pDown = pDown + ps; pDownP = pDownP + pp end
  end
  local gk = pa .. '|' .. pb
  local g = groups[gk]
  if not g then g = {a = pa, b = pb, up = 0, dn = 0}; groups[gk] = g end
  if pd == 'UP' then g.up = g.up + pp else g.dn = g.dn + pp end
end

local av = redis.call('HMGET', kAsset(a), 'upStake', 'downStake', 'bLiab')
local cUp, cDown, cBL = N(av[1]), N(av[2]), N(av[3])
local gross, net = cUp + cDown, cUp - cDown

-- Same rules and order as risk.CheckTradeLimits.
local v = {'REFUSED'}
if userTotal + pUser + s > limUser then v[#v + 1] = 'USER_EXPOSURE' end
local projGross = gross + pAsset + s
if projGross > limGross then v[#v + 1] = 'ASSET_GROSS' end
local projNet
if d == 'UP' then projNet = net + pUp + s else projNet = net - pDown - s end
if math.abs(projNet) > limNet then v[#v + 1] = 'ASSET_NET' end
if stGross + pPlat + s > limPlat then v[#v + 1] = 'PLATFORM_GROSS' end
-- Other users' reservations are left out of the denominator: a book that
-- might not materialise must not dilute this user's share.
local concGross = gross + pUserAsset + s
if concGross > limUser and (userAsset + pUserAsset + s) / concGross * 100 > concPct then
  v[#v + 1] = 'CONCENTRATION'
end

if limBkt > 0 or limAL > 0 or limPL > 0 then
  local selfKey = a .. '|' .. b
  if not groups[selfKey] then groups[selfKey] = {a = a, b = b, up = 0, dn = 0} end
  local platDelta, assetDelta, selfBefore, selfAfter = 0, 0, 0, 0
  for gk, g in pairs(groups) do
    local raw = redis.call('HGET', kBkt(g.a), g.b)
    local uS, dS, uP, dP = 0, 0, 0, 0
    if raw then
      local f = split(raw)
      uS, dS, uP, dP = N(f[1]), N(f[2]), N(f[3]), N(f[4])
    end
    local committed = L(uS, dS, uP, dP)
    local worst = L(uS, dS, uP + g.up, dP + g.dn)
    platDelta = platDelta + worst - committed
    if g.a == a then assetDelta = assetDelta + worst - committed end
    if gk == selfKey then
      selfBefore = worst
      if d == 'UP' then selfAfter = L(uS, dS, uP + g.up + p, dP + g.dn)
      else selfAfter = L(uS, dS, uP + g.up, dP + g.dn + p) end
    end
  end
  local inc = selfAfter - selfBefore
  -- A trade that does not raise the worst case is never refused on liability.
  if inc > 0 then
    if limBkt > 0 and selfAfter > limBkt then v[#v + 1] = 'EXPIRY_LIABILITY' end
    if limAL > 0 and cBL + assetDelta + inc > limAL then v[#v + 1] = 'ASSET_LIABILITY' end
    if limPL > 0 and stLiab + platDelta + inc > limPL then v[#v + 1] = 'PLATFORM_LIABILITY' end
  end
end
-- Pricing specification 7.2, final hard check, per asset and side: the
-- platform's net loss if side d wins, L(d) = profits on d - stakes on the
-- other side, must stay within limSide with this trade. Reservations count
-- in their worst case (their profit on their side, their stake never
-- credited against the other).
if limSide > 0 then
  local sv = redis.call('HMGET', kAsset(a), 'upProfit', 'downProfit')
  local cUpP, cDownP = N(sv[1]), N(sv[2])
  local lSide
  if d == 'UP' then lSide = cUpP + pUpP - cDown else lSide = cDownP + pDownP - cUp end
  if math.max(0, lSide + p) > limSide then v[#v + 1] = 'SIDE_LIABILITY' end
end
if #v > 1 then return v end

redis.call('HSET', K_RSV, t, a .. '|' .. u .. '|' .. d .. '|' .. I(s) .. '|' .. I(p) .. '|' .. b .. '|' .. sym .. '|' .. dl)
redis.call('ZADD', K_RSVDL, dl, t)
return {'OK', 'new'}
`)

// renew: ARGV 2 tradeId 3 deadline. 1 = still held, 0 = gone.
var renewScript = script(`
local raw = redis.call('HGET', K_RSV, ARGV[2])
if not raw then return 0 end
local f = split(raw)
f[8] = ARGV[3]
redis.call('HSET', K_RSV, ARGV[2], table.concat(f, '|'))
redis.call('ZADD', K_RSVDL, ARGV[3], ARGV[2])
return 1
`)

// commit: ARGV 2 tradeId 3 assetId 4 userId 5 dir 6 stake 7 profit 8 bucket
// 9 symbol 10 now 11 force. Returns {opened|dup|closed, pending|late}.
var commitScript = script(`
local t, a, u, d = ARGV[2], ARGV[3], ARGV[4], ARGV[5]
local s, p, b, sym, now = N(ARGV[6]), N(ARGV[7]), ARGV[8], ARGV[9], ARGV[10]
local force = ARGV[11] == '1'
local had = redis.call('HDEL', K_RSV, t)
redis.call('ZREM', K_RSVDL, t)
if force then redis.call('DEL', kDone(t)) end
local r = openRecord(t, a, u, d, s, p, b, sym, now, force)
journal('O|' .. t .. '|' .. a .. '|' .. u .. '|' .. d .. '|' .. I(s) .. '|' .. I(p) .. '|' .. b .. '|' .. sym)
if had == 1 then return {r, 'pending'} end
return {r, 'late'}
`)

// release: ARGV 2 tradeId. 1 = a reservation was dropped.
var releaseScript = script(`
local had = redis.call('HDEL', K_RSV, ARGV[2])
redis.call('ZREM', K_RSVDL, ARGV[2])
return had
`)

// close: ARGV 2 tradeId 3 userId 4 now. Returns closed|missing.
var closeScript = script(`
local t, u, now = ARGV[2], ARGV[3], ARGV[4]
redis.call('HDEL', K_RSV, t)
redis.call('ZREM', K_RSVDL, t)
local r = closeRecord(t, u, now)
redis.call('SET', kDone(t), '1', 'EX', DONE_TTL)
journal('X|' .. t .. '|' .. u)
return r
`)

// closeMatch: ARGV 2 assetId 3 userId 4 dir 5 stake 6 profit 7 now.
// Returns {closed, tradeId} | {nomatch}.
var closeMatchScript = script(`
local a, u, d, s, p, now = ARGV[2], ARGV[3], ARGV[4], N(ARGV[5]), N(ARGV[6]), ARGV[7]
local t = matchOpen(u, a, d, s, p)
if not t then
  journal('L|' .. a .. '|' .. u .. '|' .. d .. '|' .. I(s) .. '|' .. I(p))
  return {'nomatch'}
end
closeRecord(t, u, now)
redis.call('SET', kDone(t), '1', 'EX', DONE_TTL)
journal('X|' .. t .. '|' .. u)
return {'closed', t}
`)

// closeID: ARGV 2 tradeId 3 userId 4 assetId 5 dir 6 stake 7 profit
// 8 bucket 9 now. Releases a finished trade by its id, exactly once: the
// record under the id is closed and the id is tombstoned, so a repeat (or a
// commit that lands late) changes nothing. Backward compatibility: a trade
// counted before records were keyed by trade id sits in the book under an
// anonymous "anon-" record. Only when the id has no record, was never
// released and holds no reservation is the matching legacy record (same
// asset, side, stake and profit; the same expiry bucket first, else the
// earliest) released in its place. Records keyed by another trade's id are
// never touched. Returns {closed} | {legacy, recordId} | {missing} | {done}.
var closeIDScript = script(`
local t, u, a, d = ARGV[2], ARGV[3], ARGV[4], ARGV[5]
local s, p, b, now = N(ARGV[6]), N(ARGV[7]), ARGV[8], ARGV[9]
local already = redis.call('EXISTS', kDone(t)) == 1
local hadRsv = redis.call('HDEL', K_RSV, t)
redis.call('ZREM', K_RSVDL, t)
local r = closeRecord(t, u, now)
local legacy = nil
if r == 'missing' and not already and hadRsv == 0 then
  local all = redis.call('HGETALL', kOT(u))
  local bestB = nil
  for i = 1, #all, 2 do
    local id = all[i]
    if string.sub(id, 1, 5) == 'anon-' then
      local f = split(all[i + 1])
      if f[1] == a and f[2] == d and N(f[3]) == s and N(f[4]) == p then
        local fb = N(f[5])
        if f[5] == b then fb = -1 end
        if legacy == nil or fb < bestB then legacy, bestB = id, fb end
      end
    end
  end
  if legacy then
    closeRecord(legacy, u, now)
    redis.call('SET', kDone(legacy), '1', 'EX', DONE_TTL)
  end
end
redis.call('SET', kDone(t), '1', 'EX', DONE_TTL)
journal('X|' .. t .. '|' .. u)
if legacy then
  journal('X|' .. legacy .. '|' .. u)
  return {'legacy', legacy}
end
if r == 'missing' and already then return {'done'} end
return {r}
`)

// snapshot: one consistent read of the whole book for the reconciler.
// {stateFlat, {{assetId, assetFlat, bucketFlat}...}, {{userId, mirror,
// userFlat, recordsFlat}...}, reservationsFlat}
var snapshotScript = script(`
local ah = {}
for _, a in ipairs(redis.call('SMEMBERS', K_ASSETS)) do
  ah[#ah + 1] = {a, redis.call('HGETALL', kAsset(a)), redis.call('HGETALL', kBkt(a))}
end
local mirror = redis.call('HGETALL', K_USERS)
local uh = {}
for i = 1, #mirror, 2 do
  local u = mirror[i]
  uh[#uh + 1] = {u, mirror[i + 1], redis.call('HGETALL', kUser(u)), redis.call('HGETALL', kOT(u))}
end
return {redis.call('HGETALL', K_STATE), ah, uh, redis.call('HGETALL', K_RSV)}
`)

// rebuildBegin: ARGV 2 gen 3 now. Enters safe mode (no new reservations) and
// starts journalling. Returns the previous status.
var rebuildBeginScript = script(`
for _, g in ipairs(redis.call('SMEMBERS', K_TMPGENS)) do dropTmp(g) end
local prev = redis.call('HGET', K_STATE, 'status') or 'MISSING'
redis.call('SADD', K_TMPGENS, ARGV[2])
redis.call('HSET', K_STATE, 'status', 'REBUILDING', 'gen', ARGV[2], 'rebuildStartedAt', ARGV[3])
return prev
`)

// rebuildLoad: ARGV 2 gen 3 now, then (tradeId assetId userId dir stake
// profit bucket symbol) per trade. Builds the trades into the temporary keys.
var rebuildLoadScript = script(`
if redis.call('HGET', K_STATE, 'gen') ~= ARGV[2] then return -1 end
TMP = kTmp(ARGV[2])
local n = 0
for i = 4, #ARGV, 8 do
  if openRecord(ARGV[i], ARGV[i + 1], ARGV[i + 2], ARGV[i + 3], N(ARGV[i + 4]), N(ARGV[i + 5]), ARGV[i + 6], ARGV[i + 7], ARGV[3], true) == 'opened' then
    n = n + 1
  end
end
return n
`)

// rebuildAssets: ARGV 2 gen 3 now, then (assetId symbol) pairs. Every
// registered asset gets a hash, zeros when it has no open trades.
var rebuildAssetsScript = script(`
if redis.call('HGET', K_STATE, 'gen') ~= ARGV[2] then return -1 end
TMP = kTmp(ARGV[2])
for i = 4, #ARGV, 2 do
  local k = WK(kAsset(ARGV[i]))
  for _, f in ipairs({'upStake', 'downStake', 'upProfit', 'downProfit', 'openTrades', 'bLiab', 'nb'}) do
    redis.call('HSETNX', k, f, '0')
  end
  redis.call('HSET', k, 'symbol', ARGV[i + 1])
  redis.call('HSETNX', k, 'updatedAt', ARGV[3])
  redis.call('SADD', WK(K_ASSETS), ARGV[i])
end
return 1
`)

// rebuildSwap: ARGV 2 gen 3 now, then "a:<assetId>" / "u:<userId>" entries
// for stray live keys to retire. Replays the journal onto the rebuilt book,
// retires the old live keys and renames the rebuilt ones into place, all in
// one atomic step, then leaves safe mode.
var rebuildSwapScript = script(`
local gen, now = ARGV[2], ARGV[3]
local st = redis.call('HMGET', K_STATE, 'status', 'gen')
if st[1] ~= 'REBUILDING' or st[2] ~= gen then return {'ABORTED', st[1] or 'MISSING', st[2] or ''} end
TMP = kTmp(gen)
local entries = redis.call('LRANGE', kJournal(gen), 0, -1)
for _, e in ipairs(entries) do
  local f = split(e)
  if f[1] == 'O' then
    openRecord(f[2], f[3], f[4], f[5], N(f[6]), N(f[7]), f[8], f[9], now, false)
  elseif f[1] == 'X' then
    closeRecord(f[2], f[3], now)
  elseif f[1] == 'L' then
    local t = matchOpen(f[3], f[2], f[4], N(f[5]), N(f[6]))
    if t then closeRecord(t, f[3], now) end
  end
end
local pre = TMP
local totals = redis.call('HMGET', pre .. K_STATE, 'gross', 'liab', 'users')
TMP = nil
for _, a in ipairs(redis.call('SMEMBERS', K_ASSETS)) do redis.call('DEL', kAsset(a), kBkt(a)) end
for _, u in ipairs(redis.call('HKEYS', K_USERS)) do redis.call('DEL', kUser(u), kOT(u)) end
for i = 4, #ARGV do
  local kind, id = string.sub(ARGV[i], 1, 1), string.sub(ARGV[i], 3)
  if kind == 'a' then redis.call('DEL', kAsset(id), kBkt(id)) else redis.call('DEL', kUser(id), kOT(id)) end
end
redis.call('DEL', K_ASSETS, K_USERS)
local moved = 0
for _, k in ipairs(redis.call('SMEMBERS', pre .. 'keys')) do
  if k ~= K_STATE and redis.call('EXISTS', pre .. k) == 1 then
    redis.call('RENAME', pre .. k, k)
    moved = moved + 1
  end
end
redis.call('DEL', pre .. 'keys', pre .. K_STATE, kJournal(gen))
redis.call('SREM', K_TMPGENS, gen)
redis.call('HDEL', K_STATE, 'reason', 'dirtyAt', 'rebuildStartedAt')
redis.call('HSET', K_STATE, 'status', 'READY', 'gen', gen, 'builtAt', now,
  'gross', I(N(totals[1])), 'liab', I(N(totals[2])), 'users', I(N(totals[3])))
return {'OK', I(moved), I(#entries)}
`)

// rebuildAbort: ARGV 2 gen 3 now 4 reason. Leaves the book DIRTY (still in
// safe mode) so the next attempt rebuilds it.
var rebuildAbortScript = script(`
dropTmp(ARGV[2])
local st = redis.call('HMGET', K_STATE, 'status', 'gen')
if st[1] == 'REBUILDING' and st[2] == ARGV[2] then
  redis.call('HSET', K_STATE, 'status', 'DIRTY', 'reason', ARGV[4], 'dirtyAt', ARGV[3])
  return 1
end
return 0
`)

// markDirty: ARGV 2 reason 3 now. READY -> DIRTY.
var markDirtyScript = script(`
if redis.call('HGET', K_STATE, 'status') == 'READY' then
  redis.call('HSET', K_STATE, 'status', 'DIRTY', 'reason', ARGV[2], 'dirtyAt', ARGV[3])
  return 1
end
return 0
`)
