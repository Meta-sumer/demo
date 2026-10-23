# Permission matrix (F-19)

**Finding:** F-19 - Security, session and admin controls need production
hardening. This document is the permission matrix the finding asks for.

**Source.** The matrix was built by reading these files at commit `493abbd`:

- the roles and permissions in `server-go/internal/config/permissions.go`;
- the route registrations in `server-go/internal/api/admin/*.go`,
  `server-go/internal/api/auth/routes.go`, `server-go/internal/api/support/routes.go`,
  `server-go/internal/api/risk/routes.go` and `server-go/internal/api/transfers/routes.go`;
- the websocket channel rules in `server-go/internal/platform/ws/hub.go` `canSubscribe`.

Permissions that the USDT work adds will be listed in `docs/audit/usdt/`
([usdt-flows.md](../usdt/usdt-flows.md) §1.2). The USDT admin routes reuse
the existing money permissions; the operator resolution of a withdrawal whose
BeeX answer did not match (`POST /admin/usdt/withdrawals/{id}/mark-completed`
and `.../mark-failed`) needs `withdrawals:approve`.

> **Pending additions.** Work in progress at the time of writing adds two new
> admin route groups, `/admin/pricing` and `/admin/usdt`. Add their rows to
> §3 when that work is committed, and re-check §2 if it adds permissions.

## 1. How a check works

- **Roles are labels; the server checks permissions.**
  `config.HasPermission(role, granted, p)` is true when any of these holds:
  - the role is `SUPER_ADMIN`;
  - `p` is in the role's built-in set;
  - `p` is one of the user's explicit extra grants. Extras that are not valid
    permissions are dropped by `PermissionsForRole`.
- **The permissions are live.** They are read from the session state on every
  request (`platform/authtoken/session.go` `CheckSession`,
  `platform/httpx/middleware.go` `Authenticate`). A role or permission change
  also ends the user's sessions (`service/users/users.go` `AdminUpdateUserBy`).
- **The middleware.** In `platform/httpx/middleware.go`:
  - `RequireAdmin` needs one of the admin roles (SUPPORT, FINANCE,
    RISK_MANAGER, ADMIN, SUPER_ADMIN).
  - `RequirePermission(a, b, …)` needs **all** of the listed permissions.
  - `RequireRole(r)` needs exactly that role.
- **Rate limits.** Every `/admin` route group applies the `admin` limit
  (600/min per user). Write routes add `admin:write` (120/min per user).

## 2. Roles × permissions

There are 28 permissions. `Y` means the role holds the permission by default.
USER (customers) holds none.

| Permission | SUPPORT | FINANCE | RISK_MANAGER | ADMIN | SUPER_ADMIN |
|---|:-:|:-:|:-:|:-:|:-:|
| `users:read` | Y | Y | - | Y | Y |
| `users:write` | - | - | - | Y | Y |
| `users:suspend` | - | - | - | Y | Y |
| `trades:read` | Y | Y | Y | Y | Y |
| `trades:cancel` | - | - | - | Y | Y |
| `assets:read` | Y | - | Y | Y | Y |
| `assets:write` | - | - | - | Y | Y |
| `payouts:read` | - | - | Y | Y | Y |
| `payouts:write` | - | - | - | Y | Y |
| `risk:read` | - | - | Y | Y | Y |
| `risk:write` | - | - | Y | Y | Y |
| `exposure:read` | - | - | Y | Y | Y |
| `hedge:read` | - | - | Y | Y | Y |
| `hedge:write` | - | - | Y | Y | Y |
| `wallets:read` | Y | Y | - | Y | Y |
| `wallets:adjust` | - | Y | - | - | Y |
| `deposits:read` | Y | Y | - | Y | Y |
| `deposits:approve` | - | Y | - | - | Y |
| `withdrawals:read` | Y | Y | - | Y | Y |
| `withdrawals:approve` | - | Y | - | - | Y |
| `market-data:read` | Y | - | Y | Y | Y |
| `reports:read` | - | Y | Y | Y | Y |
| `audit:read` | - | - | - | Y | Y |
| `reconciliation:read` | - | Y | - | Y | Y |
| `reconciliation:resolve` | - | - | - | - | Y |
| `settings:write` | - | - | - | - | Y |
| `support:read` | Y | - | - | Y | Y |
| `support:write` | Y | - | - | Y | Y |

**Notes:**

- `ADMIN` = SUPPORT ∪ RISK_MANAGER ∪ {`users:write`, `users:suspend`,
  `trades:cancel`, `assets:write`, `payouts:write`, `reports:read`,
  `audit:read`, `reconciliation:read`}.
- **ADMIN cannot move money.** It has no `wallets:adjust`,
  `deposits:approve` or `withdrawals:approve`. Only FINANCE and SUPER_ADMIN
  can.
- **By default only SUPER_ADMIN** holds `reconciliation:resolve` and
  `settings:write`. A SUPER_ADMIN can grant either to another user as an
  explicit extra.
- `market-data:write`, `settings:read` and `admin:manage` were removed in
  `493abbd` because they gated nothing. A stored grant of one of them is
  ignored (`PermissionsForRole`; test `TestRemovedPermissionsAreDropped` in
  `config/security_test.go`).

## 3. Admin routes and the permission each needs

Unless noted, every route below also needs an authenticated session and an
admin role (`Authenticate`, `RequireAdmin`).

### 3.1 Sign-in (no session yet)

| Route | Needs | Code |
|---|---|---|
| `POST /admin/login` | Password; `auth:login` limit (10/min per IP). An admin role continues to the second step | `api/auth/routes.go` `MountAdminLogin` |
| `POST /admin/login/2fa` | The single-use challenge token from `/admin/login` plus a TOTP or recovery code; `auth:mfa` limit (10/min per IP) | same |
| `POST /admin/login/2fa/enrol`, `POST /admin/login/2fa/enrol/confirm` | Challenge token (first sign-in enrolment) | same |

### 3.2 Dashboard, reports, settings, reconciliation (`api/admin/core.go` `mountCore`)

| Route | Permission |
|---|---|
| `GET /admin/dashboard`, `/admin/dashboard/series`, `/admin/dashboard/assets` | Any admin role. **Each figure is filtered**: exposure needs `exposure:read`; P&L, payouts and balance totals need `reports:read`; the pending deposit and withdrawal queues need `deposits:read` and `withdrawals:read` (`service/admin/admin.go` `VisibilityFor`). Test: `TestSupportCannotReadExposureOrPnl` |
| `GET /admin/metrics/latency` | Any admin role |
| `GET /admin/reports/pnl`, `GET /admin/reports/counters` | `reports:read` |
| `GET /admin/audit-logs` | `audit:read` |
| `GET /admin/settings/risk` | `risk:read` |
| `PUT /admin/settings/risk` | `risk:write` (write limit) |
| `GET /admin/reconciliation` | `reconciliation:read` |
| `POST /admin/reconciliation/run` | `reconciliation:read` (write limit) |
| `POST /admin/reconciliation/{id}/resolve` | `reconciliation:resolve` (write limit) |

### 3.3 Trading, risk, hedging, market data (`api/admin/trading.go` `mountTrading`)

| Route | Permission |
|---|---|
| `GET /admin/binary/trades`, `GET /admin/binary/trades/{id}` | `trades:read` |
| `POST /admin/binary/trades/{id}/cancel`, `POST /admin/binary/trades/cancellations/repair` | `trades:cancel` (write limit) |
| `GET /admin/exposure` | `exposure:read` |
| `GET /admin/risk` | `risk:read` |
| `GET /admin/hedging`, `GET /admin/hedging/orders` | `hedge:read` |
| `GET /admin/hedging/status`, `GET /admin/hedging/fills`, `GET /admin/hedging/alerts`, `GET /admin/hedging/orders/{id}/replay` (`api/admin/hedging.go`) | `hedge:read` |
| `POST /admin/hedging/orders/{id}/cancel` | `hedge:write` (write limit) |
| `PUT /admin/hedging/fx-rate`, `POST /admin/hedging/reconcile`, `POST /admin/hedging/adjustments`, `POST /admin/hedging/alerts/{id}/resolve` (`api/admin/hedging.go`; audited) | `hedge:write` (write limit) |
| `GET /admin/market-data` | `market-data:read` |

### 3.4 Money (`api/admin/finance.go` `mountFinance`, `mountPaymentMethods`)

| Route | Permission |
|---|---|
| `GET /admin/wallets` | `wallets:read` |
| `GET /admin/deposits` | `deposits:read` |
| `GET /admin/withdrawals` | `withdrawals:read` |
| `POST /admin/deposits/{id}/approve` | `deposits:approve` (write limit) |
| `POST /admin/withdrawals/{id}/approve` | `withdrawals:approve` (write limit) |
| `POST /admin/transfers/{id}/reject` | **both** `deposits:approve` **and** `withdrawals:approve` (write limit) |
| `POST /admin/transfers/{id}/complete` | `deposits:approve` for a deposit, `withdrawals:approve` for a withdrawal; checked in the handler against the transfer's kind (`completeTransfer`; write limit) |
| `POST /admin/transfers/repair` | **both** `deposits:approve` **and** `withdrawals:approve` (write limit) |
| `GET /admin/payment-methods` | `deposits:read` |
| `PUT /admin/payment-methods`, `POST /admin/payment-methods/{key}/active` | `settings:write` (write limit) |

### 3.5 Users (`api/admin/users.go` `mountUsers`)

| Route | Permission |
|---|---|
| `GET /admin/users`, `GET /admin/users/{id}` | `users:read` |
| `GET /admin/users/{id}/transactions` | `wallets:read` |
| `PATCH /admin/users/{id}` | `users:write` (write limit), plus rules enforced in `service/users/users.go` `AdminUpdateUserBy`:<br/>• changing `status` also needs `users:suspend`;<br/>• changing `role` or `permissions` is **SUPER_ADMIN only**;<br/>• changing any administrative account is **SUPER_ADMIN only**;<br/>• nobody can change their own status, role or permissions.<br/>Test: `TestAdminUpdateLeastPrivilege` |
| `POST /admin/users/{id}/wallet/adjust` | `wallets:adjust` (write limit). An `Idempotency-Key` header is also required. `accountType` may be `LIVE`, `DEMO` or `FUNDING` (the USDT wallet, amount in micro-USDT); audited `wallet.adjusted` with the currency |

### 3.6 Assets and payouts (`api/admin/assets.go` `mountAssets`)

| Route | Permission |
|---|---|
| `GET /admin/assets`, `GET /admin/assets/{id}` | `assets:read` |
| `POST /admin/assets`, `PATCH /admin/assets/{id}` | `assets:write` (write limit) |
| `GET /admin/assets/{id}/payouts` | `payouts:read` |
| `POST /admin/assets/{id}/payouts` (optional `effectiveFrom` schedules the change), `DELETE /admin/assets/payouts/{id}` (cancels a scheduled change), `POST /admin/assets/payouts/repair` | `payouts:write` (write limit) |

### 3.7 Support desk (`api/support/routes.go` `MountAdmin`)

| Route | Permission |
|---|---|
| `GET /admin/support/conversations`, `GET /admin/support/conversations/{id}`, `POST /admin/support/conversations/{id}/read` | `support:read` |
| `POST /admin/support/users/{id}/conversation` | `support:write` |
| `POST /admin/support/conversations/{id}/messages`, `…/typing` | `support:write` (chat limit, 60/min) |
| `PATCH /admin/support/conversations/{id}` | `support:write` (write limit) |

### 3.8 Security (`api/admin/security.go` `mountSecurity`)

| Route | Permission |
|---|---|
| `GET /admin/security/2fa` | Any admin role (own state only) |
| `POST /admin/security/2fa/enrol`, `…/2fa/enrol/confirm`, `…/2fa/recovery-codes` | Any admin role, own account (write limit). New recovery codes need a current authenticator code |
| `GET /admin/security/admins` | **SUPER_ADMIN role** (`RequireRole`) |
| `POST /admin/security/admins/{id}/2fa/reset` | **SUPER_ADMIN role**; a reason is required; audited `admin.mfa_reset` |
| `POST /admin/security/users/{id}/sessions/revoke` | `users:write` **and** `users:suspend` (write limit). Signing out an administrative account is **SUPER_ADMIN only** (`revokeUserSessions`); audited `user.sessions_revoked` |

## 4. Permission-gated routes outside `/admin`

| Route | Needs | Code |
|---|---|---|
| `GET /risk/exposure`, `GET /risk/exposure/{id}` | `exposure:read` | `api/risk/routes.go` |
| `GET /risk/assessments`, `GET /risk/config` | `risk:read` | same |
| `GET /transfers/{id}/proof` | The transfer's owner, **or** `deposits:read` or `withdrawals:read`. Anyone else gets 404 | `api/transfers/routes.go` `proof` |

Customer routes need only an authenticated session. Writes that move money or
open positions also need an `ACTIVE` account (`RequireActiveUser`):

- `POST /binary/trades`
- `POST /binary/trades/{id}/close`
- `POST /transfers/deposits`
- `POST /transfers/withdrawals`

Customers see only their own records; another customer's trade or transfer
returns 404.

### 4.1 Public routes (no session)

These answer without a session and return no user data. Every one is rate
limited per source address except `/health` and `/ready`.

| Route | Guard | Code |
|---|---|---|
| `GET /health`, `GET /ready` | None. `/ready` reports store and feed state without reasons or ids | `api/health/routes.go` |
| `POST /auth/register`, `POST /auth/login`, `POST /auth/refresh` | `auth:register` (5/min per IP), `auth:login` (10/min per IP), `auth:refresh` (60/min per IP) | `api/auth/routes.go` |
| `POST /admin/login`, `POST /admin/login/2fa*` | See 3.1 | `api/auth/routes.go` `MountAdminLogin` |
| `POST /webhooks/beex/crypto` | HMAC signature over the exact body; `webhook:beex` (600/min per IP) | `api/usdt/webhook.go` |
| `GET /public/markets` | Read-only; `public:markets` (300/min per IP). Returns only the active instruments the pricing engine can price (symbol, name, category), per expiry the engine's indicative payout (the lower of UP and DOWN), the min/max across them and `generatedAt`. No ids, stakes, exposure, per-direction figures, halt or refusal reasons. Served from a 5-second snapshot of the cached indicative board, so a request does no pricing work | `api/public/routes.go`, `service/pricing/public.go` |

## 5. Websocket channels (`platform/ws/hub.go` `canSubscribe`)

| Channel | Who may subscribe |
|---|---|
| `user:<id>` | Only that user |
| `market:<assetId>` | Any authenticated user |
| `admin:ops` (every trade as placed and settled) | An admin role **and** `trades:read` |
| `admin:risk` | `risk:read` or `exposure:read` |
| `admin:market` | `market-data:read` |
| `admin:support` | `support:read` |

Sockets re-check these rules on every subscribe, every minute and on
revocation, and drop channels the user may no longer read
(`platform/ws/session.go`).

## 6. SUPER_ADMIN-only actions (summary)

| Action | How it is restricted |
|---|---|
| Change any user's role or explicit permissions | `AdminUpdateUserBy` (role check) |
| Change any administrative account (status, limits, flags) | `AdminUpdateUserBy` |
| Sign out an administrative account everywhere | `revokeUserSessions` |
| List every admin's 2FA state; reset another admin's 2FA | `RequireRole(SUPER_ADMIN)` in `mountSecurity` |
| Resolve a reconciliation run | `reconciliation:resolve` (default SUPER_ADMIN only) |
| Change payment methods (the account details customers pay into) | `settings:write` (default SUPER_ADMIN only) |
| Break-glass 2FA reset when no SUPER_ADMIN can sign in | Not an API. It needs shell access to the server: `seed -reset-2fa <email>` (`server-go/cmd/seed/main.go` `resetTwoFactor`), audited `admin.mfa_reset` |
