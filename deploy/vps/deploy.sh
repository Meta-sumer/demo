#!/usr/bin/env bash
# Redeploys wintratrade.online from the code already pulled into /opt/wintratrade.
#
# Run it through the server's ./deploy.sh, which pulls first and then runs this
# file (see pull-and-deploy.sh). Pulling here would change this script while
# bash is still reading it.
#
#   1. makes sure .env has the datastore passwords (generated once, never
#      printed): REDIS_PASSWORD, MONGO_ROOT_PASSWORD, MONGO_APP_PASSWORD
#   2. builds the client and admin inside a Node container (no Node on the host)
#   3. publishes both builds to /var/www/wintratrade
#   4. publishes the app download page (the APK itself is not in git)
#   5. updates the shared nginx snippets and reloads nginx; installs the
#      backup and monitor crons and the log rotation
#   6. builds the API image, turns on MongoDB access control if it is still
#      off (mongo-auth.sh, idempotent), recreates Redis if its password is
#      new, then restarts the API with the credentials
#   7. waits until the API answers /ready
#
# The database, uploads and the APK are never touched; .env only gains
# missing keys.
set -euo pipefail
cd /opt/wintratrade
# Published web files must be readable by nginx; .env is chmod 600 itself.
umask 022

. deploy/vps/lib.sh

COMPOSE="docker compose -f docker-compose.yml -f docker-compose.prod.yml --profile full"
NODE_IMAGE=node:22-alpine
WEB=/var/www/wintratrade

echo "== secrets"
# Existing installs from before datastore auth get their passwords here,
# once; later deploys find them and change nothing.
env_ensure REDIS_PASSWORD gen_hex32
env_ensure MONGO_ROOT_PASSWORD gen_hex32
env_ensure MONGO_APP_PASSWORD gen_hex32
chmod 600 .env

echo "== build client + admin"
docker run --rm -v "$PWD":/app -w /app "$NODE_IMAGE" sh -c \
  "npm ci --no-audit --no-fund --loglevel=error && npm run build --workspace client && npm run build --workspace admin" \
  > /tmp/wintratrade-web-build.log 2>&1 || { tail -30 /tmp/wintratrade-web-build.log; echo "web build FAILED - site not changed"; exit 1; }

echo "== publish web"
rsync -a --delete client/dist/ "$WEB/client/"
rsync -a --delete admin/dist/ "$WEB/admin/"

echo "== publish download page"
mkdir -p "$WEB/downloads"
cp deploy/vps/download/index.html "$WEB/downloads/index.html"
cp mobile/assets/icon.png "$WEB/downloads/icon.png"

echo "== nginx"
# Only the shared snippets. The site file is managed by certbot after the
# first install, so it is never overwritten from here. The security headers
# reach the sites through wintratrade-api.conf, which every server block
# already includes (see the comment in that file).
cp deploy/vps/nginx/wintratrade-api.conf /etc/nginx/snippets/wintratrade-api.conf
cp deploy/vps/nginx/wintratrade-security-headers.conf /etc/nginx/snippets/wintratrade-security-headers.conf
cp deploy/vps/nginx/wintratrade-logformat.conf /etc/nginx/conf.d/wintratrade-logformat.conf
nginx -t -q && systemctl reload nginx

echo "== cron + log rotation"
# Nightly dump + restore check of MongoDB (see backup-mongo.sh), and the
# host watchdog that emails when the API is down (see monitor.sh).
install -m 644 deploy/vps/cron/wintratrade-backup /etc/cron.d/wintratrade-backup
install -m 644 deploy/vps/cron/wintratrade-monitor /etc/cron.d/wintratrade-monitor
install -m 644 deploy/vps/logrotate/wintratrade /etc/logrotate.d/wintratrade

echo "== build API image"
$COMPOSE build server

echo "== datastores"
bash deploy/vps/mongo-auth.sh
# Recreated only when its configuration changed (the first deploy with a
# password); the data is in the redis-data volume (AOF).
$COMPOSE up -d redis

echo "== restart API"
$COMPOSE up -d server

echo "== health"
for i in $(seq 1 60); do
  if curl -fsS http://127.0.0.1:4000/ready >/dev/null 2>&1; then
    curl -fsS http://127.0.0.1:4000/ready | (command -v jq >/dev/null && jq -c '.data.checks | {mongo: .mongodb.ok, redis: .redis.ok, feed: .marketFeed.state, book: .exposureBook.status, limiter: .rateLimiter.mode}' || cat)
    echo "== deployed $(git rev-parse --short HEAD)"
    exit 0
  fi
  sleep 2
done
echo "API did not become ready - check: docker logs bt-server --tail 100"
exit 1
