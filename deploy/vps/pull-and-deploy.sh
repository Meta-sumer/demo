#!/usr/bin/env bash
# The server's entry point, installed as /opt/wintratrade/deploy.sh (untracked):
#
#   cp deploy/vps/pull-and-deploy.sh /opt/wintratrade/deploy.sh
#
# It pulls main, then hands over to deploy/vps/deploy.sh from the code it just
# pulled. Kept tiny and outside git on the server, so a pull never rewrites the
# script that is running.
set -euo pipefail
cd /opt/wintratrade

before=$(git rev-parse --short HEAD)
git pull --ff-only origin main
after=$(git rev-parse --short HEAD)
echo "== pulled $before -> $after"
git log --oneline "$before..$after" | head -20 || true

exec bash deploy/vps/deploy.sh
