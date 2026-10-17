package exposure

// Reconciliation: the book is compared with MongoDB (the source of truth)
// continuously, not only at boot.
//
//   - SweepReservations resolves reservations whose deadline passed (their
//     request crashed or stalled): a trade that exists and is live in MongoDB
//     is committed, anything else is released.
//   - Reconcile takes one atomic snapshot of the book, checks that every
//     counter equals what the per-trade records add up to (internal), then
//     compares the records with the open LIVE trades in MongoDB (external).
//     Internal drift means a counter was changed outside the scripts (or a key
//     was lost): the book is rebuilt. External drift is repaired per trade
//     with the same idempotent scripts the trade path uses, so a repair can
//     never double count against a commit or release still in flight.
//     Disagreements younger than ReconcileGrace are left alone.
//
// Every repair is logged and written to the audit log.

import (
	"context"
	"sort"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/audit"
	"binarytrading/internal/service/metrics"
)

type SweepReport struct {
	Committed []string `json:"committed"`
	Released  []string `json:"released"`
}

// SweepReservations resolves lapsed reservations against MongoDB. A trade
// that cannot be looked up (MongoDB down) keeps its reservation: holding
// headroom too long is safe, releasing it under a live trade is not.
func SweepReservations(ctx context.Context) (SweepReport, error) {
	out := SweepReport{Committed: []string{}, Released: []string{}}
	ns := BookNamespace()
	now := util.NowMs()
	lapsed, err := redisx.R().ZRangeByScoreWithScores(ctx, ns+":rsvdl", &redis.ZRangeBy{
		Min: "-inf", Max: strconv.FormatInt(now, 10), Count: 500,
	}).Result()
	if err != nil {
		return out, err
	}
	if len(lapsed) == 0 {
		return out, nil
	}
	ids := make([]string, 0, len(lapsed))
	oids := make(bson.A, 0, len(lapsed))
	for _, z := range lapsed {
		id, _ := z.Member.(string)
		ids = append(ids, id)
		if oid, err := bson.ObjectIDFromHex(id); err == nil {
			oids = append(oids, oid)
		}
	}
	found := map[string]bookTrade{}
	if len(oids) > 0 {
		cur, err := mongox.C(models.CollBinaryTrades).Find(ctx, bson.M{"_id": bson.M{"$in": oids}},
			options.Find().SetProjection(openTradeProjection))
		if err != nil {
			return out, err
		}
		var rows []bookTrade
		if err := cur.All(ctx, &rows); err != nil {
			return out, err
		}
		for _, r := range rows {
			found[r.ID.Hex()] = r
		}
	}
	for i, id := range ids {
		t, ok := found[id]
		switch {
		case ok && t.live():
			if _, err := CommitTrade(ctx, t.open()); err != nil {
				return out, err
			}
			out.Committed = append(out.Committed, id)
		case ok || int64(lapsed[i].Score)+ReservationReleaseMargin.Milliseconds() <= now:
			// Finished (its release, if any, already ran) or never written.
			if err := ReleaseReservation(ctx, id); err != nil {
				return out, err
			}
			out.Released = append(out.Released, id)
		}
	}
	if len(out.Committed)+len(out.Released) > 0 {
		logx.Warn("lapsed exposure reservations resolved", "committed", out.Committed, "released", out.Released)
	}
	return out, nil
}

// ── snapshot ────────────────────────────────────────────────────────────

type recordSnap struct {
	AssetID   string
	Direction string
	Stake     int64
	Profit    int64
	Bucket    string
}

type bookSnapshot struct {
	State   map[string]string
	Assets  map[string]map[string]string // exp:asset:<a>
	Buckets map[string]map[string]string // <ns>:bkt:<a>
	Mirror  map[string]int64             // <ns>:users
	Users   map[string]map[string]string // exp:user:<u>
	Records map[string]map[string]recordSnap
	Pending map[string]string // tradeId -> raw reservation
}

func flatMap(v any) map[string]string {
	out := map[string]string{}
	arr, _ := v.([]any)
	for i := 0; i+1 < len(arr); i += 2 {
		out[strs(arr[i : i+1])[0]] = strs(arr[i+1 : i+2])[0]
	}
	return out
}

func parseRecord(raw string) recordSnap {
	f := splitPipe(raw)
	at := func(i int) string {
		if i < len(f) {
			return f[i]
		}
		return ""
	}
	stake, _ := strconv.ParseInt(at(2), 10, 64)
	profit, _ := strconv.ParseInt(at(3), 10, 64)
	return recordSnap{AssetID: at(0), Direction: at(1), Stake: stake, Profit: profit, Bucket: at(4)}
}

func splitPipe(s string) []string {
	out := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '|' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func takeSnapshot(ctx context.Context) (bookSnapshot, error) {
	res, err := snapshotScript.Run(ctx, redisx.R(), nil, BookNamespace()).Slice()
	if err != nil {
		return bookSnapshot{}, err
	}
	s := bookSnapshot{
		Assets: map[string]map[string]string{}, Buckets: map[string]map[string]string{},
		Mirror: map[string]int64{}, Users: map[string]map[string]string{}, Records: map[string]map[string]recordSnap{},
	}
	if len(res) < 4 {
		return s, nil
	}
	s.State = flatMap(res[0])
	assetRows, _ := res[1].([]any)
	for _, row := range assetRows {
		r, _ := row.([]any)
		if len(r) < 3 {
			continue
		}
		a := strs(r[:1])[0]
		s.Assets[a] = flatMap(r[1])
		s.Buckets[a] = flatMap(r[2])
	}
	userRows, _ := res[2].([]any)
	for _, row := range userRows {
		r, _ := row.([]any)
		if len(r) < 4 {
			continue
		}
		u := strs(r[:1])[0]
		total, _ := strconv.ParseInt(strs(r[1:2])[0], 10, 64)
		s.Mirror[u] = total
		s.Users[u] = flatMap(r[2])
		recs := map[string]recordSnap{}
		for t, raw := range flatMap(r[3]) {
			recs[t] = parseRecord(raw)
		}
		s.Records[u] = recs
	}
	s.Pending = flatMap(res[3])
	return s, nil
}

// checkInternal recomputes every counter from the per-trade records and
// lists each one that differs. Pure.
func checkInternal(s bookSnapshot) []string {
	drift := []string{}
	type assetAgg struct {
		c       RawCounters
		buckets map[string]BucketCounters
	}
	agg := map[string]*assetAgg{}
	userAsset := map[string]map[string]int64{}
	var gross int64
	for u, recs := range s.Records {
		for _, r := range recs {
			a := agg[r.AssetID]
			if a == nil {
				a = &assetAgg{buckets: map[string]BucketCounters{}}
				agg[r.AssetID] = a
			}
			dir := models.DirectionDown
			if r.Direction == "UP" {
				dir = models.DirectionUp
				a.c.UpStake += r.Stake
				a.c.UpProfit += r.Profit
			} else {
				a.c.DownStake += r.Stake
				a.c.DownProfit += r.Profit
			}
			a.c.OpenTrades++
			a.buckets[r.Bucket] = a.buckets[r.Bucket].With(dir, r.Stake, r.Profit)
			if userAsset[u] == nil {
				userAsset[u] = map[string]int64{}
			}
			userAsset[u][r.AssetID] += r.Stake
			gross += r.Stake
		}
	}

	var liab int64
	ids := map[string]bool{}
	for a := range s.Assets {
		ids[a] = true
	}
	for a := range agg {
		ids[a] = true
	}
	for a := range ids {
		want := agg[a]
		if want == nil {
			want = &assetAgg{buckets: map[string]BucketCounters{}}
		}
		h, listed := s.Assets[a]
		if !listed {
			drift = append(drift, "asset "+a+" has open trades but is not in the book")
			h = map[string]string{}
		}
		var wantLiab int64
		for _, b := range want.buckets {
			wantLiab += BucketLiability(b)
		}
		liab += wantLiab
		check := func(field string, got, exp int64) {
			if got != exp {
				drift = append(drift, "asset "+a+" "+field+" is "+strconv.FormatInt(got, 10)+", trades say "+strconv.FormatInt(exp, 10))
			}
		}
		check("upStake", num(h, "upStake"), want.c.UpStake)
		check("downStake", num(h, "downStake"), want.c.DownStake)
		check("upProfit", num(h, "upProfit"), want.c.UpProfit)
		check("downProfit", num(h, "downProfit"), want.c.DownProfit)
		check("openTrades", num(h, "openTrades"), want.c.OpenTrades)
		check("bLiab", num(h, "bLiab"), wantLiab)
		check("nb", num(h, "nb"), int64(len(want.buckets)))
		got := s.Buckets[a]
		if len(got) != len(want.buckets) {
			drift = append(drift, "asset "+a+" has "+strconv.Itoa(len(got))+" expiry buckets, trades say "+strconv.Itoa(len(want.buckets)))
		}
		for k, b := range want.buckets {
			if parseBucket(got[k]) != b {
				drift = append(drift, "asset "+a+" bucket "+k+" differs from its trades")
			}
		}
	}

	users := map[string]bool{}
	for u := range s.Mirror {
		users[u] = true
	}
	for u := range userAsset {
		users[u] = true
	}
	for u := range users {
		var want int64
		for _, v := range userAsset[u] {
			want += v
		}
		if s.Mirror[u] != want {
			drift = append(drift, "user "+u+" mirror total is "+strconv.FormatInt(s.Mirror[u], 10)+", trades say "+strconv.FormatInt(want, 10))
		}
		h := s.Users[u]
		if num(h, "total") != want {
			drift = append(drift, "user "+u+" total is "+strconv.FormatInt(num(h, "total"), 10)+", trades say "+strconv.FormatInt(want, 10))
		}
		for field := range h {
			if field != "total" && num(h, field) != userAsset[u][field] {
				drift = append(drift, "user "+u+" asset "+field+" differs from its trades")
			}
		}
		for a, v := range userAsset[u] {
			if num(h, a) != v {
				drift = append(drift, "user "+u+" asset "+a+" differs from its trades")
			}
		}
	}

	if num(s.State, "gross") != gross {
		drift = append(drift, "platform gross is "+strconv.FormatInt(num(s.State, "gross"), 10)+", trades say "+strconv.FormatInt(gross, 10))
	}
	if num(s.State, "liab") != liab {
		drift = append(drift, "platform liability is "+strconv.FormatInt(num(s.State, "liab"), 10)+", trades say "+strconv.FormatInt(liab, 10))
	}
	if num(s.State, "users") != int64(len(s.Mirror)) {
		drift = append(drift, "user count differs from the user mirror")
	}
	sort.Strings(drift)
	return drift
}

// ── reconcile ───────────────────────────────────────────────────────────

type ReconcileReport struct {
	At            int64       `json:"at"`
	Status        string      `json:"status"`
	Rebuilt       bool        `json:"rebuilt"`
	RebuildReason string      `json:"rebuildReason,omitempty"`
	Drift         []string    `json:"drift"`
	Opened        []string    `json:"opened"`
	Closed        []string    `json:"closed"`
	Swept         SweepReport `json:"swept"`
	OpenTrades    int         `json:"openTrades"`
	Records       int         `json:"records"`
	Pending       int         `json:"pending"`
}

// Repaired reports whether the pass changed anything.
func (r ReconcileReport) Repaired() bool {
	return r.Rebuilt || len(r.Opened) > 0 || len(r.Closed) > 0 ||
		len(r.Swept.Committed) > 0 || len(r.Swept.Released) > 0
}

// Reconcile verifies the book against MongoDB and repairs it.
func Reconcile(ctx context.Context) (ReconcileReport, error) {
	rep := ReconcileReport{At: util.NowMs(), Drift: []string{}, Opened: []string{}, Closed: []string{}}
	defer func() {
		if rep.Repaired() || len(rep.Drift) > 0 {
			metrics.IncrementCounter("exposure.reconcile_repairs", 1, 0)
			logx.Warn("exposure book reconciled with repairs",
				"rebuilt", rep.Rebuilt, "reason", rep.RebuildReason, "drift", rep.Drift,
				"opened", rep.Opened, "closed", rep.Closed,
				"sweptCommitted", rep.Swept.Committed, "sweptReleased", rep.Swept.Released)
			actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			audit.Write(actx, audit.Entry{
				ActorRole: "system", Action: AuditBookReconciled, Resource: "ExposureBook",
				ResourceID: BookNamespace(), After: rep, Reason: rep.RebuildReason,
			})
		}
	}()

	sw, err := SweepReservations(ctx)
	if err != nil {
		return rep, err
	}
	rep.Swept = sw

	st, err := GetBookStatus(ctx)
	if err != nil {
		return rep, err
	}
	if st.Status != BookReady {
		rep.RebuildReason = "book " + st.Status
		res, err := RebuildBook(ctx, RebuildOptions{Reason: rep.RebuildReason})
		if err != nil && err != ErrBookUnavailable {
			return rep, err
		}
		rep.Rebuilt = res.Rebuilt
		rep.Status = statusOr(ctx, st.Status)
		return rep, nil
	}

	// Redis first, MongoDB second: a record seen in the snapshot was committed
	// after its insert, so the trade is visible to the read that follows.
	snap, err := takeSnapshot(ctx)
	if err != nil {
		return rep, err
	}
	if snap.State["status"] != BookReady {
		rep.Status = statusOr(ctx, BookMissing)
		return rep, nil
	}
	if drift := checkInternal(snap); len(drift) > 0 {
		rep.Drift = drift
		rep.RebuildReason = "counters disagree with their trades"
		res, err := RebuildBook(ctx, RebuildOptions{Reason: rep.RebuildReason, Force: true, Wait: 5 * time.Second})
		if err != nil && err != ErrBookUnavailable {
			return rep, err
		}
		rep.Rebuilt = res.Rebuilt
		rep.Status = statusOr(ctx, BookDirty)
		return rep, nil
	}

	records := map[string]recordSnap{}
	owner := map[string]string{}
	for u, recs := range snap.Records {
		for t, r := range recs {
			records[t] = r
			owner[t] = u
		}
	}
	rep.Records, rep.Pending = len(records), len(snap.Pending)

	cur, err := mongox.C(models.CollBinaryTrades).Find(ctx, openTradeFilter(), options.Find().SetProjection(openTradeProjection))
	if err != nil {
		return rep, err
	}
	var open []bookTrade
	if err := cur.All(ctx, &open); err != nil {
		return rep, err
	}
	rep.OpenTrades = len(open)
	now := time.Now()
	graceCut := now.Add(-ReconcileGrace)

	inMongo := make(map[string]bool, len(open))
	for _, t := range open {
		id := t.ID.Hex()
		inMongo[id] = true
		r, counted := records[id]
		if counted {
			o := t.open()
			if owner[id] != o.UserID || r.AssetID != o.AssetID || r.Direction != dirArg(o.Direction) ||
				r.Stake != o.Stake || r.Profit != o.Profit || r.Bucket != BucketOf(o.ExpiryTimestamp) {
				// The record does not describe the trade: replace it.
				if _, err := CloseTrade(ctx, id, owner[id]); err != nil {
					return rep, err
				}
				if _, err := forceOpen(ctx, o); err != nil {
					return rep, err
				}
				rep.Closed = append(rep.Closed, id)
				rep.Opened = append(rep.Opened, id)
			}
			continue
		}
		if _, pending := snap.Pending[id]; pending {
			continue // in flight, or the sweeper's job once it lapses
		}
		if t.CreatedAt.After(graceCut) {
			continue // its commit may still be on the way
		}
		if _, err := forceOpen(ctx, t.open()); err != nil {
			return rep, err
		}
		rep.Opened = append(rep.Opened, id)
	}

	// Records whose trade is no longer an open LIVE trade.
	var gone []bson.ObjectID
	goneIDs := map[string]bool{}
	for id := range records {
		if inMongo[id] {
			continue
		}
		goneIDs[id] = true
		if oid, err := bson.ObjectIDFromHex(id); err == nil {
			gone = append(gone, oid)
		}
	}
	if len(goneIDs) > 0 {
		finished := map[string]bookTrade{}
		if len(gone) > 0 {
			cur, err := mongox.C(models.CollBinaryTrades).Find(ctx, bson.M{"_id": bson.M{"$in": gone}},
				options.Find().SetProjection(openTradeProjection))
			if err != nil {
				return rep, err
			}
			var rows []bookTrade
			if err := cur.All(ctx, &rows); err != nil {
				return rep, err
			}
			for _, r := range rows {
				finished[r.ID.Hex()] = r
			}
		}
		for id := range goneIDs {
			if t, ok := finished[id]; ok && t.live() {
				continue // became open again between the reads; next pass
			} else if ok {
				at := t.UpdatedAt
				if t.SettledAt != nil {
					at = *t.SettledAt
				}
				if at.After(graceCut) {
					continue // its release may still be on the way
				}
			}
			// Finished long enough ago, or no such trade at all.
			if _, err := CloseTrade(ctx, id, owner[id]); err != nil {
				return rep, err
			}
			rep.Closed = append(rep.Closed, id)
		}
	}
	sort.Strings(rep.Opened)
	sort.Strings(rep.Closed)

	if err := redisx.R().HSet(ctx, BookNamespace()+":state", "lastReconciledAt", strconv.FormatInt(rep.At, 10)).Err(); err != nil {
		return rep, err
	}
	rep.Status = BookReady
	return rep, nil
}

func statusOr(ctx context.Context, fallback string) string {
	st, err := GetBookStatus(ctx)
	if err != nil {
		return fallback
	}
	return st.Status
}
