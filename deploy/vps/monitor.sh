#!/usr/bin/env bash
# Host watchdog for wintratrade.online, run every minute by cron
# (/etc/cron.d/wintratrade-monitor, installed by deploy.sh).
#
# The API emails its own alerts (service/alerts), but it cannot do that while
# it is down, while MongoDB is down, or while Redis is down (no leader can be
# elected). This script covers those cases from outside the API:
#
#   - GET http://127.0.0.1:4000/ready; after FAILS_BEFORE_ALERT (2)
#     consecutive failures it sends one "API NOT READY" email, and one
#     "RECOVERED" email when /ready answers again;
#   - disk usage of / and of the backup folder at DISK_ALERT_PERCENT (85) or
#     more, at most once a day;
#   - the TLS certificate expiring within CERT_ALERT_DAYS (14), at most once
#     a day.
#
# State lives in /var/lib/wintratrade-monitor. Only changes are logged.
set -uo pipefail

. "$(dirname "$0")/lib.sh"

READY_URL=${READY_URL:-http://127.0.0.1:4000/ready}
STATE=${MONITOR_STATE_DIR:-/var/lib/wintratrade-monitor}
FAILS_BEFORE_ALERT=${FAILS_BEFORE_ALERT:-2}
DISK_ALERT_PERCENT=${DISK_ALERT_PERCENT:-85}
CERT_ALERT_DAYS=${CERT_ALERT_DAYS:-14}
CERT=${CERT_FILE:-/etc/letsencrypt/live/wintratrade.online/fullchain.pem}
HOST=$(hostname)
NOW=$(date -u +%Y-%m-%dT%H:%M:%SZ)
TODAY=$(date -u +%Y-%m-%d)

mkdir -p "$STATE"
chmod 700 "$STATE"
# One run at a time.
exec 9>"$STATE/lock"
flock -n 9 || exit 0

log() { echo "$NOW $*"; }

# ── API readiness ───────────────────────────────────────────────────────
body=$(curl -sS --max-time 10 -w '\nHTTP %{http_code}' "$READY_URL" 2>&1)
code=$(printf '%s' "$body" | tail -n 1 | sed -n 's/^HTTP //p')
fails=$(cat "$STATE/fails" 2>/dev/null || echo 0)
case $fails in ''|*[!0-9]*) fails=0 ;; esac

if [ "$code" = 200 ]; then
  if [ -f "$STATE/alerted" ]; then
    since=$(cat "$STATE/alerted")
    if send_email "RECOVERED: API ready again on $HOST" "The API answers /ready again at $NOW UTC.
It was not ready since $since UTC (first failed check)."; then
      rm -f "$STATE/alerted"
      log "recovered (down since $since); recovery email sent"
    fi
  elif [ "$fails" -gt 0 ]; then
    log "ready again after $fails failed check(s)"
  fi
  echo 0 > "$STATE/fails"
else
  fails=$((fails + 1))
  echo "$fails" > "$STATE/fails"
  [ "$fails" -eq 1 ] && echo "$NOW" > "$STATE/first-failure"
  log "/ready failed ($fails): $(printf '%s' "$body" | tail -n 1)"
  if [ "$fails" -ge "$FAILS_BEFORE_ALERT" ] && [ ! -f "$STATE/alerted" ]; then
    first=$(cat "$STATE/first-failure" 2>/dev/null || echo "$NOW")
    containers=$(docker ps -a --filter name=bt- --format '{{.Names}}  {{.Status}}' 2>&1 | head -n 10)
    if send_email "API NOT READY on $HOST" "GET $READY_URL has failed $fails times in a row (first failure $first UTC).
Customers cannot trade or sign in while this lasts.

Last answer:
$(printf '%s' "$body" | head -c 2000)

Containers:
$containers

Disk: $(df -h / | tail -n 1)
Memory: $(free -m 2>/dev/null | awk '/Mem:/ {print $3 " MB used of " $2 " MB"}')

Runbook: docs/audit/operations/production-readiness.md section 6.1
  docker logs bt-server --tail 100
  cd /opt/wintratrade && docker compose -f docker-compose.yml -f docker-compose.prod.yml --profile full up -d"; then
      echo "$first" > "$STATE/alerted"
      log "alert email sent"
    fi
  fi
fi

# ── once-a-day checks ───────────────────────────────────────────────────
once_a_day() { # once_a_day NAME -> true if NAME has not fired today
  [ "$(cat "$STATE/$1.day" 2>/dev/null)" != "$TODAY" ]
}
mark_today() { echo "$TODAY" > "$STATE/$1.day"; }

for mount in / /var/backups; do
  [ -d "$mount" ] || continue
  use=$(df -P "$mount" 2>/dev/null | awk 'NR==2 {gsub("%", "", $5); print $5}')
  case $use in ''|*[!0-9]*) continue ;; esac
  name="disk$(printf '%s' "$mount" | tr '/' '_')"
  if [ "$use" -ge "$DISK_ALERT_PERCENT" ] && once_a_day "$name"; then
    if send_email "DISK ${use}% full on $HOST ($mount)" "$(df -h "$mount" | tail -n 1)

Largest users to check: docker system df; du -sh /var/backups/wintratrade /var/lib/docker/containers
Container logs rotate (docker-compose.prod.yml logging); old images: docker image prune."; then
      mark_today "$name"; log "disk alert sent for $mount ($use%)"
    fi
  fi
done

if [ -r "$CERT" ] && command -v openssl >/dev/null 2>&1; then
  if ! openssl x509 -checkend $((CERT_ALERT_DAYS * 86400)) -noout -in "$CERT" >/dev/null 2>&1 && once_a_day cert; then
    end=$(openssl x509 -enddate -noout -in "$CERT" | cut -d= -f2)
    if send_email "TLS certificate expires soon on $HOST" "The certificate $CERT expires on $end (less than $CERT_ALERT_DAYS days).
Check: certbot certificates; certbot renew --dry-run; systemctl list-timers | grep certbot
Runbook: docs/audit/operations/production-readiness.md section 6.9."; then
      mark_today cert; log "certificate alert sent ($end)"
    fi
  fi
fi
exit 0
