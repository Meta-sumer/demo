#!/usr/bin/env bash
# Nightly MongoDB backup for wintratrade.online.
#
# Dumps the binary_trading database from the bt-mongo container as a gzipped
# archive into /var/backups/wintratrade, keeps the last KEEP_DAYS days, and
# verifies every new archive by restoring it into a scratch database inside
# the same mongod, comparing document counts, and dropping the scratch copy.
#
# Installed by deploy.sh as /etc/cron.d/wintratrade-backup (02:30 server time).
# Run by hand: bash /opt/wintratrade/deploy/vps/backup-mongo.sh
#
# Restore (replaces the live data - stop the API first):
#   docker compose -f docker-compose.yml -f docker-compose.prod.yml --profile full stop server
#   docker exec -i bt-mongo mongorestore --archive --gzip --drop < /var/backups/wintratrade/<file>.archive.gz
#   docker compose -f docker-compose.yml -f docker-compose.prod.yml --profile full start server
set -euo pipefail

DB=binary_trading
DIR=/var/backups/wintratrade
KEEP_DAYS=${KEEP_DAYS:-14}
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
FILE="$DIR/$DB-$STAMP.archive.gz"
SCRATCH="restore_check_$STAMP"

mkdir -p "$DIR"
chmod 700 "$DIR"

echo "== dump $DB -> $FILE"
docker exec bt-mongo mongodump --quiet --db "$DB" --archive --gzip > "$FILE.tmp"
mv "$FILE.tmp" "$FILE"
chmod 600 "$FILE"

echo "== verify by restoring into $SCRATCH"
docker exec -i bt-mongo mongorestore --quiet --archive --gzip \
  --nsFrom "$DB.*" --nsTo "$SCRATCH.*" < "$FILE"

counts() {
  docker exec bt-mongo mongosh --quiet "mongodb://localhost:27017/$1?directConnection=true" --eval '
    db.getCollectionNames().sort().forEach(c => print(c + " " + db.getCollection(c).countDocuments()))'
}
live=$(counts "$DB")
copy=$(counts "$SCRATCH")
docker exec bt-mongo mongosh --quiet "mongodb://localhost:27017/$SCRATCH?directConnection=true" --eval 'db.dropDatabase()' >/dev/null

# Documents written between the dump and the count (a trade, a tick candle)
# can make live counts slightly higher; the copy must never be larger or miss
# a collection.
if [ "$(echo "$live" | awk '{print $1}')" != "$(echo "$copy" | awk '{print $1}')" ]; then
  echo "VERIFY FAILED: collection lists differ"; echo "live:"; echo "$live"; echo "copy:"; echo "$copy"; exit 1
fi
paste <(echo "$live") <(echo "$copy") | awk '$4 > $2 { bad=1; print "VERIFY FAILED: " $1 " copy " $4 " > live " $2 } END { exit bad }'
echo "== verified: $(echo "$copy" | wc -l) collections, $(du -h "$FILE" | cut -f1)"

echo "== prune archives older than $KEEP_DAYS days"
find "$DIR" -name "$DB-*.archive.gz" -mtime +"$KEEP_DAYS" -print -delete
ls -1t "$DIR" | head -5
