#!/usr/bin/env bash
# Redeploys wintratrade.online from the code already pulled into /opt/wintratrade.
#
# Run it through the server's ./deploy.sh, which pulls first and then runs this
# file (see pull-and-deploy.sh). Pulling here would change this script while
# bash is still reading it.
#
#   1. builds the client and admin inside a Node container (no Node on the host)
#   2. publishes both builds to /var/www/wintratrade
#   3. publishes the app download page (the APK itself is not in git)
#   4. updates the shared nginx snippet and reloads nginx
#   5. rebuilds and restarts the API container; MongoDB and Redis keep running
#   6. waits until the API answers /ready
#
# .env, CREDENTIALS, the database, uploads and the APK are never touched.
set -euo pipefail
cd /opt/wintratrade

COMPOSE="docker compose -f docker-compose.yml -f docker-compose.prod.yml --profile full"
NODE_IMAGE=node:22-alpine
WEB=/var/www/wintratrade

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
# Only the shared snippet. The site file is managed by certbot after the first
# install, so it is never overwritten from here.
cp deploy/vps/nginx/wintratrade-api.conf /etc/nginx/snippets/wintratrade-api.conf
nginx -t -q && systemctl reload nginx

echo "== rebuild + restart API"
$COMPOSE up -d --build server

echo "== health"
for i in $(seq 1 60); do
  if curl -fsS http://127.0.0.1:4000/ready >/dev/null 2>&1; then
    curl -fsS http://127.0.0.1:4000/ready | (command -v jq >/dev/null && jq -c '.data.checks | {mongo: .mongodb.ok, redis: .redis.ok, feed: .marketFeed.state}' || cat)
    echo "== deployed $(git rev-parse --short HEAD)"
    exit 0
  fi
  sleep 2
done
echo "API did not become ready - check: docker logs bt-server --tail 100"
exit 1
