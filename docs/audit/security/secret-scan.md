# Secret scan report (F-19)

**Finding:** F-19 - Security, session and admin controls need production
hardening. This document is the secret-scan report the finding asks for.

**When and what.** The scan ran on 17 Oct 2026 over the repository at branch
`main` (HEAD `493abbd`). It covered:

- all **750 git-tracked files**, including the contents of the one tracked
  archive, `server-go.zip`;
- the **full git history**: 15 commits over all refs, about 5.7 MB of patch
  text; lockfiles were excluded because they hold only package integrity
  hashes.

**Result: no real secret was found** in the tracked files, in the archive or
in the history. Every hit falls into one of four kinds:

- a **placeholder**;
- a **test fixture**;
- a **former published default password**, intentionally known and now
  refused by the seed;
- a **false positive**.

The values themselves are not reproduced here, except for placeholders that
are already in `.env.example`.

---

## 1. Commands

Run from the repository root (Git Bash). `$OUT` is any scratch folder
**outside** the repository.

```bash
# Inventory
git ls-files | wc -l
git rev-list --all | wc -l

# 1. Tracked files that are typically secret
git ls-files | grep -iE '(^|/)\.env($|\.)|\.pem$|\.key$|\.p12$|\.pfx$|\.jks$|\.keystore$|id_rsa|credentials|\.npmrc$|\.netrc$|google-services\.json|serviceAccount'
git ls-files | grep -iE '\.(zip|tar|gz|tgz|7z|rar|apk|aab|jar)$'

# 2. Key and token patterns in tracked files (lockfiles excluded)
EXCL=(-- . ':!package-lock.json' ':!**/package-lock.json')
git grep -nIE -e '-----BEGIN ([A-Z]+ )?PRIVATE KEY-----'                        "${EXCL[@]}"
git grep -nIE -e '(AKIA|ASIA)[0-9A-Z]{16}'                                      "${EXCL[@]}"   # AWS
git grep -nIE -e '(sk|rk|pk)_(live|test)_[0-9A-Za-z]{10,}'                      "${EXCL[@]}"   # Stripe
git grep -nIE -e '(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{20,}' "${EXCL[@]}"   # GitHub
git grep -nIE -e 'xox[baprs]-[A-Za-z0-9-]{10,}|AIza[0-9A-Za-z_-]{35}|ck_(live|test)_[A-Za-z0-9]{8,}|eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}' "${EXCL[@]}"   # Slack, Google, BeeX, JWT
git grep -nIE -e '(mongodb(\+srv)?|redis|rediss|postgres(ql)?|mysql|amqp)://[^/[:space:]:@]+:[^@[:space:]]+@' "${EXCL[@]}"   # URIs with credentials

# 3. Literal password / secret assignments
git grep -nIiE -e '(password|passwd|pwd|secret|api[_-]?key|apikey|access[_-]?token|auth[_-]?token|private[_-]?key|client[_-]?secret)["'"'"']?[[:space:]]*[:=][[:space:]]*["'"'"'][^"'"'"'[:space:]]{6,}["'"'"']' "${EXCL[@]}"
git grep -nIE -e '^[[:space:]]*(export[[:space:]]+)?[A-Z0-9_]*(PASSWORD|SECRET|API_KEY|TOKEN|PRIVATE_KEY)[A-Z0-9_]*=[^[:space:]$]{4,}' -- .
git grep -nIE 'JWTSecret:|JWTRefreshSecret:|MFAEncryptionKey:|WebhookSecret:|BeexAPIKey:' -- '*.go'

# 4. Long quoted literals with digits (high-entropy review), import paths filtered out
git grep -nIE "[\"'\`][A-Za-z0-9+/=_.-]{32,}[\"'\`]" -- . ':!**/package-lock.json' ':!package-lock.json' ':!**/go.sum' ':!*.svg' ':!docs/**' \
  | grep -E "[\"'\`][^\"'\` ]*[0-9][^\"'\` ]*[\"'\`]" | grep -vE 'go.mongodb.org|github.com|golang.org|/admin/|binarytrading/'

# 5. The tracked archive: extract into a scratch folder, then repeat 2-3 on it
mkdir -p "$OUT/zipscan" && unzip -q server-go.zip -d "$OUT/zipscan"
grep -rnIE -e '<each pattern from step 2>' "$OUT/zipscan"
grep -aoE '(AKIA|ASIA)[0-9A-Z]{16}|-----BEGIN [A-Z ]*PRIVATE KEY-----|(ghp|gho|ghs)_[A-Za-z0-9]{30,}|sk_live_[0-9A-Za-z]{10,}' "$OUT/zipscan/server-go/bin/server.exe"
grep -ciE 'password|secret|bearer|eyJ|mongodb://|redis://|apikey|api_key' "$OUT/zipscan/server-go/bin/server.log"

# 6. History: every added or removed line in every commit (lockfiles excluded)
git log -p --all --no-color -- . ':!package-lock.json' ':!**/package-lock.json' > "$OUT/history.patch"
grep -nE '^[+-]' "$OUT/history.patch" | grep -E '<combined pattern from step 2>' | head
grep -nE '^-[[:space:]]*(export[[:space:]]+)?[A-Z0-9_]*(PASSWORD|SECRET|API_KEY|TOKEN)[A-Z0-9_]*=[^[:space:]$]{4,}' "$OUT/history.patch"
git log --all --diff-filter=A --name-only --format='' | grep -iE '(^|/)\.env($|\.)|\.pem$|\.key$|credentials|id_rsa|\.p12$|keystore' | sort -u
git log --all -S '<a former default password>' --oneline    # where it was added and removed
```

---

## 2. Findings

### 2.1 Tracked files

| # | File : line | Type | Classification |
|---|---|---|---|
| 1 | `.env.example:23`, `:24` | `JWT_SECRET` and `JWT_REFRESH_SECRET` set to `change-me-…` values | **Placeholder.** Production refuses any JWT secret starting with `change-me` (`server-go/internal/config/env.go`, `config/security.go`) |
| 2 | `.env.example`, `mobile/.env.example` (other keys) | Empty values (`MFA_ENCRYPTION_KEY=`, `SEED_*_PASSWORD=`, `HEDGE_API_*=`, `MARKET_DATA_API_*=`) | **Placeholder** |
| 3 | `.npmrc`, `mobile/.npmrc` | npm settings (`audit`, `fund`, `workspaces`) | **Clean**: no registry token |
| 4 | `client/tests/auth-schemas.test.ts:33`, `mobile/tests/auth-schemas.test.ts:33` | Password literal in a validation test | **Test fixture** |
| 5 | `server-go/internal/api/auth/validation_test.go:25` | Password inside a JSON request body | **Test fixture** |
| 6 | `server-go/internal/service/auth/mfa_flow_test.go:22`, `:279` | Test account passwords | **Test fixture** |
| 7 | `server-go/internal/service/auth/auth_test.go:12`, `:24` | A test password and its bcrypt hash at cost 4 (compatibility test) | **Test fixture** |
| 8 | `server-go/internal/api/admin/security_test.go:43`, `api/binary/summary_route_test.go:41`, `api/marketdata/routes_test.go:70`, `service/auth/mfa_flow_test.go:29`, `service/users/revocation_test.go:33` | JWT secrets built as a repeated letter (`strings.Repeat`) | **Test fixture** |
| 9 | `server-go/internal/service/auth/totp_test.go:28`, `:29`, `:88` | The published RFC 4226 / RFC 6238 test-vector seeds | **Test fixture** (public standard vectors) |
| 10 | `client/tests/usdt-model.test.ts:25-26,62`, `mobile/tests/usdt-model.test.ts:25-26,62` | TRON and BSC addresses used to test address validation | **Test fixture** (public addresses, not keys) |
| 11 | `server-go/internal/service/wallets/adjust_test.go:33`, `:133` | UUID used as a test idempotency key | **Test fixture** |
| 12 | `mobile/app.json:61` | Expo `projectId` | **False positive**: a public project identifier, not a credential |
| 13 | `server-go/internal/service/beex/errors.go:15-17` | Provider error code names (`missing_api_key`, …) | **False positive** |
| 14 | `server-go/internal/service/transfers/transfer_test.go:134` | Path `../../etc/passwd` in a filename-sanitising test | **False positive** |
| 15 | `server-go/cmd/seed/main.go:257` | List of formerly published passwords | **Intentional deny-list**: the seed refuses these values (`checkPasswordPolicy`) |
| 16 | `deploy/vps/first-setup.sh:64` | The three former default passwords of the seeded `risk@`, `finance@` and `trader@` accounts | **Former published default.** The script used them only to log in once and replace each with a random password at install, recorded in the root-only `CREDENTIALS` file. See note A |
| 17 | `mobile/README.md:31` | The former default demo trader password | **Former published default.** The document is out of date (the seed no longer sets it). See note A |
| 18 | `deploy/vps/first-setup.sh:29-30` | `JWT_SECRET=$(rand 64)` | **Clean**: generated on the server at install; no literal |
| 19 | `deploy/vps/README.md:18` | Name of an SSH key file on the operator's PC | **Not a secret**: the key itself is not in the repository |
| 20 | `server-go.zip` (tracked, 11 MB, added in commit `106443c`) | An archive of an old `server-go` snapshot: source, a compiled `bin/server.exe`, a 1,327-line local development log `bin/server.log`, and 28 tiny `uploads/*.png` files | **No secret.** The pattern scans of every file in it (and a raw scan of the binary) found nothing. The log has no credentials, tokens or e-mail addresses, and only `0.0.0.0` and `127.0.0.1` as IP addresses. See note B |

No `.env`, key, keystore or credentials file is tracked. No `.env` file was
ever added in any commit (step 6).

### 2.2 Git history

| # | Where | Type | Classification |
|---|---|---|---|
| H1 | Added or changed in commits `f422d99`, `106443c`, `6d05532` and `5eed03a`; removed from the README, `.env.example`, the seed and `config/env.go` in `493abbd` | The former default passwords of the seeded accounts, in the root README, the old `.env.example` (`SEED_SUPER_ADMIN_PASSWORD`), the seed source and a default in `config/env.go` | **Former published default.** Deny-listed by the seed since `493abbd`. Still present in the working tree in `deploy/vps/first-setup.sh` and `mobile/README.md` (rows 16-17), and readable in the history of a public repository. See note A |
| H2 | All commits | Private keys; AWS, Stripe, GitHub, Slack or Google tokens; BeeX `ck_live_` / `ck_test_` keys; JWTs; database URIs with credentials | **None found** |
| H3 | All commits | Added files named like `.env`, `*.pem`, `*.key`, credentials or keystores | Only `.env.example` and `mobile/.env.example` (**placeholders**) |

---

## 3. Notes and actions

**A - Former default passwords.** These passwords were public by design (the
old README listed them for local development). Since `493abbd`:

- the seed takes passwords from `SEED_*_PASSWORD` or generates random ones,
  and refuses these values;
- the README no longer lists them.

On the production VPS, `first-setup.sh` gave the super admin a random password
and replaced the other three at install. **What the owner should do:**

1. Confirm on production that none of the four former defaults signs in to any
   account (a one-off check by the owner; this scan did not touch production).
2. Remove the demo password from `mobile/README.md`.
3. ~~Update `deploy/vps/first-setup.sh`.~~ Done on 22 Oct 2026: the script
   no longer signs in with old default passwords; it chooses random seed
   passwords, hands them to the seed through the environment and writes them
   only to the root-only `CREDENTIALS` file (see
   `docs/audit/operations/production-readiness.md` §7).

**B - `server-go.zip`.** The archive holds no secret. It was a stale copy of
the backend, with a compiled Windows binary and a development log, in a
public repository. It was deleted from the working tree on 22 Oct 2026 (the
deletion goes in with that change) and `.gitignore` lists it. It stays in the
git history; rewriting history is optional and not required for secrecy.

**C - Keep it clean.**

- Real secrets live only in the VPS `.env` (`chmod 600`) and in `CREDENTIALS`
  (root-only); see `production-readiness.md` §7.
- Re-run this scan before every release. A pre-commit hook or a CI step that
  runs the patterns in step 2 would catch a mistake before it is pushed
  (there is no CI yet; F-20).
