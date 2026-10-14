# 02 — Money: wallets, ledger, deposits, withdrawals, USDT

How the platform stores and moves money: the wallet model, the append-only
ledger, manual INR deposits and withdrawals, the USDT wallet through BeeX
Payments, reconciliation and the audit trail.

Everything here comes from the code. Paths are relative to the repo root;
Go paths are under `server-go/internal/` unless the path starts with
`client/` or `admin/`.

> **Read this first: USDT is not wired up.** The USDT service
> (`service/usdt`, `service/beex`) is complete as a library, but **no package
> outside those two imports it**. `cmd/server/main.go` mounts no
> `/wallet/usdt/*`, `/admin/usdt/*` or webhook routes. `workers/workers.go`
> never calls `usdt.Start()`, and nothing calls `models.SeedUsdtNetworks`. The
> client (`client/src/services/api.ts`) still calls `/wallet/usdt/...`. Today
> those calls hit no route. Section 5 describes the service logic as written,
> so whoever wires it up knows the contract. Reconciliation (section 6) does
> already run the USDT checks, because it reads the collections directly.

---

## 1. Money representation

### 1.1 INR: integer minor units (paise)

The database, the API and websocket payloads store every INR amount as an
`int64` count of **paise** (`₹100.00 → 10000`).

| Where | Helper | Behaviour |
|---|---|---|
| `platform/util/util.go` | `MinorUnitsPerMajor = 100` | |
| | `ToMinor(major float64) int64` | `int64(floor(major*100 + 0.5))`, which is JS `Math.round` |
| | `FromMinor(minor) float64` | `minor / 100` |
| | `FormatMinor(minor) string` | en-IN grouping, `₹1,23,456.78` |
| | `CalculateProfit(stake, pct)` | `round_half_up(stake * pct / 100)` (pct is `82` for 82 %) |
| | `CalculateWinningCredit` / `CalculateLiability` | `stake + CalculateProfit(...)` (exposure and liability only, never credited as such) |
| `client/src/utils/format.ts` (same in `admin/src/utils/format.ts`) | `MINOR = 100`, `toMinor = Math.round(major*100)`, `fromMinor = minor/100`, `formatMoney`, `formatSignedMoney`, `formatMoneyCompact` | `Intl` en-IN currency formatting |

The API rejects non-integer amounts. For example, `minorAmount` in
`api/transfers/multipart.go` requires an integer, `> 0` and `<= 1_000_000_000_000`.

### 1.2 USDT: integer micro-USDT

`1 USDT = 1_000_000 micro`, 6 decimals (`models.USDTDecimals = 6`). No float
ever touches a USDT amount. Parsing and printing are done with string and
integer operations.

| Where | Helper | Behaviour |
|---|---|---|
| `platform/micro/micro.go` | `Scale = 1_000_000`, `Decimals = 6` | |
| | `ParseStrict(s)` | Customer input. Unsigned decimal, at most 6 decimals, no sign, exponent or spaces. Errors: `ErrSyntax`, `ErrPrecision`, `ErrNegative`, `ErrRange` (more than 18 digits). |
| | `ParseLenient(s)` | Provider amounts. Trims spaces, accepts a leading `+` and an exponent, and **floors** anything past the 6th decimal. Returns `exact=false` when something was dropped. A negative amount is an error. |
| | `Format(v)` / `FormatFixed(v)` | Shortest exact decimal (`100500000 → "100.5"`) or exactly 6 decimals |
| | `PercentFloor(amount, "0.5")` | `floor(amount * percent / 100)` with `big.Int` |
| `service/beex/types.go` | `Amount.Micro()` → `ParseLenient`; `AmountOf(v)` → `Format` | BeeX amounts are decimal strings or JSON numbers |
| `client/src/utils/usdt.ts` | `USDT_DECIMALS = 6`, `MICRO_PER_USDT = 1_000_000`, `parseUsdt`, `formatUsdt`, `toUsdtInput` | Regex `^(?:(\d+)(?:\.(\d{0,6}))?\|\.(\d{1,6}))$`. Refuses `1,000`, exponents and signs. Returns `null` past 16 digits or past `Number.isSafeInteger`. |
| `admin/src/utils/usdt.ts` | Its own integer-digit formatter (also takes `bigint` and strings) | Differs from the client file |

---

## 2. Wallets

### 2.1 Accounts and documents

Collection `wallets` (`models/wallet.go`). Unique index
`{userId, accountType, currency}`.

| accountType | Currency | Purpose | Created by |
|---|---|---|---|
| `LIVE` | `INR` | Real trading money, manual deposits and withdrawals, the house book | `wallets.GetOrCreateWallet` on register and login (`service/auth/auth.go`), and on `GET /wallet` |
| `DEMO` | `INR` | Practice money, never in the house book | `wallets.EnsureDemoWallet`, lazily |
| `FUNDING` | `USDT` | USDT wallet (BeeX). Real money, but **not trading money**. | `usdt.fundingWallet` |
| `LIVE` | `USDT` | Target of "transfer to trading". Not in the trading account list. | `usdt.TransferToTrading` |

- `AccountTypeValues = [LIVE, DEMO]`. FUNDING can never be requested where an
  API takes `accountType`.
- `NormAccountType`: a missing or unknown value means `LIVE`. The startup
  migration `models/migrate.go` back-fills `accountType: "LIVE"` and drops the
  legacy `{userId, currency}` unique index.
- `NotDemo()` is `{$nin: [DEMO, FUNDING]}`. Every house-book aggregate uses it,
  and it matches documents with no `accountType`. `INRCurrency()` is
  `{$in: ["INR", null]}`.

Fields: `balance` (total owned, **including** locked), `lockedBalance`
(reserved by open trades or pending withdrawals), `version` (incremented on
every mutation), timestamps. `availableBalance` is **always derived and never
stored**:

```
availableBalance = balance - lockedBalance
```

`WalletView` (`models.ToWalletView`) returns all three.

### 2.2 The lock model

`models/wallet.go` and `service/wallets/wallet.go` define these movements.
`P = CalculateProfit(stake, payoutPercent)` is fixed when the trade opens
(`service/binary/binary.go`).

| Event | balance Δ | locked Δ | Guard | Ledger type / movement | Idempotency key |
|---|---|---|---|---|---|
| Trade open (`LockTradeFunds`) | 0 | +stake | available ≥ stake | `TRADE_LOCK` / `LOCK` | `trade:lock:<tradeId>` |
| Lose (`SettleTradeLoss`) | −stake | −stake | locked ≥ stake, balance ≥ stake | `TRADE_LOSS` / `DEBIT` | `trade:settle:<tradeId>` |
| Win (`SettleTradeWin`) | **+P** | −stake | locked ≥ stake | `TRADE_WIN` / `CREDIT` (amount = P, metadata `{stake, profit}`) | `trade:settle:<tradeId>` |
| Draw (`ReleaseTradeFunds(DRAW)`) | 0 | −stake | locked ≥ stake | `TRADE_REFUND` / `UNLOCK` | `trade:settle:<tradeId>` |
| Cancel or orphan (`ReleaseTradeFunds(CANCELLED)`) | 0 | −stake | locked ≥ stake | `TRADE_CANCEL` / `UNLOCK` | `trade:settle:<tradeId>` |
| Early close at value V (`SettleTradeEarlyClose`) | V − stake | −stake | locked ≥ stake | `TRADE_EARLY_CLOSE` / `CREDIT` if V ≥ stake else `DEBIT`, amount `abs(V − stake)` | `trade:settle:<tradeId>` |

Every settlement path shares the key `trade:settle:<id>`, so a trade settles
**at most once**, whether through expiry, early close, cancel or the orphan
sweep.

On a win the stake was never removed from `balance`, so the platform credits
only the profit. The root README's "Spec deviations" section explains this.

Non-trade movements (`AdjustmentInput`):

| Function | balance Δ | locked Δ | Guard | Movement |
|---|---|---|---|---|
| `CreditWallet` | +a | 0 | `a > 0`, otherwise 400 `INVALID_AMOUNT` | `CREDIT` |
| `DebitWallet` | −a | 0 | `a > 0`; available ≥ a | `DEBIT` |
| `LockWithdrawalFunds` | 0 | +a | available ≥ a | `LOCK` |
| `UnlockWithdrawalFunds` | 0 | −a | locked ≥ a | `UNLOCK` |
| `SettleWithdrawal` | −a | −a | locked ≥ a | `DEBIT` |
| `TransferBetweenAccounts` | −a from, +a to | 0 | from: available ≥ a | `DEBIT` + `CREDIT` |

### 2.3 DEMO account

`service/wallets/demo.go`:

- **Opening balance:** `models.DemoOpeningBalance = 1_000_000` paise, which is ₹10,000.00.
- **Lazy creation:** `EnsureDemoWallet` upserts the empty DEMO wallet, then
  looks for the ledger key `demo:open:<userId>`. If the key is missing, it
  credits `ADJUSTMENT_CREDIT` with reference `DemoAccount/demo-open`.
  Concurrent first requests collide on the key, so the wallet is funded once.
  The following call it: `GET /wallet?accountType=DEMO`, `GET /wallet/accounts`
  (returns `[LIVE, DEMO]`), a trader's first DEMO trade, and an admin
  adjustment of the DEMO account.
- **Reset** (`POST /wallet/demo/reset`, `ResetDemoWallet`):
  - It is refused with **409 `CONFLICT` "Close your open demo trades first"**
    when the user has DEMO trades in `OPEN` or `SETTLING`, or when the DEMO
    `lockedBalance > 0`.
  - `delta = DemoOpeningBalance − balance`. A delta of `0` is a no-op. A
    positive delta posts `ADJUSTMENT_CREDIT`, a negative delta posts
    `ADJUSTMENT_DEBIT`. The reference is `DemoAccount/demo-reset`.
  - The key is `demo:reset:<userId>:<unixMs>:<uuid>`. Every reset is a new
    operation, so a replay can never undo a later reset.
  - The route has two rate limits: `wallet` (120/min) and `wallet:transfer`
    (10/min per user). It is audited as `wallet.demo_reset`.
- DEMO rows use the same guarded, ledgered path as real money. They are
  excluded from house-book totals, P&L and `platform_result_consistency`.

### 2.4 The ledger: `wallet_transactions`

`models/wallet_transaction.go`. There is **one row per balance change**, and
rows are never updated or deleted.

| Field | Notes |
|---|---|
| `walletId`, `userId`, `accountType` | A missing `accountType` means LIVE |
| `type` | `DEPOSIT`, `WITHDRAWAL`, `TRADE_LOCK`, `TRADE_LOSS`, `TRADE_WIN`, `TRADE_REFUND`, `TRADE_EARLY_CLOSE`, `TRADE_CANCEL`, `ADJUSTMENT_CREDIT`, `ADJUSTMENT_DEBIT`, `FEE`, `TRANSFER` (`models/domain.go`). `FEE` is declared but never written. |
| `movement` | `CREDIT`, `DEBIT`, `LOCK`, `UNLOCK` |
| `amount` | Always positive. `movement` carries the direction. |
| `balanceBefore/After`, `lockedBefore/After` | Snapshot taken from the guarded update's pre-image (`ReturnDocument: Before`) |
| `referenceType` / `referenceId` | `BinaryTrade`, `Transfer`, `AdminAdjustment`, `DemoAccount`, `UsdtDeposit`, `UsdtWithdrawal`, `UsdtTransfer` |
| `idempotencyKey` | **Partial unique index** where `idempotencyKey` is a string. Rows without a key are unconstrained. A row with a key can only ever be written once. |
| `network`, `fee` | Only on USDT rows (fee is in micro-USDT) |
| `metadata` | A free-form object, `{}` by default |

Other indexes: `type`, `referenceId`, `{userId, createdAt}`,
`{walletId, createdAt}`, `createdAt`.

A withdrawal therefore posts **two or three rows with the same type
`WITHDRAWAL`**. They differ by movement: `LOCK` when requested, then either
`DEBIT` when settled or `UNLOCK` when released.

**Idempotency keys by operation:**

| Key | Written by |
|---|---|
| `trade:lock:<tradeId>` / `trade:settle:<tradeId>` | Trade engine, settlement, early close, orphan sweep |
| `demo:open:<userId>` / `demo:reset:<userId>:<ms>:<uuid>` | DEMO account |
| `adjust:ADJ_<random>` | Admin manual adjustment |
| `deposit:credit:<transferId>` | Manual deposit approval |
| `withdrawal:lock:` / `withdrawal:settle:` / `withdrawal:unlock:<transferId>` | Manual withdrawal |
| `usdt:deposit:<depositId>` | USDT deposit credit (automatic or operator) |
| `usdt:withdrawal:lock:` / `:settle:` / `:unlock:<withdrawalId>` | USDT withdrawal |
| `usdt:transfer:<userId>:<sha256(clientKey)[:32]>:out` and `:in` | USDT transfer to trading |

`GET /wallet/transactions` (`api/wallet/routes.go`):

- `accountType` defaults to LIVE, and LIVE excludes FUNDING. The currency is
  INR.
- The `type` filter enum leaves out `TRADE_EARLY_CLOSE` and `TRANSFER`.

### 2.5 Concurrency guarantees

`applyMutation` in `service/wallets/wallet.go` is **the only code that writes
`wallets`**. It makes every mutation:

1. **Guarded.** The preconditions sit in the `$expr` of the
   `findOneAndUpdate` filter, together with the `$inc`. The preconditions are:
   `balance − locked ≥ RequireAvailable`, `locked ≥ RequireLocked`, and
   non-negative results for negative deltas. Two concurrent requests cannot
   both pass. If the filter misses:
   - no wallet gives 404 `WALLET_NOT_FOUND`;
   - a locked guard gives 409 `LOCKED_FUNDS_MISMATCH`;
   - anything else gives 400 `INSUFFICIENT_BALANCE`.
2. **Serialised.** A Redis lock `lock:wallet:<uid>:<currency>` (LIVE) or
   `lock:wallet:<uid>:<ACCOUNT>:<currency>` is taken with TTL 10 s and a 15 s
   wait. A timeout returns 503 `SERVICE_UNAVAILABLE` "Your account is busy…".
   Callers that already hold the lock pass `ExternallyLocked`.
   `TransferBetweenAccounts` takes both wallet locks in sorted key order, so
   two opposite transfers cannot deadlock.
3. **Idempotent.** With a key present, the ledger is checked first and a hit
   returns `Replayed: true` without touching the balance. If two racing callers
   both miss, the loser gets a duplicate-key error on insert, reads the
   winner's row and returns it as a replay.
4. **Not cancellable.** It runs with `context.WithoutCancel`, so a client
   disconnect cannot abandon a half-done movement.
5. **Announced.** On success it emits websocket `wallet.updated` on `user:<id>`.

**Transactions.** `ENABLE_MONGO_TRANSACTIONS` (default `true`,
`config/env.go`) and a startup probe (`platform/mongox/mongo.go`) together
decide `transactionsSupported`.

- **With transactions:** the guarded update, the ledger insert and the
  caller's `InTx` hook commit together. The hook is how the USDT code flips
  `credited` or the withdrawal status. Read concern is `local`, write concern
  is `majority`, and transient errors are retried 5 times with backoff
  `10ms·2^n + jitter`.
- **With `ENABLE_MONGO_TRANSACTIONS=false`** (or when the probe fails):
  `WithTransaction` runs the function as plain sequential writes. The guard
  and the unique key still prevent double spends and double credits, but:
  - a failure *after* the balance update (ledger insert, `InTx` hook) leaves
    the balance changed with no ledger row or record;
  - `reconciliation` then reports `wallet_ledger_vs_balance` drift;
  - `usdt.Start` logs a warning in this mode.

**Orphaned stake locks** (`service/wallets/orphan_locks.go`). The trade engine
locks the stake before it inserts the trade. If the process crashes in
between, the money stays locked.

- At worker start (`workers/workers.go`), `ReleaseOrphanedTradeLocks` finds
  `TRADE_LOCK` rows that meet both conditions:
  - older than `orphanGrace = 60s`;
  - no `binary_trades` document with that id.
- It processes at most `500` per run and skips any lock that already has a
  `trade:settle:<id>` row.
- It releases each orphan with `ReleaseTradeFunds(CANCELLED)`, which shares
  the settle key with settlement.

---

## 3. Manual INR deposits

`service/transfers/transfer.go`, `api/transfers/routes.go`, `models/transfer.go`.
Collection `transfers` with `kind: DEPOSIT`.

### 3.1 Payment methods

`payment_methods` (`models/payment_method.go`, `service/transfers/payment_method.go`).

- Fields: `key` (trimmed, lower-cased, unique), `label`, `type`, the type's
  details, `instructions`, `minAmount` and `maxAmount`, `active`,
  `displayOrder`.
- `type` is `UPI` (`upiId`, `qrFileId`) or `BANK` (`accountName`,
  `accountNumber`, `ifsc`, `bankName`).
- Defaults: `minAmount = 100_00` (₹100), `maxAmount = 500_000_00` (₹5,00,000),
  `active = true`.
- Validation on upsert:
  - UPI must match `^[a-zA-Z0-9.\-_]{2,64}@[a-zA-Z]{2,32}$`;
  - an account number is 6–20 digits;
  - IFSC must match `^[A-Z]{4}0[A-Z0-9]{6}$` after upper-casing;
  - `min ≤ max`.
- Customers read active methods with `GET /transfers/payment-methods`.
- Admins:
  - `GET /admin/payment-methods` needs `deposits:read`;
  - `PUT /admin/payment-methods` and `POST /admin/payment-methods/{key}/active`
    need `settings:write`. Both write `payment_method.updated` to the audit log
    synchronously, with before and after.

### 3.2 Submitting a claim: `POST /transfers/deposits`

The route needs an authenticated, active user, with rate limit
`wallet:transfer` (10/min). It accepts JSON or `multipart/form-data`. The
multipart form has fields `amount`, `paymentMethodKey`, `utr`, `customerNote`
and the file field `proof`.

Route validation:

- `amount`: integer paise, `> 0`, `≤ 1e12`;
- `paymentMethodKey`: 2–40 characters;
- `utr`: 6–40 characters;
- `customerNote`: up to 500 characters.

Multipart limits are 1 file, 12 fields, and 4096 bytes per field (longer
values are truncated). The body cap is `MAX_UPLOAD_BYTES + 64 KiB`. Breaking a
limit returns 413 `PAYLOAD_TOO_LARGE`.

`RequestDeposit` then runs these rules:

1. `NormaliseUtr`: trim, then **upper-case**, then 6–40 characters matching
   `^[A-Z0-9-]+$`. Otherwise 422 `VALIDATION_ERROR`.
2. **A UTR or a proof file is required.** With neither, the request fails 422
   "Provide the UTR … or attach a screenshot".
3. If `paymentMethodKey` is given, it must name an *active* method, and the
   amount must lie in `[minAmount, maxAmount]`. Otherwise 422.
   *Edge case: when the key is omitted, no method limits apply. Only the
   route's `1..1e12` bound does.*
4. **UTR uniqueness.** A friendly pre-check returns 409 `CONFLICT` "That
   reference number has already been submitted…". The real guarantee is the
   partial unique index `{utr: 1}` filtered on `kind: "DEPOSIT"` and `utr` of
   type string. Withdrawals and rows without a UTR are unconstrained. Because
   the UTR is upper-cased first, changing its case does not get around the
   index. If the insert hits the index, the stored proof file is deleted and
   the request fails 409.
5. The proof is stored (section 3.4). The row is inserted with these fields:
   - `reference = "DEP_" + base64url(9 random bytes)` (unique index);
   - `status = PENDING`, `currency = INR`;
   - `method` = the method's label, plus `paymentMethodId`.
6. **Nothing is credited.** The route writes `deposit.created` to the audit
   log asynchronously, with reference, amount, method, utr and hasProof.

### 3.3 State machine and approval

```
PENDING --approve--> APPROVED --credit ok--> COMPLETED
PENDING --reject---> REJECTED
```

`TransferStatus` also declares `FAILED` (used by withdrawals).

- **Approve** (`POST /admin/deposits/{id}/approve`, `deposits:approve`,
  optional `note` up to 500 characters):
  1. `claimForReview` atomically moves `{_id, kind, status: PENDING}` to
     `APPROVED`. A second operator gets 409 `CONFLICT` "Transfer is already
     APPROVED|COMPLETED|…", and an unknown id gets 404 `NOT_FOUND`.
  2. `CreditWallet` on LIVE/INR posts `DEPOSIT`/`CREDIT` with key
     **`deposit:credit:<transferId>`**. The description is
     `Deposit <ref> (UTR <utr>)`, and the metadata is `{utr, approvedBy}`.
  3. The transfer becomes `COMPLETED` with `reviewedBy`, `reviewedAt`,
     `reviewNote` and `walletTransactionId`. The audit log gets
     `deposit.approved`, written synchronously.
- **Reject** (`POST /admin/transfers/{id}/reject`, required `note` of 3–500
  characters): `PENDING → REJECTED` atomically. Nothing to undo. Audit action
  `deposit.rejected`.

*Edge case:* if step 2 fails, for example because the wallet lock timed out,
the transfer **stays `APPROVED`**. Approve and reject both require `PENDING`,
so no API path retries it. An operator has to fix it in the database.

### 3.4 Proof uploads

`service/transfers/attachment.go`:

- **Type comes from magic bytes only.** The declared content type and the
  file extension are ignored. Recognised types:

  | Type | Signature |
  |---|---|
  | JPEG | `FF D8 FF` |
  | PNG | `89 50 4E 47` |
  | WebP | `RIFF....WEBP` |
  | PDF | `%PDF-` |

  A file under 12 bytes, or of any other type, fails 422 "Upload a JPG, PNG,
  WebP image or a PDF". An empty file fails 422.
- **Size:** at most `MAX_UPLOAD_BYTES` (default `5_242_880`, which is 5 MiB).
  Larger files fail 413.
- **Stored name:** `<uuid>.<ext-from-sniffed-type>`, written with `O_EXCL`
  under `UPLOAD_DIR` (default `./uploads`). The uploaded name is only
  displayed, after `SanitiseFilename`:
  - the basename is taken;
  - characters outside `[\w.\- ]` are removed;
  - the result is capped at 120 characters;
  - the fallback is `"attachment"`.
- **Path safety:** `ResolveAttachmentPath` refuses any path that escapes the
  upload root.
- **Serving:** `GET /transfers/{id}/proof` streams the file from disk. It is
  never a static mount.
  - Allowed readers: the owner, or a holder of `deposits:read` or
    `withdrawals:read`. Anyone else gets **404 "Transfer not found"**, not 403.
  - Headers: `Content-Type` from the stored mime type,
    `Content-Disposition: inline; filename="<originalName>"` and
    `Cache-Control: private, max-age=60`.

---

## 4. Manual INR withdrawals

### 4.1 Request: `POST /transfers/withdrawals`

The route needs an active user, with rate limit `wallet:transfer`. Body:
`amount` (integer paise, `1..1e12`), optional `customerNote` (up to 500
characters) and `destination`.

**Destination** is validated at the route, then again by
`ValidateDestination`. Only the fields that belong to the chosen type are
kept; the rest are stored as null.

| Type | Field | Route rule | Service rule |
|---|---|---|---|
| `UPI` | `upiId` | 3–80 characters | Trimmed, must match `^[a-zA-Z0-9.\-_]{2,64}@[a-zA-Z]{2,32}$` |
| `BANK` | `accountName` | 2–120 characters | At least 2 characters |
| | `accountNumber` | 6–20 characters | Must match `^[0-9]{6,20}$` |
| | `ifsc` | Exactly 11 characters | Upper-cased, must match `^[A-Z]{4}0[A-Z0-9]{6}$` |
| | `bankName` | Optional | |

Any other `type` fails with "Expected 'UPI' | 'BANK'".

`RequestWithdrawal` then:

1. Reads the LIVE wallet. It fails 404 `WALLET_NOT_FOUND` if there is none,
   and 400 `INSUFFICIENT_BALANCE` if `available < amount`. This check is
   advisory; the guarded lock in step 3 is the real one.
2. Inserts the transfer with these fields:
   - `reference = "WDR_…"`, `status = PENDING`;
   - `method` = `"UPI"` or `"Bank transfer"`;
   - `lockedAmount = amount`.
3. **Reserves the amount immediately**: `LockWithdrawalFunds`, posting
   `WITHDRAWAL`/`LOCK` with key **`withdrawal:lock:<transferId>`**. If the lock
   fails, for example because a concurrent spend won, the transfer is set to
   `FAILED` with `reviewNote: "Could not reserve funds"` and
   `lockedAmount: null`, and the error is returned.
   - The insert and the lock are **not** one transaction. A crash between them
     leaves a `PENDING` row with no lock. `locked_vs_open_positions` would then
     flag it.
4. The audit log gets `withdrawal.created` asynchronously, with reference,
   amount and destinationType.

### 4.2 Approval and rejection

```
PENDING --approve(utr)--> APPROVED --settle ok--> COMPLETED
PENDING --reject(note)--> REJECTED  (+ reservation released)
PENDING ...lock failed.... FAILED
```

- **Approve** (`POST /admin/withdrawals/{id}/approve`, `withdrawals:approve`):
  - **A payout UTR is required**: `utr` of 6–40 characters, normalised by
    `NormaliseUtr` (upper-cased). A missing UTR fails 422 "Record the UTR /
    reference of the payment you made before approving".
  - `claimForReview` moves the transfer `PENDING → APPROVED`. Then
    `SettleWithdrawal` runs: balance −a, locked −a, with key
    **`withdrawal:settle:<transferId>`**. The metadata is
    `{utr, approvedBy}`.
  - The transfer becomes `COMPLETED` with `utr`, `lockedAmount: null` and
    `walletTransactionId`. Audit action `withdrawal.approved`, written
    synchronously.
  - Payout UTRs are not unique-indexed: the partial index covers deposits only.
- **Reject** (`POST /admin/transfers/{id}/reject`, `note` of 3–500
  characters):
  - The transfer moves `PENDING → REJECTED`. When `lockedAmount ≠ 0`,
    `UnlockWithdrawalFunds` posts `WITHDRAWAL`/`UNLOCK` with key
    **`withdrawal:unlock:<transferId>`**, then `lockedAmount` is set to null.
  - Audit action `withdrawal.rejected` with `reason = note`.
- *Edge case, same as deposits:* if the settle fails after the claim, the
  withdrawal stays `APPROVED` with its funds still locked, and no API path
  recovers it.

---

## 5. USDT wallet via BeeX (service logic, currently unrouted)

The package overview is in `service/usdt/settings.go`. The money is the
customer's `FUNDING`/`USDT` wallet, and it moves only through
`service/wallets`.

The provider-side records are:

- `usdt_deposits` (one BeeX invoice per deposit);
- `usdt_withdrawals` (one payout per withdrawal);
- `usdt_transfers`;
- `beex_webhook_events`;
- `usdt_networks`.

### 5.1 Configuration and production guards

`config/env.go`:

| Variable | Default | Meaning |
|---|---|---|
| `USDT_ENABLED` | `false` | Master switch. When off, service calls fail **403 `USDT_DISABLED`**. |
| `BEEX_PROVIDER` | `beex` | `beex` or `mock` |
| `BEEX_API_BASE_URL` | `https://beexpayments.com/api/v1` | Must be http(s), https in production |
| `BEEX_API_KEY` | — | Must start `ck_live_` or `ck_test_`; the prefix sets `Mode()` |
| `BEEX_WEBHOOK_SECRET` | — | HMAC secret |
| `BEEX_ACCEPT_TEST_MODE` | `false` | Credit test-mode payments outside production |
| `BEEX_PAY_URL_BASE` | `https://beexpayments.com/pay/` | `paymentUrl = base + invoiceId` |
| `USDT_SUCCESS_URL` / `USDT_CANCEL_URL` | `CLIENT_URL + /wallet/usdt/success` / `/wallet/usdt` | `?deposit=<id>` is appended to the success URL |
| `USDT_DEPOSIT_CREDIT_BASIS` | `received` | `received` or `net` (after BeeX's fee) |
| `USDT_WITHDRAWAL_REVIEW_ABOVE` | `0` | micro-USDT. **0 means every withdrawal waits for an operator.** |
| `USDT_TRANSFER_TO_TRADING_ENABLED` | `false` | Gates transfer to trading |
| `USDT_USER_DAILY_WITHDRAWAL_LIMIT` | `0` | micro-USDT, rolling 24 h. 0 means use BeeX `dailyWithdrawalCap`. |

`validateUsdt` stops the server from booting in production when any of these
hold:

- `BEEX_PROVIDER=mock`. As a second line of defence, `beex.New` also refuses
  the mock in production.
- `BEEX_ACCEPT_TEST_MODE=true`.
- `USDT_ENABLED=true` together with any of:
  - a missing `BEEX_API_KEY`;
  - a key that is not `ck_live_`;
  - a `BEEX_WEBHOOK_SECRET` shorter than 16 characters;
  - a non-https `BEEX_API_BASE_URL`.

Credit-time mode gate (`ModeAccepted`):

- `live` is always accepted.
- `test` is accepted only when `NODE_ENV != production` **and**
  `BEEX_ACCEPT_TEST_MODE=true`.
- An empty or unknown mode is refused.

None of these variables appear in `.env.example` or `server-go/README.md`.

### 5.2 Networks

Seeds are in `models.DefaultUsdtNetworks`. Rows are only ever inserted with
`$setOnInsert`, so an operator's edits survive restarts.

| code | label | chain | minDeposit | Address rule (`service/usdt/chainaddr`) |
|---|---|---|---|---|
| `USDTTRC20` | TRC20 | TRON | `10_000_000` (10 USDT) | 34 characters, starts `T`, base58 decodes to 25 bytes: prefix `0x41`, then 20-byte id, then a 4-byte double-SHA256 checksum. The zero id is refused. |
| `USDTBSC` | BSC | BSC | `10_000_000` | `0x` + 40 hex digits. All-lower or all-upper is accepted. Mixed case must match EIP-55. The zero address is refused. |

- `depositEnabled` and `withdrawEnabled` can be toggled per network
  (`UpdateNetwork`). A paused network fails 400 `INVALID_NETWORK`.
- An unknown code fails 400 `INVALID_NETWORK`.
- A bad address fails 400 `INVALID_ADDRESS`.
- The client's `addressHint` (`client/src/features/usdt/model.ts`) is only a
  hint; the server's check is authoritative.

### 5.3 Idempotency per attempt

Every money call (deposit, withdraw, transfer) carries a client
`Idempotency-Key`. `createAttemptKey` in `client/src/features/usdt/model.ts`:

- keeps the same key for the same JSON body;
- generates a new key when the body changes;
- calls `reset()` after a success, so the next identical request counts as a
  new attempt.

On the server:

- `usdt_deposits`, `usdt_withdrawals` and `usdt_transfers` each have a unique
  index `{userId, idempotencyKey}`.
- Each record stores `requestHash = sha256(join("|", parts))`:
  - `deposit|network|amount`;
  - `withdrawal|network|address|amount`;
  - `transfer|amount`.
- Replaying a key with a different hash fails **422
  `IDEMPOTENCY_PAYLOAD_MISMATCH`**. Replaying with the same hash returns the
  existing record.
- When two concurrent inserts race, the duplicate-key loser reads back the
  winner.

### 5.4 Deposits (BeeX invoices)

`service/usdt/deposits.go`, `service/usdt/webhook.go`.

**Creation** (`CreateDeposit`):

1. The service checks that USDT is enabled, that the network exists with
   deposits enabled, that `amount > 0`, and that
   `amount ≥ network.minDeposit` (400 `INVALID_AMOUNT`).
2. It inserts a row with `status: created` and `orderId = "dep-<_id>"`.
   `orderId` is unique, and BeeX returns the same invoice for the same orderId.
3. `ensureInvoice` calls `CreateInvoice`. The amount is sent as an exact
   `json.Number` built by `micro.Format`.
4. On success, `adoptInvoice` stores `invoiceId` (or the BeeX reference as a
   fallback), `paymentUrl`, `reference`, `mode`, `payAddress` and `expiresAt`.
   If BeeX's `payAmount` differs from the request, **`amountExpected` is
   overwritten** to match the invoice.
5. On a **definite** BeeX refusal (a 4xx that is not a credentials or config
   problem): `status: failed` with a customer-safe `failureReason`.
6. On an **unknown** outcome: the row stays `created`. A replay with the same
   key calls BeeX again with the same orderId. If the invoice is still missing
   more than 1 h later and BeeX answers 404, `RefreshDeposit` marks the row
   `failed`.

**Statuses** (`models/usdt.go`) and the rank that prevents moving backwards:

| Status | Rank |
|---|---|
| `created` | 0 |
| `awaiting_payment` | 1 |
| `partially_paid` | 2 |
| `confirming` | 3 |
| `underpaid`, `expired`, `cancelled`, `failed` | 4 |
| `paid` | 5 |
| `overpaid` | 6 |

- Open statuses: `created`, `awaiting_payment`, `partially_paid`,
  `confirming`.
- **Only `paid` and `overpaid` can be credited** (`InvoiceCreditable`).

**`applyInvoiceUpdate`** is the single path for webhook, refresh, sweep,
admin, user and create updates:

- These updates are **ignored** (recorded as ignored, never retried):
  - an unknown status;
  - a `reference` mismatch;
  - a `mode` that differs from the stored mode;
  - a malformed amount.
- **Already credited:** money never changes again. The update only records
  `providerStatus`, a higher `amountReceived`, and an upgrade to a higher
  creditable status (`paid → overpaid`). When the basis is `received`,
  `received > amountCredited` logs "received beyond the credited amount needs
  manual review". Nothing is auto-credited.
- **Stale:** `received < recorded` is ignored. A lower rank is ignored unless
  more money arrived. Closed-unpaid statuses share rank 4, so a late payment
  can move one of them on to `paid`.
- **Creditable but missing `amountReceived`:** returns `errNeedAmounts`, and
  the webhook path then refreshes the full invoice from BeeX.
- **Mode not accepted:** `providerStatus` is recorded, there is no credit, and
  the event is ignored.

**Crediting** (`creditDeposit`):

```
basis = USDT_DEPOSIT_CREDIT_BASIS
amount = received                      if basis = received   (overpaid → full received is credited)
amount = net ?? (received − fee)       if basis = net         (neither reported → wait)
amount ≤ 0 → error, retried later
```

- The credit posts `CreditWallet` on FUNDING/USDT as `DEPOSIT`/`CREDIT`, with
  key **`usdt:deposit:<depositId>`**, `fee` and `network` on the row, and
  metadata `{orderId, invoiceReference, status, basis, amountReceived, source}`.
- Inside the same transaction, `credited: false → true` is a conditional
  update that sets `amountCredited`, `creditedAt`, `creditBasis` and
  `walletTransactionId`. If it matches nothing, the credit rolls back with
  "already credited".
- Precision: amounts with more than 6 decimals are floored, and the raw string
  is kept in `amountReceivedRaw`.

**Underpaid and partially paid deposits** are never auto-credited.
`AdminCreditDeposit` (operator, with a note):

- Allowed for `underpaid`, or for `partially_paid` once `expiresAt` has
  passed. Otherwise 409 `CONFLICT`.
- Credits `amountReceived`, or the net amount under the `net` basis, using the
  same key `usdt:deposit:<id>`. It records `manualCredit {by, note, amount, at}`.
- If the deposit was already credited: 409 `TRANSACTION_ALREADY_PROCESSED`.

**Refresh and polling:**

- `RefreshDeposit` calls `GetInvoice(orderId)`.
- `UserRefreshDeposit` lets a customer refresh **at most once per 30 s**. This
  is claimed through `lastUserRefreshAt`. Otherwise the stored record is
  returned.
- The client keeps polling while the deposit is open, or while it is
  `paid`/`overpaid` but not yet credited (`depositNeedsPolling`).
- `GetWallet` reports `pendingDeposits`: the sum of `amountReceived` on
  uncredited `partially_paid` or `confirming` deposits. It is not spendable.

### 5.5 Webhooks

`ReceiveWebhook`:

1. **Verify before parsing.** The header is
   `X-Signature: t=<unix>,v1=<hex>` and
   `v1 = hex(HMAC-SHA256(secret, t + "." + rawBody))`
   (`service/beex/signature.go`).
   - The comparison is constant-time, and any `v1` in the header may match
     (key rotation).
   - The timestamp tolerance is **±5 min**.
   - Errors: no secret configured gives 503 `SERVICE_UNAVAILABLE`; a bad,
     stale or future signature gives 401. The body cap is
     `MaxWebhookBody = 64 KiB`.
2. **Store under the event identity** (unique `eventKey`):
   - invoice events: `event|reference|status|amountReceived`;
   - payout events: `event|reference|status|txHash`;
   - otherwise `other|event|sha256(body)`, or `malformed|sha256(body)` (stored
     as failed).

   A redelivery increments `deliveries` and does nothing else.
3. **Process**, matching the event to a record:
   - invoice: `orderId`, falling back to `reference`;
   - payout: `clientReference` (our reference), falling back to a
     `providerReference` we already stored.

   Results:

   | Result | When |
   |---|---|
   | `processed` | Something changed |
   | `duplicate` | No change |
   | `ignored` | No matching record, or a mismatch |
   | `failed` | An error |

   BeeX still receives 200 once the event is stored.
4. **Retry** (`RetryWebhookEvents`, a leader loop every 30 s, batch 50):
   - The initial `nextAttemptAt` is `now + 2 min`, which keeps the loop off an
     event that is still being processed.
   - Backoff is `1min << attempts`, capped at 4 h.
   - Retrying stops after **10 attempts**.
   - When USDT is disabled, events stay `pending` and are retried in 10 min.
   - The operator's "process again" (`ReprocessEvent`) forces a rerun and is
     idempotent.

### 5.6 Withdrawal quote, limits and fees

`service/usdt/limits.go`, `service/usdt/withdrawals.go`.

**BeeX payout limits** (`GetPayoutLimits`):

| Item | Value |
|---|---|
| Fields | `minWithdrawal`, `maxWithdrawal`, `dailyWithdrawalCap`, `reviewedAbove`, `fee.fixed`, `fee.percent` |
| Fresh for | 10 min |
| Shared cache | Redis key `usdt:payout-limits`, kept 24 h |
| Fetch timeout | 10 s, one fetch per process at a time |
| BeeX unreachable | The stale copy is used and flagged `limitsStale: true`. With no copy at all: 503 `PROVIDER_UNAVAILABLE`. |

Formulas, with all amounts in micro-USDT:

```
fee   = feeFixed + floor(amount * feePercent / 100)        (estimate; BeeX's payout fee is authoritative)
net   = amount − fee
userDailyCap = USDT_USER_DAILY_WITHDRAWAL_LIMIT > 0 ? that : dailyWithdrawalCap
used(24h)    = Σ amount of withdrawals created in the last 24h with status ∉ {failed, rejected}
needsReview  = USDT_WITHDRAWAL_REVIEW_ABOVE ≤ 0  ||  amount > USDT_WITHDRAWAL_REVIEW_ABOVE
```

`GetQuote` returns these fields: `amount`, `feeAmount`, `netAmount`,
`feeIsEstimate: true`, `min`/`maxWithdrawal`, `dailyCap`, `dailyUsed`,
`reviewedAbove`, `needsReview`, `feePercent`, `feeFixed`, `availableBalance`,
`providerReviewedAbove` and `limitsStale`.

**Request checks**, in order (`RequestWithdrawal`). A limit of 0 means no
limit.

| Check | Error |
|---|---|
| USDT enabled | 403 `USDT_DISABLED` |
| Network exists and has withdrawals enabled | 400 `INVALID_NETWORK` |
| Address valid for the chain (trimmed) | 400 `INVALID_ADDRESS` |
| `amount > 0` | 400 `INVALID_AMOUNT` |
| Same key replayed | Existing record, or 422 on a hash mismatch |
| BeeX lists the currency | 400 `INVALID_NETWORK` |
| `amount ≥ minWithdrawal` | 400 `MIN_WITHDRAWAL_NOT_MET` |
| `amount ≤ maxWithdrawal` | 400 `MAX_WITHDRAWAL_EXCEEDED` |
| `fee < amount` | 400 `INVALID_AMOUNT` |
| Under the per-user Redis lock `lock:usdt:withdraw:<uid>` (TTL 20 s, wait 10 s; timeout gives 503): `used + amount ≤ userDailyCap` | 400 `DAILY_CAP_EXCEEDED` |
| Under the same lock: platform-wide `used + amount ≤ dailyWithdrawalCap` | 400 `DAILY_CAP_EXCEEDED` |
| Available balance | 400 `INSUFFICIENT_BALANCE` (from the guarded lock) |

**Lock and record commit together.**

- `LockWithdrawalFunds` on FUNDING/USDT posts `WITHDRAWAL`/`LOCK` with key
  **`usdt:withdrawal:lock:<id>`**, and the `usdt_withdrawals` insert happens
  inside `InTx`.
- `reference = "withdrawal-<id>"` (unique). This is our payout reference, and
  it is resent on every attempt.
- The new record starts in one of two states:
  - `pending_review`;
  - `requested` with `submissionPending: true`, followed by an immediate
    `SubmitPayout`.

### 5.7 Withdrawal state machine

```
pending_review --operator approve--> requested --> [awaiting_approval] --> sending --> completed  (settle)
pending_review --operator reject---> rejected                                       (release)
requested/awaiting_approval/sending --BeeX--> failed | rejected                    (release)
requested (never acknowledged, BeeX confirms no payout) --operator reject--> rejected (release)
```

- Open statuses, which hold the gross locked: `pending_review`, `requested`,
  `awaiting_approval`, `sending`.
- Final statuses: `completed`, `failed`, `rejected`. Nothing changes a final
  status.
- **Submission** (`SubmitPayout`):
  - Redis lock `lock:usdt:payout:<id>`, TTL 90 s.
  - The next attempt is scheduled *before* calling BeeX. Backoff is 30 s,
    1 m, 2 m, 5 m, 10 m, then 30 m.
  - Outcomes:

    | BeeX result | Action |
    |---|---|
    | Answered (a repeat comes back as `created:false`) | Adopt the payout |
    | Definite refusal on attempt > 1 | Check `GetPayout(reference)` first, in case an earlier unknown attempt succeeded |
    | Definite refusal, no payout exists | `failed`, funds released |
    | Unknown outcome or config error | Funds stay locked; `RetryPendingSubmissions` (every 30 s, batch 20) resends with the same reference |

- **`applyPayoutUpdate`** ignores an update when any of these mismatch:
  - `clientReference`;
  - `providerReference`;
  - **amount**;
  - address (EVM addresses compare case-insensitively);
  - currency.

  It also ignores updates for a withdrawal still in `pending_review`. When
  submission hits a mismatch, resubmission stops and an operator has to look.
  - **completed**: `SettleWithdrawal` posts `DEBIT` of the **gross** (balance
    −gross, locked −gross) with key **`usdt:withdrawal:settle:<id>`**. It
    records the final `fee`, `net` and `txHash`, and the status flips in the
    same transaction.
  - **failed or rejected**: `UnlockWithdrawalFunds` posts the **full gross**
    back with key **`usdt:withdrawal:unlock:<id>`**.
  - **BeeX reports `completed` for a withdrawal we already released**: this is
    logged as `CONFLICT` and ignored. Money left without a debit, so an
    operator must act.
  - Intermediate statuses only move forward. A reported fee sets
    `feeIsEstimate: false`.
- **Operator actions:**
  - `ApproveWithdrawal` takes `pending_review → requested` and then submits.
  - `RejectWithdrawal` works as shown in the diagram above.
  - Otherwise the result is 409 `WITHDRAWAL_ALREADY_PROCESSED`.
  - Approving while USDT is disabled gives 409 `USDT_DISABLED`.

### 5.8 Background work and sweep

`service/usdt/worker.go`. These loops are leader-elected, but **nothing calls
`usdt.Start` today** (see the top of this page).

| Loop | Interval | Work |
|---|---|---|
| `usdt-webhook-retry` | 30 s | `RetryWebhookEvents(50)` |
| `usdt-payout-submit` | 30 s | `RetryPendingSubmissions(20)` |
| `usdt-reconciliation` | 10 min | `Sweep` |

`Sweep` refreshes at most 50 records per run, within a 5 min budget:

- uncredited deposits in open statuses that are older than 30 min, with no
  webhook in the last 30 min and no refresh in the last 10 min;
- withdrawals in `requested`, `awaiting_approval` or `sending` with
  `submissionPending: false` and `updatedAt` older than 30 min.

### 5.9 Transfer to trading

`service/usdt/transfer.go`:

- **Gated** twice: `USDT_ENABLED` (otherwise 403 `USDT_DISABLED`), and
  `USDT_TRANSFER_TO_TRADING_ENABLED` (otherwise **409
  `USDT_TRANSFER_DISABLED`**).
- Moves `amount` from **FUNDING/USDT to LIVE/USDT**. There is no conversion to
  INR, and the INR trading engine never sees this wallet.
- `TransferBetweenAccounts` writes two `TRANSFER` rows (`DEBIT` on FUNDING,
  `CREDIT` on LIVE) in one transaction. Both rows have `referenceType:
  UsdtTransfer`. The keys are `usdt:transfer:<uid>:<sha256(clientKey)[:32]>`
  with `:out` and `:in`.
- The `usdt_transfers` row is inserted in `InTx`. Events: `usdt.updated`
  `{kind, id, status}` and `wallet.updated`.

### 5.10 Error codes the client shows verbatim

`KNOWN_ERROR_CODES` in `client/src/features/usdt/model.ts`:

`INVALID_NETWORK`, `INVALID_ADDRESS`, `INVALID_AMOUNT`, `INSUFFICIENT_BALANCE`,
`MIN_WITHDRAWAL_NOT_MET`, `MAX_WITHDRAWAL_EXCEEDED`, `DAILY_CAP_EXCEEDED`,
`USDT_DISABLED`, `USDT_TRANSFER_DISABLED`, `PROVIDER_ERROR`,
`PROVIDER_UNAVAILABLE`, `RATE_LIMITED`, `IDEMPOTENCY_PAYLOAD_MISMATCH`.

Any other code is shown as a generic message.

`beex.ToAppError` maps BeeX error codes to these:

| BeeX code(s) | API error |
|---|---|
| `currency_not_enabled` | `INVALID_NETWORK` 400 |
| `below_network_minimum`, `amount_below_minimum`, `fiat_pricing_not_supported` | `INVALID_AMOUNT` 400 |
| `below_minimum` | `MIN_WITHDRAWAL_NOT_MET` 400 |
| `above_maximum` | `MAX_WITHDRAWAL_EXCEEDED` 400 |
| `invalid_address` | `INVALID_ADDRESS` 400 |
| `daily_cap_exceeded` | `DAILY_CAP_EXCEEDED` 400 |
| `rate_limited` | `RATE_LIMITED` 429 |
| Credential codes, `insufficient_balance`, temporary errors | `PROVIDER_UNAVAILABLE` 503 |
| Anything else | `PROVIDER_ERROR` 502 |

Raw provider text is stored only for admins (`providerError`).

---

## 6. Reconciliation and audit

### 6.1 Reconciliation

`service/reconciliation/{reconciliation,usdt,worker}.go`. A leader loop runs
every `RECONCILIATION_INTERVAL_MS` (default `300_000`, minimum `10_000`).

- Each run writes a `reconciliation_runs` document with status `OK`, `ALERT`
  or `FAILED`. A check that errors aborts the run, which becomes `FAILED`.
- On `ALERT` the run writes an audit row `reconciliation.alert` (actor
  `SYSTEM`, `success: false`) and logs `RECONCILIATION_ALERT`.
- **Nothing here corrects a balance.**

| Check | Rule |
|---|---|
| `wallet_ledger_vs_balance` | For every INR wallet: `Σ(balanceAfter − balanceBefore) == balance` **and** `Σ(lockedAfter − lockedBefore) == lockedBalance` |
| `locked_vs_open_positions` | For every non-FUNDING INR wallet with `locked > 0`: `locked == Σ stake` of its account's `OPEN` + `SETTLING` trades, plus (LIVE only) `Σ amount` of `PENDING` manual withdrawals. Expected locks with no wallet lock are flagged too. |
| `settled_trades_vs_settlements` | Count of WON, LOST, DRAW and CLOSED_EARLY trades == count of settlements |
| `settlements_vs_wallet_transactions` | Count of settlements == count of `TRADE_WIN`, `TRADE_LOSS`, `TRADE_REFUND` and `TRADE_EARLY_CLOSE` rows |
| `platform_result_consistency` | `Σ platformResult` of non-DEMO trades settled in the window (24 h) == the same sum over settlements |
| `hedge_order_integrity` | Filled hedge orders have a fill price, a venue id and a notional > 0 |
| `usdt_wallet_ledger_vs_balance` | The ledger-vs-balance check for every USDT wallet (FUNDING and LIVE/USDT) |
| `usdt_locked_vs_open_withdrawals` | FUNDING `locked == Σ amount` of open USDT withdrawals |
| `usdt_paid_deposits_credited` | No `paid` or `overpaid` deposit has `credited ≠ true` (reports up to 20 ids) |
| `usdt_credited_deposits_vs_ledger` | Each credited deposit has exactly one `DEPOSIT` row whose amount is `amountCredited`, and no uncredited deposit has a row |
| `usdt_completed_withdrawals_settled` | Each `completed` withdrawal has its `usdt:withdrawal:settle:<id>` row for the gross amount |

INR and micro-USDT figures are never summed together.

`locked_vs_open_positions` counts only `PENDING` withdrawals. The stuck
`APPROVED` state from section 4.2 therefore shows up as a mismatch.

Admin routes: `GET /admin/reconciliation` and `POST /admin/reconciliation/run`
need `reconciliation:read`. `POST /admin/reconciliation/{id}/resolve` needs
`reconciliation:resolve`; it records a resolution note and audits
`reconciliation.resolved`.

### 6.2 Audit log

`service/audit/audit.go`, `models/audit_log.go`. The collection is
`audit_logs`.

- **Fields:** `actorId`, `actorRole`, `actorEmail`, `action`, `resource`,
  `resourceId`, `before`, `after`, `ip`, `userAgent`, `requestId`, `success`,
  `reason`, `createdAt`.
- **Append-only by convention.** The application only ever calls `InsertOne`;
  there is no update or delete code. There is **no database-level
  enforcement**: no validator and no role restriction.
- **Write modes:** `Write` is synchronous and used for admin money and config
  actions. `Log` is fire-and-forget, with a 10 s timeout. A failed write logs
  `AUDIT WRITE FAILED` at error level.

Money-related actions that are actually written:

| Action | When | Mode | Payload |
|---|---|---|---|
| `deposit.created` | Customer submits a claim | async | reference, amount, method, utr, hasProof |
| `withdrawal.created` | Customer requests a withdrawal | async | reference, amount, destinationType |
| `deposit.approved` / `withdrawal.approved` | Operator approves | sync | The full transfer view |
| `deposit.rejected` / `withdrawal.rejected` | Operator rejects | sync | Transfer view, `reason = note` |
| `wallet.adjusted` | Admin manual credit or debit | sync, before the response | reference, direction, accountType, amount, reason, balanceAfter |
| `wallet.demo_reset` | Customer resets DEMO | async | New balance |
| `payment_method.updated` | Admin upsert or toggle | sync | before / after |
| `reconciliation.alert` / `reconciliation.resolved` | Reconciliation | async | Alerts / note |

`wallet.credit`, `wallet.debit`, `wallet.lock` and `wallet.unlock` are declared
but never written. Per-movement history lives in the ledger. No USDT action is
audited, because no USDT route exists.

**Manual adjustment** (`POST /admin/users/{id}/wallet/adjust`,
`wallets:adjust`, `api/admin/users.go`):

- Body: `amount` (integer paise, `1..1e12`), `direction` (`CREDIT` or
  `DEBIT`), `reason` (3–500 characters), optional `accountType` (default
  LIVE).
- Posts `ADJUSTMENT_CREDIT` or `ADJUSTMENT_DEBIT` with reference
  `AdminAdjustment/ADJ_…` and key `adjust:ADJ_…`.
- That key is random per request, so **a retried HTTP request creates a second
  adjustment**.

---

## 7. Permissions on money actions

`config/permissions.go`. `HasPermission` grants a permission when any of these
holds:

- the role is `SUPER_ADMIN`;
- the role's own set contains it;
- the user's explicit extra permissions contain it.

`httpx.RequirePermission(a, b)` requires **all** of the listed permissions.

| Action | Route | Permission |
|---|---|---|
| List wallets with LIVE INR totals | `GET /admin/wallets` | `wallets:read` |
| A user's ledger | `GET /admin/users/{id}/transactions` | `wallets:read` |
| Manual credit or debit (LIVE or DEMO) | `POST /admin/users/{id}/wallet/adjust` | `wallets:adjust` |
| List deposits | `GET /admin/deposits` | `deposits:read` |
| Approve a deposit (credits) | `POST /admin/deposits/{id}/approve` | `deposits:approve` |
| List withdrawals | `GET /admin/withdrawals` | `withdrawals:read` |
| Approve a withdrawal (settles) | `POST /admin/withdrawals/{id}/approve` | `withdrawals:approve` |
| Reject a deposit **or** a withdrawal | `POST /admin/transfers/{id}/reject` | **both** `deposits:approve` **and** `withdrawals:approve` |
| View a payment proof | `GET /transfers/{id}/proof` | Owner, or `deposits:read`, or `withdrawals:read` |
| List payment methods (admin) | `GET /admin/payment-methods` | `deposits:read` |
| Change payment methods | `PUT /admin/payment-methods`, `POST …/{key}/active` | `settings:write` |
| View or run reconciliation | `GET /admin/reconciliation`, `POST …/run` | `reconciliation:read` |
| Resolve a reconciliation run | `POST /admin/reconciliation/{id}/resolve` | `reconciliation:resolve` |
| Read the audit log | `GET /admin/audit-logs` | `audit:read` |
| Customer deposit, withdrawal, DEMO reset | `/transfers/*`, `/wallet/demo/reset` | Authenticated. Transfer writes also need `RequireActiveUser`. |

Default role grants:

- `FINANCE` holds every money permission above except `settings:write`,
  `reconciliation:resolve` and `audit:read`.
- `SUPPORT` holds only `wallets:read`, `deposits:read` and `withdrawals:read`.
- `ADMIN` holds the read permissions, `audit:read` and `settings:read`. It has
  no approve, adjust or `settings:write`.
- Only `SUPER_ADMIN` holds `settings:write` and `reconciliation:resolve` by
  default.

USDT operator actions (approve or reject a withdrawal, manual credit, webhook
reprocess, network patch) have **no route and no permission** defined yet.

---

## Where to look

| Topic | File(s) |
|---|---|
| Money helpers (INR) | `server-go/internal/platform/util/util.go`, `client/src/utils/format.ts`, `admin/src/utils/format.ts` |
| Money helpers (USDT) | `server-go/internal/platform/micro/micro.go`, `client/src/utils/usdt.ts`, `admin/src/utils/usdt.ts` |
| Wallet and ledger schemas | `server-go/internal/models/wallet.go`, `models/wallet_transaction.go`, `models/domain.go`, `models/migrate.go` |
| Guarded mutations, lock model, keys | `server-go/internal/service/wallets/wallet.go` |
| DEMO account | `service/wallets/demo.go`, `api/wallet/routes.go` |
| Account-to-account transfer | `service/wallets/transfer.go` |
| Orphaned stake locks | `service/wallets/orphan_locks.go`, `workers/workers.go` |
| Transactions and the probe | `platform/mongox/mongo.go`, `config/env.go` (`ENABLE_MONGO_TRANSACTIONS`) |
| Redis locks | `platform/redisx/lock.go`, `config/constants.go` |
| Manual deposits and withdrawals | `service/transfers/transfer.go`, `models/transfer.go`, `api/transfers/routes.go`, `api/transfers/multipart.go` |
| Proof storage | `service/transfers/attachment.go` |
| Payment methods | `service/transfers/payment_method.go`, `models/payment_method.go`, `api/admin/finance.go` |
| Admin finance and adjustments | `api/admin/finance.go`, `api/admin/users.go`, `service/wallets/admin.go` |
| USDT config and guards | `config/env.go` (`validateUsdt`), `service/usdt/settings.go`, `service/beex/provider.go` |
| USDT deposits and webhooks | `service/usdt/deposits.go`, `service/usdt/webhook.go`, `service/usdt/apply.go`, `service/beex/signature.go` |
| USDT withdrawals and limits | `service/usdt/withdrawals.go`, `service/usdt/limits.go`, `service/usdt/chainaddr/chainaddr.go` |
| USDT transfer and workers | `service/usdt/transfer.go`, `service/usdt/worker.go` |
| USDT models | `models/usdt.go` |
| BeeX client, errors, mock | `service/beex/{client,errors,types,mock}.go` |
| Client USDT rules | `client/src/features/usdt/model.ts`, `client/src/services/api.ts` |
| Reconciliation | `service/reconciliation/{reconciliation,usdt,worker}.go` |
| Audit | `service/audit/audit.go`, `models/audit_log.go`, `api/admin/audit.go` |
| RBAC | `config/permissions.go`, `platform/httpx/middleware.go` |
| Error codes | `platform/apperr/errors.go` |

---

## Code vs README disagreements

1. **USDT is unreachable.** The memory notes and the client UI describe a
   working USDT wallet. In `server-go`, no route, worker or seed calls into
   `service/usdt` or `service/beex`. The root README says nothing about USDT
   at all. The `/wallet/usdt*` calls in `client/src/services/api.ts` have no
   server routes.
2. **"The only places that convert."** The root README names
   `internal/platform/util` and `utils/format.ts` as the only conversion
   points. Three things contradict this:
   - USDT has its own converters: `platform/micro`, `client/src/utils/usdt.ts`
     and `admin/src/utils/usdt.ts`.
   - Code outside those files converts by hand:
     - `client/src/features/trade/ticketModel.ts` (`amountText`: `minor / 100`);
     - `client/src/pages/Trades.tsx` (CSV export: `/ 100`);
     - `admin/src/pages/Dashboard.tsx` (`Math.round(value * 100)`).
   - The server's `util.ToMinor` and `util.FromMinor` have no callers.
3. **"Mutation blocked at the schema level" for `audit_logs`** (root README).
   The Go backend has no schema or database guard. Append-only holds only
   because the code never updates or deletes.
4. **"An operator with `deposits:approve` … approves"** (root README). That is
   true for approval. Rejection, though, needs **both** `deposits:approve` and
   `withdrawals:approve`, whatever the transfer's kind. The default `ADMIN`
   role cannot approve or reject at all.
5. **Test table.** The root README cites Node tests (`wallet.test.ts`,
   `transfers.test.ts`, …), including the "25 concurrent locks" and
   "two operators approving" concurrency cases. The Go tests
   (`service/wallets/*_test.go`, `service/transfers/transfer_test.go`) are
   unit tests of mutation construction, guards, UTR, destination and file
   sniffing. The concurrency claims have no Go test behind them. There are no
   tests for `service/usdt`, `service/beex` or `service/reconciliation`.
6. **Environment docs.** None of the `USDT_*` and `BEEX_*` variables are in
   `.env.example`, `server-go/README.md` or the root README's environment
   table.
7. **Client ledger labels.** `LEDGER_LABELS` in
   `client/src/features/usdt/model.ts` maps `WITHDRAWAL_LOCK`,
   `WITHDRAWAL_RELEASE`, `WITHDRAWAL_REFUND`, `TRANSFER_TO_TRADING` and
   `TRANSFER_OUT`. The server never writes those types. It writes `WITHDRAWAL`
   with movement `LOCK`, `UNLOCK` or `DEBIT`, and `TRANSFER`. Labels fall back
   to `humanise(type)`, so `TRANSFER` displays as "Transfer".
8. **"No payment provider"** (root README, "What is deliberately not built").
   This is accurate for what is reachable, but the BeeX integration code
   exists in the tree.
