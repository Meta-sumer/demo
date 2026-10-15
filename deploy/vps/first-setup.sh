#!/usr/bin/env bash
# First install on a fresh server, from a git checkout in /opt/wintratrade.
# Run bootstrap.sh before this. Later updates use ./deploy.sh instead.
#
#   - writes .env with fresh secrets (only when it does not exist)
#   - starts MongoDB, Redis and the API
#   - seeds the database and replaces every seeded password with a random one,
#     written to /opt/wintratrade/CREDENTIALS (root-only)
#   - installs the nginx site and runs the normal deploy (web, download page)
#
# Then point DNS at the server and run certbot (see README.md).
set -euo pipefail
cd /opt/wintratrade

DOMAIN=${DOMAIN:-wintratrade.online}
COMPOSE="docker compose -f docker-compose.yml -f docker-compose.prod.yml --profile full"
rand() { openssl rand -base64 48 | tr -dc 'A-Za-z0-9' | head -c "$1"; }
pw() { echo "Wt-$(rand 16)!7"; }

if [ ! -f .env ]; then
  echo "== writing .env"
  cat > .env <<EOF
NODE_ENV=production
PORT=4000
HOST=0.0.0.0
LOG_LEVEL=info
MONGO_PORT=27017
ENABLE_MONGO_TRANSACTIONS=true
JWT_SECRET=$(rand 64)
JWT_REFRESH_SECRET=$(rand 64)
JWT_ACCESS_TTL=15m
JWT_REFRESH_TTL=30d
BCRYPT_ROUNDS=12
# DEMO ONLY: the simulated feed is a random walk, not a real price.
MARKET_DATA_PROVIDER=simulated
ALLOW_SIMULATED_FEED_IN_PRODUCTION=true
HEDGE_PROVIDER=paper
HEDGE_LIVE_EXECUTION_ENABLED=false
WORKERS_ENABLED=true
CLIENT_URL=https://${DOMAIN}
ADMIN_URL=https://admin.${DOMAIN}
UPLOAD_DIR=/app/uploads
MAX_UPLOAD_BYTES=5242880
SEED_SUPER_ADMIN_EMAIL=admin@binary.local
EOF
  chmod 600 .env
fi

echo "== start the stack"
$COMPOSE up -d --build
for i in $(seq 1 90); do curl -fsS http://127.0.0.1:4000/ready >/dev/null 2>&1 && break; sleep 2; done

if [ ! -f .seeded ]; then
  echo "== seed"
  ADMIN_PW=$(pw)
  $COMPOSE run --rm -e SEED_SUPER_ADMIN_PASSWORD="$ADMIN_PW" --entrypoint /app/seed server
  # The seed creates three more accounts whose passwords are published in the
  # README. Each is changed to a random password straight away.
  API=http://127.0.0.1:4000
  {
    echo "Admin console: https://admin.${DOMAIN}/admin/login"
    echo "admin@binary.local  ${ADMIN_PW}"
  } > CREDENTIALS
  for pair in "risk@binary.local:Risk@12345" "finance@binary.local:Finance@12345" "trader@binary.local:Trader@12345"; do
    email=${pair%%:*}; old=${pair#*:}; new=$(pw)
    token=$(curl -fsS -X POST "$API/auth/login" -H 'content-type: application/json' \
      -d "{\"email\":\"$email\",\"password\":\"$old\"}" | jq -r '.data.tokens.accessToken')
    curl -fsS -X POST "$API/auth/change-password" -H 'content-type: application/json' \
      -H "authorization: Bearer $token" \
      -d "{\"currentPassword\":\"$old\",\"newPassword\":\"$new\"}" >/dev/null
    echo "$email  $new" >> CREDENTIALS
  done
  chmod 600 CREDENTIALS
  touch .seeded
fi

echo "== nginx site"
mkdir -p /var/www/wintratrade/client /var/www/wintratrade/admin /var/www/wintratrade/downloads
cp deploy/vps/nginx/wintratrade-api.conf /etc/nginx/snippets/wintratrade-api.conf
if [ ! -f /etc/nginx/conf.d/wintratrade.conf ]; then
  cp deploy/vps/nginx/wintratrade.conf /etc/nginx/conf.d/wintratrade.conf
fi

echo "== server entry point"
cp deploy/vps/pull-and-deploy.sh ./deploy.sh && chmod +x ./deploy.sh
grep -qx 'deploy.sh' .git/info/exclude 2>/dev/null || printf '%s\n' deploy.sh CREDENTIALS .seeded >> .git/info/exclude

exec bash deploy/vps/deploy.sh
