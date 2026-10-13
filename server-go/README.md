# server-go

The platform's backend: the HTTP API, the websocket hub at `/ws` and the
background workers, in one Go process. It is the only backend - the React
`client/` and `admin/` apps talk to it. It began as a port of a Node/TypeScript
server (since removed), which is why `CONVENTIONS.md` still describes the
porting rules; the API, MongoDB documents and Redis keys it defined are kept.

## Requirements

- Go 1.27 (`/c/Users/rijju/sdk/go/bin` on this machine)
- MongoDB running as a replica set (transactions) and Redis. From the repo
  root: `docker compose up -d`.

## Configuration

Environment variables are documented in the repo-root `.env`.
`config.Load()` reads `server-go/.env` and then `../.env`, so running from
`server-go/` picks up the repo-root file automatically. Real environment
variables take precedence.

Most relevant: `MONGO_URI`, `REDIS_URL`, `JWT_SECRET`, `JWT_REFRESH_SECRET`,
`PORT` (default 4000), `HOST`, `NODE_ENV`, `LOG_LEVEL`, `WORKERS_ENABLED`,
`MARKET_DATA_PROVIDER`, `HEDGE_PROVIDER`, `CLIENT_URL`, `ADMIN_URL`,
`UPLOAD_DIR`.

## Run

From the repo root, `npm run dev` starts this server (through
`scripts/go.mjs`, which finds the Go toolchain) together with both frontends;
`npm run seed` runs the seed. Directly:

```bash
cd server-go
go run ./cmd/server                 # API + websocket + workers
go run ./cmd/seed                   # idempotent development seed

# an extra API-only instance (workers are leader-elected either way):
PORT=4100 WORKERS_ENABLED=false go run ./cmd/server
```

At startup, before indexes are created, `models.Migrate` runs idempotent data
migrations (currently: back-fill `wallets.accountType = "LIVE"` and drop the
legacy unique `{userId, currency}` wallet index in favour of
`{userId, accountType, currency}`).

Build binaries (no make needed):

```bash
go build -o bin/server.exe ./cmd/server
go build -o bin/seed.exe   ./cmd/seed
```

Check and test:

```bash
go build ./... && go vet ./... && go test ./...
```

Stop the server with Ctrl+C (SIGINT) or SIGTERM: workers stop first, then
the websocket hub, the HTTP server, Redis and MongoDB, with a 15 s limit.

## Layout

```
cmd/server        process bootstrap and router
cmd/seed          development seed
internal/config   environment, constants (Redis keys, events, channels), RBAC
internal/models   Mongo documents, collections, indexes, JSON views
internal/platform httpx (envelope, middleware, validation), mongox, redisx,
                  ws hub, leader election, logging, utilities
internal/service  business logic, one package per domain
internal/api      HTTP handlers, one package per route group; api/admin
                  mounts every /admin route
internal/workers  worker lifecycle
```

Routes: `/health` and `/ready` at the root, then `/auth`, `/users`, `/assets`, `/market-data`, `/binary`,
`/wallet`, `/transfers`, `/notifications`, `/support`, `/risk` and `/admin`.
`GET /ws?token=<access token>` upgrades to the websocket.
