#!/usr/bin/env bash
# Nightly MongoDB backup for wintratrade.online.
#
# Dumps the binary_trading database from the bt-mongo container as a gzipped
# archive into /var/backups/wintratrade, keeps the last KEEP_DAYS days, and
# verifies every new archive by restoring it into a scratch database inside
# the same mongod, comparing document counts, and dropping the scratch copy.
# Authenticates as the MongoDB root user when .env has MONGO_ROOT_PASSWORD
# (the password never appears on a command line; see lib.sh).
#
# A failure at any step exits non-zero and emails the operators (SMTP settings
# in .env, see .env.example); the cron log keeps the full output.
#
# Installed by deploy.sh as /etc/cron.d/wintratrade-backup (02:30 server time).
# Run by hand: bash /opt/wintratrade/deploy/vps/backup-mongo.sh
#
# Restore (replaces the live data - stop the API first): see
# docs/audit/operations/production-readiness.md section 6.8.
set -euo pipefail

. "$(dirname "$0")/lib.sh"

DB=binary_trading
DIR=${BACKUP_DIR:-/var/backups/wintratrade}
KEEP_DAYS=${KEEP_DAYS:-14}
MONGO_CONTAINER=${MONGO_CONTAINER:-bt-mongo}
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
FILE="$DIR/$DB-$STAMP.archive.gz"
SCRATCH="restore_check_$STAMP"
STEP="start"

on_exit() {
  local rc=$?
  rm -f "$FILE.tmp"
  [ $rc -eq 0 ] && return 0
  echo "BACKUP FAILED at step '$STEP' (exit $rc)"
  local host
  host=$(hostname)
  send_email "BACKUP FAILED on $host" "The nightly MongoDB backup failed at step: $STEP (exit code $rc)
Time: $(date -u +%Y-%m-%dT%H:%M:%SZ) UTC
Archive: $FILE

Disk:
$(df -h "$DIR" 2>/dev/null | tail -n 1)

Last lines of /var/log/wintratrade-backup.log:
$(tail -n 25 /var/log/wintratrade-backup.log 2>/dev/null || echo '(not readable)')

Run it by hand to see the error: bash $WT_APP_DIR/deploy/vps/backup-mongo.sh
Runbook: docs/audit/operations/production-readiness.md section 3." || true
  exit $rc
}
trap on_exit EXIT

mkdir -p "$DIR"
chmod 700 "$DIR"

STEP="dump"
echo "== dump $DB -> $FILE"
mongo_tools "$MONGO_CONTAINER" mongodump --quiet --db "$DB" --archive --gzip > "$FILE.tmp"
mv "$FILE.tmp" "$FILE"
chmod 600 "$FILE"

STEP="restore check"
echo "== verify by restoring into $SCRATCH"
mongo_tools "$MONGO_CONTAINER" mongorestore --quiet --archive --gzip \
  --nsFrom "$DB.*" --nsTo "$SCRATCH.*" < "$FILE"

counts() {
  mongo_js "$MONGO_CONTAINER" "const d = db.getSiblingDB('$1');
    d.getCollectionNames().sort().forEach(c => print(c + ' ' + d.getCollection(c).countDocuments()))"
}
STEP="count"
live=$(counts "$DB")
copy=$(counts "$SCRATCH")
mongo_js "$MONGO_CONTAINER" "db.getSiblingDB('$SCRATCH').dropDatabase()" >/dev/null

# Documents written between the dump and the count (a trade, a tick candle)
# can make live counts slightly higher; the copy must never be larger or miss
# a collection.
STEP="compare"
if [ "$(echo "$live" | awk '{print $1}')" != "$(echo "$copy" | awk '{print $1}')" ]; then
  echo "VERIFY FAILED: collection lists differ"; echo "live:"; echo "$live"; echo "copy:"; echo "$copy"; exit 1
fi
paste <(echo "$live") <(echo "$copy") | awk '$4 > $2 { bad=1; print "VERIFY FAILED: " $1 " copy " $4 " > live " $2 } END { exit bad }'
echo "== verified: $(echo "$copy" | wc -l) collections, $(du -h "$FILE" | cut -f1)"

STEP="prune"
echo "== prune archives older than $KEEP_DAYS days"
find "$DIR" -name "$DB-*.archive.gz" -mtime +"$KEEP_DAYS" -print -delete
ls -1t "$DIR" | head -5
STEP="done"
