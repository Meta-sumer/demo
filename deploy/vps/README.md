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
it builds the client and admin, publishes them and the download page, updates
the nginx snippet, rebuilds the API and waits until it is healthy. If the web
build fails, the live site is left as it was. MongoDB, Redis, `.env`, uploads
and the APK are never touched.

Never edit code on the server: the pull is fast-forward only and stops if the
server copy differs from GitHub.

## What lives where

| File in this folder | Installed as |
|---|---|
| `pull-and-deploy.sh` | `/opt/wintratrade/deploy.sh` (untracked, so a pull never rewrites the running script) |
| `deploy.sh` | run from the checkout by `./deploy.sh` |
| `nginx/wintratrade-api.conf` | `/etc/nginx/snippets/wintratrade-api.conf`, copied on every deploy |
| `nginx/wintratrade.conf` | `/etc/nginx/conf.d/wintratrade.conf`, first install only; certbot edits it afterwards |
| `download/index.html` | `/var/www/wintratrade/downloads/index.html`, copied on every deploy |
| `bootstrap.sh`, `first-setup.sh` | fresh server only |

Not in git, by design:

- `/opt/wintratrade/.env` - production secrets, generated on the server.
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

## When something goes wrong

- API logs: `docker logs bt-server --tail 100`
- All containers: `docker ps`
- Roll back: `git checkout <older-commit> && bash deploy/vps/deploy.sh`, then
  `git checkout main` when the fix is pushed.
