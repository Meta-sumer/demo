# Deploying wintratrade.online (Hostinger VPS)

Production runs on one VPS: the Go API, MongoDB and Redis in Docker, and
nginx serving the trading site, the admin console, the API (`/api`, `/ws`) and
the Android app download page, all over HTTPS.

| What | Where |
|---|---|
| Trading site | https://wintratrade.online |
| Admin console | https://admin.wintratrade.online/admin/login |
| App download | https://wintratrade.online/download/ |
| Server | `201.18.214.48`, Ubuntu, app in `/opt/wintratrade` (a checkout of this repo) |

## Updating (every change)

1. On your PC, commit and push to `main`.
2. Open a terminal on the server (hPanel → VPS → Browser terminal, or
   `ssh -i ~/.ssh/wintratrade-deploy root@201.18.214.48`).
3. Run:

   ```bash
   cd /opt/wintratrade
   ./deploy.sh
   ```

`./deploy.sh` pulls `main` and runs [`deploy.sh`](deploy.sh) from this folder:
it adds any missing datastore password to `.env` (generated once, never
printed), builds the client and admin, publishes them and the download page,
updates the nginx snippets, installs the backup and monitor crons and the log
rotation, builds the API, turns MongoDB access control on if it is still off
([`mongo-auth.sh`](mongo-auth.sh)), recreates Redis if its configuration
changed, restarts the API and waits until it is healthy. If the web build
fails, the live site is left as it was. The database, uploads and the APK are
never touched; `.env` only gains missing keys.

The first deploy of the 22 Oct 2026 hardening has one-time steps (security
headers include, alert email settings):
`docs/audit/operations/production-readiness.md` §13.

Never edit code on the server: the pull is fast-forward only and stops if the
server copy differs from GitHub.

## What lives where

| File in this folder | Installed as |
|---|---|
| `pull-and-deploy.sh` | `/opt/wintratrade/deploy.sh` (untracked, so a pull never rewrites the running script) |
| `deploy.sh` | run from the checkout by `./deploy.sh` |
| `nginx/wintratrade-api.conf` | `/etc/nginx/snippets/wintratrade-api.conf`, copied on every deploy |
| `nginx/wintratrade-security-headers.conf` | `/etc/nginx/snippets/wintratrade-security-headers.conf`, copied on every deploy; included by the page locations |
| `nginx/wintratrade-logformat.conf` | `/etc/nginx/conf.d/wintratrade-logformat.conf`, copied on every deploy (the `/ws` log without its token) |
| `nginx-enable-headers.sh` | run by hand once on a server installed before the headers: adds the include to the certbot-managed site file (backup, `nginx -t`, rollback) |
| `nginx/wintratrade.conf` | `/etc/nginx/conf.d/wintratrade.conf`, first install only; certbot edits it afterwards |
| `download/index.html` | `/var/www/wintratrade/downloads/index.html`, copied on every deploy |
| `backup-mongo.sh` + `cron/wintratrade-backup` | `/etc/cron.d/wintratrade-backup`: nightly 02:30 dump of MongoDB to `/var/backups/wintratrade` (14 days kept), each archive verified by a scratch restore; emails on failure |
| `monitor.sh` + `cron/wintratrade-monitor` | `/etc/cron.d/wintratrade-monitor`: every minute, emails when `/ready` fails twice in a row (and when it recovers), when the disk passes 85 % or the certificate has under 14 days |
| `logrotate/wintratrade` | `/etc/logrotate.d/wintratrade` (the backup and monitor logs) |
| `mongo-auth.sh` | run by `deploy.sh` and `first-setup.sh`: MongoDB users and access control, idempotent |
| `lib.sh` | helpers for the scripts and the runbooks: `.env` reading (never sourced), `send_email`, `mongo_js`, `mongo_tools` |
| `bootstrap.sh`, `first-setup.sh` | fresh server only |

Not in git, by design:

- `/opt/wintratrade/.env` - production secrets, generated on the server
  (JWT secrets, MFA key, MongoDB and Redis passwords, SMTP settings).
- `/opt/wintratrade/CREDENTIALS` - the seeded accounts' passwords (root-only).
- `/var/www/wintratrade/downloads/binary-trading.apk` - a build output larger
  than GitHub's 100 MB file limit.

## Releasing a new Android app

1. Raise `version` in `mobile/app.json`, commit, push.
2. On your PC: `cd mobile && npx eas-cli build -p android --profile preview`
   (the `preview` profile builds an APK pointed at wintratrade.online and
   raises the version code itself).
3. On the server, put the APK from the link the build prints in place:

   ```bash
   cd /var/www/wintratrade/downloads
   curl -fsSL -o binary-trading.apk.tmp "<apk link from expo.dev>"
   mv binary-trading.apk.tmp binary-trading.apk
   ```

4. Update the version and size line in `download/index.html`, commit, push and
   run `./deploy.sh`.

## Fresh server

```bash
# as root on a new Ubuntu VPS
git clone https://github.com/Meta-sumer/demo.git /opt/wintratrade
bash /opt/wintratrade/deploy/vps/bootstrap.sh     # Docker, nginx, certbot, firewall, swap
bash /opt/wintratrade/deploy/vps/first-setup.sh   # .env, database, seed, nginx, first deploy
```

Point the `@`, `www` and `admin` A records at the server, then:

```bash
certbot --nginx --non-interactive --agree-tos --register-unsafely-without-email --redirect \
  -d wintratrade.online -d www.wintratrade.online -d admin.wintratrade.online
```

Keep the folder name `/opt/wintratrade`: Docker names the data volumes after
it (`wintratrade_mongo-data` and so on), so a different folder starts with an
empty database.

## Backups

`backup-mongo.sh` runs every night at 02:30 (log: `/var/log/wintratrade-backup.log`).
It writes `/var/backups/wintratrade/binary_trading-<UTC time>.archive.gz`,
restores it into a scratch database to check that every collection came back,
drops the scratch copy, and keeps 14 days. Run it by hand at any time with
`bash /opt/wintratrade/deploy/vps/backup-mongo.sh`. The restore steps are at
the top of the script. These archives are on the same server; copy them off
the machine (or enable Hostinger's VPS backups) to survive losing the VPS.

## When something goes wrong

- API logs: `docker logs bt-server --tail 100`
- All containers: `docker ps`
- Alerts: admin console, Governance > Alerts; host watchdog log
  `/var/log/wintratrade-monitor.log`
- MongoDB and Redis need credentials: use the `MONGO` / `REDIS` helpers of
  `docs/audit/operations/production-readiness.md` §6
- Roll back: `git checkout <older-commit> && bash deploy/vps/deploy.sh`, then
  `git checkout main` when the fix is pushed.
