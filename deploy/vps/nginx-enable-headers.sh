#!/usr/bin/env bash
# ONE-TIME manual step on a server installed before the security headers
# existed. Run it as root after a deploy.sh that copied
# /etc/nginx/snippets/wintratrade-security-headers.conf:
#
#   bash /opt/wintratrade/deploy/vps/nginx-enable-headers.sh
#
# The site file /etc/nginx/conf.d/wintratrade.conf is managed by certbot and
# deploy.sh never overwrites it. This script edits it in place: after each
# `add_header Cache-Control ...` line of the page locations (/ and /assets/
# of the client and admin sites, in the HTTPS blocks certbot added and in the
# HTTP ones), it adds the include of the headers snippet, unless it is there
# already. It keeps a dated backup, runs `nginx -t`, and restores the backup
# if the test fails. Running it again changes nothing.
set -euo pipefail

SITE=${1:-/etc/nginx/conf.d/wintratrade.conf}
SNIPPET=/etc/nginx/snippets/wintratrade-security-headers.conf

[ "$(id -u)" -eq 0 ] || { echo "run as root"; exit 1; }
[ -f "$SNIPPET" ] || { echo "$SNIPPET is missing - run ./deploy.sh first"; exit 1; }
[ -f "$SITE" ] || { echo "$SITE not found"; exit 1; }

backup="$SITE.bak-$(date -u +%Y%m%dT%H%M%SZ)"
cp -p "$SITE" "$backup"
tmp=$(mktemp)
awk -v snip="$SNIPPET" '
  { lines[NR] = $0 }
  END {
    for (i = 1; i <= NR; i++) {
      print lines[i]
      if (lines[i] ~ /add_header[ \t]+Cache-Control[ \t]+"(no-cache|public, immutable)";/ &&
          lines[i + 1] !~ /wintratrade-security-headers\.conf/) {
        match(lines[i], /^[ \t]*/)
        print substr(lines[i], 1, RLENGTH) "include " snip ";"
        added++
      }
    }
    print added + 0 > "/dev/stderr"
  }' "$SITE" > "$tmp" 2> "$tmp.count"
added=$(cat "$tmp.count"); rm -f "$tmp.count"

if [ "$added" = 0 ]; then
  rm -f "$tmp" "$backup"
  echo "nothing to do: every page location already includes the headers"
  exit 0
fi
cat "$tmp" > "$SITE"
rm -f "$tmp"
if ! nginx -t -q; then
  cp -p "$backup" "$SITE"
  echo "nginx -t FAILED - $SITE restored from $backup"
  exit 1
fi
systemctl reload nginx
echo "added the headers include to $added location(s); backup: $backup"
echo "check: curl -sI https://wintratrade.online/ | grep -i content-security-policy"
