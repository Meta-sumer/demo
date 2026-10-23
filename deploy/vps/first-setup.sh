#!/usr/bin/env bash
# First install on a fresh server, from a git checkout in /opt/wintratrade.
# Run bootstrap.sh before this. Later updates use ./deploy.sh instead.
# Safe to run again: nothing that exists is replaced.
#
#   - writes .env with fresh secrets (only when it does not exist): JWT
#     secrets, MFA_ENCRYPTION_KEY, the Redis and MongoDB passwords; admin
#     two-factor required; USDT, live hedging and alert email OFF
#   - starts MongoDB with access control (users root + the API's user),
#     Redis with a password, and the API
#   - seeds the database once, with random passwords chosen here and written
#     only to /opt/wintratrade/CREDENTIALS (root-only, chmod 600)
#   - installs the nginx site and runs the normal deploy (web, download page,
#     crons)
#
# No secret is printed. Then point DNS at the server and run certbot (see
# README.md), and turn on alert email in .env (ALERT_EMAIL_* / SMTP_*).
set -euo pipefail
cd /opt/wintratrade
umask 077

. deploy/vps/lib.sh

DOMAIN=${DOMAIN:-wintratrade.online}
COMPOSE="docker compose -f docker-compose.yml -f docker-compose.prod.yml --profile full"
rand() { openssl rand -base64 48 | tr -dc 'A-Za-z0-9' | head -c "$1"; }
# Meets the account password rule: lower case, upper case and a digit.
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
# Admin roles sign in with an authenticator code. The key encrypts the
# stored authenticator secrets: never change it once admins have enrolled.
ADMIN_2FA_REQUIRED=true
MFA_ENCRYPTION_KEY=$(openssl rand -hex 32)
# Datastore credentials (docker-compose.prod.yml builds the URLs from them).
REDIS_PASSWORD=$(gen_hex32)
MONGO_ROOT_PASSWORD=$(gen_hex32)
MONGO_APP_USERNAME=wintratrade
MONGO_APP_PASSWORD=$(gen_hex32)
# Real prices: the public Binance spot feed (@bookTicker, no key needed) for
# BTC/USD and ETH/USD. The simulated random walk is refused in production.
MARKET_DATA_PROVIDER=binance
ALLOW_SIMULATED_FEED_IN_PRODUCTION=false
# Launch scope: EUR/USD, GBP/USD, XAU/USD are switched off at boot
# (idempotent, audited; docs/audit/operations/feed-rules.md section 9).
APPLY_LAUNCH_SCOPE=true
# Hedging: paper venue, no real orders.
HEDGE_PROVIDER=paper
HEDGE_LIVE_EXECUTION_ENABLED=false
WORKERS_ENABLED=true
CLIENT_URL=https://${DOMAIN}
ADMIN_URL=https://admin.${DOMAIN}
UPLOAD_DIR=/app/uploads
MAX_UPLOAD_BYTES=5242880
SEED_SUPER_ADMIN_EMAIL=admin@binary.local
# USDT wallet (BeeX): off until live keys are added (docs/audit/usdt/usdt-flows.md).
USDT_ENABLED=false
BEEX_PROVIDER=beex
ALLOW_BEEX_SANDBOX_IN_PRODUCTION=false
USDT_TRANSFER_TO_TRADING_ENABLED=false
# Alert email (API and host scripts): fill in, then set true and ./deploy.sh.
# Port 465 = implicit TLS; 587 = STARTTLS (required).
ALERT_EMAIL_ENABLED=false
SMTP_HOST=
SMTP_PORT=587
SMTP_USERNAME=
SMTP_PASSWORD=
ALERT_EMAIL_FROM=
ALERT_EMAIL_TO=
ALERT_MIN_SEVERITY=warning
EOF
  chmod 600 .env
else
  echo "== .env exists - only missing keys are added"
  # A re-run, or a server set up before these keys existed. MFA_ENCRYPTION_KEY
  # is deliberately not added here: on a server whose admins already enrolled
  # with the key derived from JWT_SECRET, a new key would lock them out.
  env_ensure REDIS_PASSWORD gen_hex32
  env_ensure MONGO_ROOT_PASSWORD gen_hex32
  env_ensure MONGO_APP_PASSWORD gen_hex32
  env_ensure ADMIN_2FA_REQUIRED echo true
  env_ensure ALERT_EMAIL_ENABLED echo false
  env_ensure USDT_ENABLED echo false
  env_ensure HEDGE_LIVE_EXECUTION_ENABLED echo false
  env_has MFA_ENCRYPTION_KEY || echo "   note: MFA_ENCRYPTION_KEY is not set; set it only before any admin enrols (production-readiness.md section 7)"
  chmod 600 .env
fi

echo "== build the API image"
$COMPOSE build server

echo "== start the datastores"
bash deploy/vps/mongo-auth.sh
$COMPOSE up -d redis

echo "== start the API"
$COMPOSE up -d server
for i in $(seq 1 90); do curl -fsS http://127.0.0.1:4000/ready >/dev/null 2>&1 && break; sleep 2; done

if [ ! -f .seeded ]; then
  echo "== seed"
  # The passwords are chosen here, kept in CREDENTIALS.pending until the seed
  # succeeds (a re-run after a failure reuses them, because the seed never
  # changes the password of an account that already exists), and handed to
  # the seed through the environment only.
  if [ ! -f CREDENTIALS.pending ]; then
    {
      echo "SEED_SUPER_ADMIN_PASSWORD=$(pw)"
      echo "SEED_RISK_PASSWORD=$(pw)"
      echo "SEED_FINANCE_PASSWORD=$(pw)"
      echo "SEED_TRADER_PASSWORD=$(pw)"
    } > CREDENTIALS.pending
    chmod 600 CREDENTIALS.pending
  fi
  val() { grep "^$1=" CREDENTIALS.pending | head -n 1 | cut -d= -f2-; }
  SEED_SUPER_ADMIN_PASSWORD=$(val SEED_SUPER_ADMIN_PASSWORD) SEED_RISK_PASSWORD=$(val SEED_RISK_PASSWORD) \
    SEED_FINANCE_PASSWORD=$(val SEED_FINANCE_PASSWORD) SEED_TRADER_PASSWORD=$(val SEED_TRADER_PASSWORD) \
    $COMPOSE run --rm -e SEED_SUPER_ADMIN_PASSWORD -e SEED_RISK_PASSWORD -e SEED_FINANCE_PASSWORD -e SEED_TRADER_PASSWORD \
      --entrypoint /app/seed server > /tmp/wintratrade-seed.log 2>&1 ||
    { tail -20 /tmp/wintratrade-seed.log; echo "seed FAILED - run first-setup.sh again"; exit 1; }
  admin_email=$(env_get SEED_SUPER_ADMIN_EMAIL admin@binary.local)
  {
    echo "# wintratrade.online seeded accounts - root only. Move them to a password"
    echo "# manager, change them in the console, then delete this file."
    echo "Admin console: https://admin.${DOMAIN}/admin/login"
    echo "Admin roles set up two-factor (authenticator app) at their first sign-in."
    echo
    printf '%-24s %-14s %s\n' "$admin_email" SUPER_ADMIN "$(val SEED_SUPER_ADMIN_PASSWORD)"
    printf '%-24s %-14s %s\n' risk@binary.local RISK_MANAGER "$(val SEED_RISK_PASSWORD)"
    printf '%-24s %-14s %s\n' finance@binary.local FINANCE "$(val SEED_FINANCE_PASSWORD)"
    printf '%-24s %-14s %s\n' trader@binary.local "USER (demo)" "$(val SEED_TRADER_PASSWORD)"
  } > CREDENTIALS
  chmod 600 CREDENTIALS
  rm -f CREDENTIALS.pending /tmp/wintratrade-seed.log
  touch .seeded
  echo "   accounts created; passwords are in /opt/wintratrade/CREDENTIALS (root only)"
fi

echo "== nginx site"
mkdir -p /var/www/wintratrade/client /var/www/wintratrade/admin /var/www/wintratrade/downloads
chmod 755 /var/www/wintratrade /var/www/wintratrade/client /var/www/wintratrade/admin /var/www/wintratrade/downloads
cp deploy/vps/nginx/wintratrade-api.conf /etc/nginx/snippets/wintratrade-api.conf
cp deploy/vps/nginx/wintratrade-security-headers.conf /etc/nginx/snippets/wintratrade-security-headers.conf
cp deploy/vps/nginx/wintratrade-logformat.conf /etc/nginx/conf.d/wintratrade-logformat.conf
if [ ! -f /etc/nginx/conf.d/wintratrade.conf ]; then
  cp deploy/vps/nginx/wintratrade.conf /etc/nginx/conf.d/wintratrade.conf
fi
chmod 644 /etc/nginx/snippets/wintratrade-*.conf /etc/nginx/conf.d/wintratrade*.conf

echo "== server entry point"
cp deploy/vps/pull-and-deploy.sh ./deploy.sh && chmod 700 ./deploy.sh
for f in deploy.sh CREDENTIALS CREDENTIALS.pending .seeded; do
  grep -qx "$f" .git/info/exclude 2>/dev/null || printf '%s\n' "$f" >> .git/info/exclude
done

# Web files must stay readable by nginx.
umask 022
exec bash deploy/vps/deploy.sh
