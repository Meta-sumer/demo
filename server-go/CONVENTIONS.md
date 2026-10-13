# server-go conventions

`server-go/` is the platform's only backend. It began as a drop-in port of a Node/TypeScript
server (removed on 2026-09-29), and the contract that port kept is still the contract:
the HTTP API, JSON shapes, status and error codes, websocket events, MongoDB collections
and fields, and Redis keys. The React client (`client/`) and admin (`admin/`) depend on
it; their expectations are in `client/src/types/api.ts`, `admin/src/types/api.ts` and
`client|admin/src/services/api.ts`. Change the API only together with those files, and
add fields rather than rename or remove them.

Where this document says "TS", it means the original Node implementation; the Go code
now carries that behaviour, so read the Go code as the reference.

## Toolchain

```bash
export PATH="/c/Users/rijju/sdk/go/bin:$PATH"
cd /c/Users/rijju/binary-trading/server-go
go build ./... && go vet ./... && go test ./... && gofmt -l .
```

From the repo root, `npm run dev`, `npm run seed`, `npm test` and `npm run build` run Go
through `scripts/go.mjs`, which finds `go` on PATH or at `%USERPROFILE%\sdk\go`.

Module path: `binarytrading`. Go 1.27. Prefer the packages already in `go.mod`: chi v5,
mongo-driver v2, go-redis v9, gorilla/websocket, golang-jwt v5, google/uuid, godotenv,
golang.org/x/crypto (bcrypt), golang.org/x/sync (errgroup). Add a dependency only when
nothing here covers the need.

When several agents work at once, each edits only the packages it was given, and
`go mod tidy` waits until they are done. Changes to `internal/platform/**` and
`internal/config/**` are shared by every package: keep them additive and mention them in
the final report.

## Layout

```
cmd/server/main.go            process bootstrap
internal/config               env (config.E), constants (RedisKeys, Ev*, Ch*, Lat*), RBAC
internal/platform/apperr      error taxonomy: apperr.New(code,msg,status,details?), Validation, NotFoundErr, ...
internal/platform/httpx       H(), OK/Created/Send/Paginate, middleware, V/Q validation, DecodeBody, IDParam
internal/platform/mongox      C(name), WithTransaction, Now(), IsDuplicateKey, IsNotFound, ParseID, JSONValue
internal/platform/redisx      R() (*redis.Client), locks: Acquire/Release/Renew/WithLock, NewConnection()
internal/platform/ws          Emit(channels, event, payload), Publish, hub
internal/platform/iso         iso.Time: JSON time exactly like JS toISOString()
internal/platform/util        money (CalculateProfit...), RoundHalfUp (= JS Math.round), Reference(), Fingerprint(),
                              ParseDuration, NowMs, UTCDayKey, TradingWindow/IsWithinTradingHours/NextTradingOpen,
                              Summarise, birth date
internal/platform/leader      leader.New(Options{Name, Interval, OnTick...}) periodic single-instance loops
internal/platform/authtoken   JWT sign/verify (Node-compatible)
internal/models               every Mongo document struct + collection const + indexes + views
internal/service/<name>       business logic (one package per TS module)
internal/api/<name>           HTTP handlers; each exports func Mount(r chi.Router)
internal/workers              background workers
internal/testsupport          test-only helpers: LiveStores (throwaway Mongo database, shared Redis; skips if unreachable)
```

Service dependency order (imports must form a DAG — Go forbids cycles):

```
metrics, audit, notifications, riskconfig           (leaves)
assets -> payouts -> marketdata(+providers)          marketdata also uses riskconfig, metrics
exposure(+sentiment) -> assets
risk -> assets, exposure, marketdata, riskconfig
wallets -> models only
users -> wallets, exposure, audit ; auth -> users, wallets, audit
transfers(payment methods, attachments) -> wallets, audit ; support -> audit
binary(+earlyclose, slippage) -> assets, exposure, marketdata, risk, riskconfig, metrics, wallets, payouts
settlement -> exposure, marketdata, riskconfig, metrics, wallets, notifications
expiry -> settlement, marketdata, metrics
hedging(+providers) -> risk, marketdata, riskconfig, metrics, assets
reconciliation -> audit, models
api/* -> any service ; workers -> any service
```

The TS code has import cycles; they were almost all model-only imports, which is why
every model lives in `internal/models`. If a service-level cycle remains, break it by
moving the shared piece down (e.g. a view into `models`) or by injecting a function
variable set at startup — never by duplicating business logic.

Package names: directory `service/marketdata` → `package marketdata`. Name exported
functions after the TS ones (`requireAssetDoc` → `RequireAssetDoc`). Every function doing
I/O takes `ctx context.Context` first.

## Models (`internal/models`)

- One file per TS model, e.g. `binary_trade.go`. Collection name as a constant
  (`CollBinaryTrades = "binary_trades"`), exactly the Mongoose `collection` option.
- `ID bson.ObjectID \`bson:"_id,omitempty"\``. Refs (`Schema.Types.ObjectId`) are
  `bson.ObjectID`; nullable refs `*bson.ObjectID`.
- Numbers: `int64` for integral values (money in minor units, counts, versions, epoch-ms
  timestamps, latencies stored as whole ms); `float64` for prices, percentages, ratios,
  scores, and anything that may be fractional. When unsure whether a stored value can be
  fractional, use float64.
- Dates: `time.Time`, nullable `*time.Time`. Store `mongox.Now()` (UTC, ms precision).
- Field names: bson tag = the Mongoose path, verbatim. Nested schema objects become
  nested structs with their own tags.
- Nullability: Mongoose `default: null` → pointer **without** omitempty (null is written,
  as Mongoose does). Optional with no default → pointer **with** `omitempty`. Arrays → slices
  (nil is stored as `[]`). `Schema.Types.Mixed` → `any`.
- Mongoose applies `lowercase`, `uppercase`, `trim`, and defaults on write. Go does not —
  apply them explicitly in the service before insert/update/query.
- `timestamps: true` means every insert sets `createdAt` and `updatedAt`, and **every
  update must `$set` `updatedAt: mongox.Now()`**; upserts `$setOnInsert` `createdAt`.
  Respect `timestamps: { createdAt: true, updatedAt: false }` variants.
- Indexes: register in the file's `init()` with `RegisterIndexes(mongox.Index{...})`, same
  keys and options (`options.Index().SetUnique(true).SetPartialFilterExpression(...)`,
  sparse, TTL `SetExpireAfterSeconds`). Include `index: true` field-level indexes.
- Enum values as typed string constants.

## JSON (views and responses)

- JSON field names and shapes must equal what the TS code sends. Views (`toXxxView`) are
  ported to `ToXxxView(doc) XxxView` returning a struct with json tags.
- Dates: `iso.Time` / `*iso.Time` (never raw `time.Time` in JSON). Epoch-ms fields stay
  numbers.
- `T | null` → pointer without omitempty (marshals `null`). Optional keys that TS omits
  when undefined → `omitempty`.
- **Slices must never marshal as `null`** where TS sends `[]` — initialise them.
- ObjectIDs in JSON are hex strings.
- Where TS returns a Mongoose `lean()` document directly, the JSON has `_id` (string),
  every stored field, `createdAt`/`updatedAt` ISO strings, and `__v` if present. Either
  decode into `bson.M` and pass through `mongox.JSONValue`, or use a struct with
  `json:"_id"`. Check what the frontend reads.
- Maps from TS `Record<string, X>` → `map[string]X`.

## HTTP

- Mount: the TS `app.register(xRoutes, { prefix: '/x' })` becomes
  `r.Route("/x", xapi.Mount)` in `cmd/server`. Your `Mount(r chi.Router)` registers the
  routes relative to that prefix.
- Plugin-level `app.addHook('preHandler', authenticate)` → `r.Use(httpx.Authenticate)`
  (use `r.Group` when only some routes are protected). Route `preHandler` arrays become
  per-route middleware **in the same order**:
  `r.With(httpx.RequireActiveUser, httpx.RateLimit(httpx.RateLimits.Trade)).Post("/trades", httpx.H(createTrade))`.
- Handlers: `func(w http.ResponseWriter, r *http.Request) error`, wrapped with `httpx.H`.
  Respond with `httpx.OK(w, data)`, `httpx.Created(w, data)`,
  `httpx.Send(w, status, data, meta)`, lists with `httpx.Paginate(items, page, limit, total)`.
  Return errors; `httpx.WriteError` maps them (apperr → its status; duplicate key → 409).
- Validation replaces Zod. Body: `httpx.DecodeBody(r, &body)` then check fields with
  `v := httpx.NewV()` (`Required`, `Str`, `Int`, `Float`, `Positive`, `IsInt`, `Enum`,
  `Email`, `Match`, `ObjectID`, `Check`) and `return v.Err("request body")`. Query:
  `q := httpx.Query(r)`; `page, limit := q.Page()`; `q.Int/OptInt/OptFloat/Enum/OptEnum/
  OptBool/OptDate/OptObjectID/Str/OptStr`; `q.Err()`. Path: `id, err := httpx.IDParam(r, "id")`.
  Use pointer fields (or `httpx.Opt[T]` when null must be told apart from absent) to detect
  missing keys. Apply Zod `.trim()`, `.toLowerCase()`, `.default()` yourself. Keep the
  exact min/max/enum rules of each schema.
- Actor: `actor, err := httpx.RequireActor(r)`; `actor.UserID` (hex), `actor.ObjectID()`,
  `actor.Role`, `actor.Permissions`. `httpx.AssertSelfOrPermission`, `httpx.HasPermission`.
- Request metadata: `httpx.ClientIP(r)`, `httpx.UserAgent(r)`, `httpx.AuditMeta(r)`.
- Audit: `e := audit.FromRequest(actor, httpx.AuditMeta(r)); e.Action = audit.X; ...;
  audit.Log(e)` (fire and forget) or `audit.Write(ctx, e)` (awaited — use where TS awaits
  `writeAudit`).
- Headers such as `idempotency-key`: `r.Header.Get("Idempotency-Key")`.

## Behaviour

- Error codes, messages and status codes are part of the API: copy them exactly.
- Money is integer minor units (`int64`). JS `Math.round(x)` → `util.RoundHalfUp(x)` (not
  `math.Round`, which rounds half away from zero). JS numbers are float64: when TS does
  `a * b / 100` on numbers, do it in float64 then round the way TS does.
- `withTransaction(async (session) => ...)` → `mongox.WithTransaction(ctx, func(ctx context.Context) error {...})`;
  pass that inner ctx to every collection call inside. `{ session }` options disappear.
- Mongoose `findOneAndUpdate(filter, update, { new: true })` →
  `FindOneAndUpdate(ctx, filter, update, options.FindOneAndUpdate().SetReturnDocument(options.After))`;
  a nil result is `mongo.ErrNoDocuments` (`mongox.IsNotFound(err)`).
- Fire-and-forget (`void promise`, `emit`) → a goroutine with
  `context.WithTimeout(context.Background(), ...)` — never the request ctx.
- `Promise.all` → sequential calls are fine unless latency matters (then errgroup).
- Websocket: `ws.Emit([]string{config.ChUser(id)}, config.EvWalletUpdated, view)` — same
  channels, event names and payload shapes as TS.
- Redis: keys come from `config.RedisKeys`; keep their data types and encodings (hash field
  names, JSON strings, ZSET scores) stable, since running instances share them.
- Logging: `logx.Info/Warn/Error/Debug(msg, "key", value, ...)`.
- Keep the substantive "why" comments from the TS source, condensed. Do not narrate.
- Put unit tests next to the code (`_test.go`) and keep `go test ./...` green. Tests that need
  Mongo or Redis must skip when those are unreachable and clean up what they create
  (`internal/testsupport` has helpers).

## Final report

End with: files created and changed, API changes (and the client types that must follow),
exported functions other packages will need (signatures), changes to platform or config
packages, and anything left undone and why.
