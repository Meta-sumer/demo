# Shared helpers for the wintratrade.online host scripts. Source it:
#
#   . /opt/wintratrade/deploy/vps/lib.sh
#
# Nothing here prints a secret. .env is read as plain KEY=VALUE lines and
# never sourced, so a value cannot run code. Secrets reach containers through
# the environment (docker exec -e NAME, no value on any command line) and
# reach curl through a config file on stdin.

WT_APP_DIR=${WT_APP_DIR:-/opt/wintratrade}
WT_ENV_FILE=${WT_ENV_FILE:-$WT_APP_DIR/.env}

# env_get KEY [default] - the last KEY=VALUE line of .env, surrounding quotes
# removed. Prints the default when the key is missing or empty.
env_get() {
  local key=$1 def=${2-} line val
  [ -r "$WT_ENV_FILE" ] || { printf '%s' "$def"; return 0; }
  line=$(grep -E "^[[:space:]]*(export[[:space:]]+)?${key}=" "$WT_ENV_FILE" | tail -n 1) || true
  if [ -z "$line" ]; then printf '%s' "$def"; return 0; fi
  val=${line#*=}
  val=${val%$'\r'}
  case $val in
    \"*\") val=${val#\"}; val=${val%\"} ;;
    \'*\') val=${val#\'}; val=${val%\'} ;;
  esac
  [ -n "$val" ] || val=$def
  printf '%s' "$val"
}

# env_has KEY - true when .env sets KEY to a non-empty value.
env_has() { [ -n "$(env_get "$1")" ]; }

# env_ensure KEY GENERATOR... - appends KEY=$(GENERATOR) to .env once. The
# value is never printed. .env stays chmod 600.
env_ensure() {
  local key=$1; shift
  if env_has "$key"; then return 0; fi
  local val
  val=$("$@") || return 1
  [ -n "$val" ] || { echo "could not generate $key" >&2; return 1; }
  touch "$WT_ENV_FILE"
  chmod 600 "$WT_ENV_FILE"
  # Remove an empty KEY= line first, so the new value is the only one.
  if grep -qE "^${key}=$" "$WT_ENV_FILE"; then
    sed -i "/^${key}=$/d" "$WT_ENV_FILE"
  fi
  [ -z "$(tail -c 1 "$WT_ENV_FILE")" ] || printf '\n' >> "$WT_ENV_FILE"
  printf '%s=%s\n' "$key" "$val" >> "$WT_ENV_FILE"
  echo "   generated $key in .env"
}

# Random secrets: hex only, so they are safe in URLs, YAML and shell.
gen_hex32() { openssl rand -hex 32; }

# ── email ───────────────────────────────────────────────────────────────

# send_email SUBJECT BODY - sends one plain-text email with the SMTP settings
# of .env (ALERT_EMAIL_ENABLED, SMTP_HOST, SMTP_PORT, SMTP_USERNAME,
# SMTP_PASSWORD, ALERT_EMAIL_FROM, ALERT_EMAIL_TO). Port 465 is implicit TLS;
# any other port must offer STARTTLS (--ssl-reqd), never clear text.
# Returns non-zero when email is off or the send failed.
send_email() {
  local subject=$1 body=$2
  local enabled host port user pass from to prefix
  enabled=$(env_get ALERT_EMAIL_ENABLED false)
  case $enabled in true|1|yes|on) ;; *) echo "email disabled (ALERT_EMAIL_ENABLED) - not sent: $subject"; return 2 ;; esac
  host=$(env_get SMTP_HOST); port=$(env_get SMTP_PORT 587)
  user=$(env_get SMTP_USERNAME); pass=$(env_get SMTP_PASSWORD)
  from=$(env_get ALERT_EMAIL_FROM); to=$(env_get ALERT_EMAIL_TO)
  prefix=$(env_get ALERT_EMAIL_SUBJECT_PREFIX "[wintratrade]")
  if [ -z "$host" ] || [ -z "$from" ] || [ -z "$to" ]; then
    echo "email not configured (SMTP_HOST, ALERT_EMAIL_FROM, ALERT_EMAIL_TO) - not sent: $subject"; return 2
  fi
  case $port in ''|*[!0-9]*) echo "bad SMTP_PORT"; return 2 ;; esac
  # Header values on one line only.
  subject=$(printf '%s %s' "$prefix" "$subject" | tr -d '\r\n')
  from=$(printf '%s' "$from" | tr -d '\r\n')

  local url tls_flag
  if [ "$port" = 465 ]; then url="smtps://$host:$port"; tls_flag=""; else url="smtp://$host:$port"; tls_flag="--ssl-reqd"; fi

  local msg rcpts=() addr list=""
  msg=$(mktemp) || return 1
  chmod 600 "$msg"
  local IFS_SAVE=$IFS
  IFS=',;'
  for addr in $to; do
    addr=$(printf '%s' "$addr" | tr -d ' \r\n')
    [ -n "$addr" ] || continue
    rcpts+=(--mail-rcpt "$addr")
    list="${list:+$list, }$addr"
  done
  IFS=$IFS_SAVE
  [ ${#rcpts[@]} -gt 0 ] || { rm -f "$msg"; echo "ALERT_EMAIL_TO has no address"; return 2; }
  {
    printf 'From: %s\r\n' "$from"
    printf 'To: %s\r\n' "$list"
    printf 'Subject: %s\r\n' "$subject"
    printf 'Date: %s\r\n' "$(LC_ALL=C date -R)"
    printf 'MIME-Version: 1.0\r\n'
    printf 'Content-Type: text/plain; charset=UTF-8\r\n'
    printf 'Content-Transfer-Encoding: 8bit\r\n'
    printf 'Auto-Submitted: auto-generated\r\n'
    printf '\r\n'
    printf '%s\n' "$body" | sed 's/$/\r/'
  } > "$msg"

  local rc=0
  if [ -n "$user" ]; then
    # Credentials as a curl config on stdin: never on a command line.
    local esc_user esc_pass
    esc_user=$(printf '%s' "$user" | sed 's/[\\"]/\\&/g')
    esc_pass=$(printf '%s' "$pass" | sed 's/[\\"]/\\&/g')
    printf 'user = "%s:%s"\n' "$esc_user" "$esc_pass" |
      curl -sS --max-time 60 --config - $tls_flag --url "$url" --mail-from "$from" "${rcpts[@]}" --upload-file "$msg" || rc=$?
  else
    curl -sS --max-time 60 $tls_flag --url "$url" --mail-from "$from" "${rcpts[@]}" --upload-file "$msg" || rc=$?
  fi
  rm -f "$msg"
  if [ $rc -ne 0 ]; then echo "email FAILED (curl exit $rc): $subject"; return 1; fi
  echo "email sent: $subject"
}

# ── MongoDB in the bt-mongo container ───────────────────────────────────

# mongo_js CONTAINER JS - runs mongosh --eval JS in the container. When .env
# has MONGO_ROOT_PASSWORD, the script first authenticates as root through
# process.env (the password is in the exec environment only).
mongo_js() {
  local cid=$1 js=$2 port pw
  port=$(env_get MONGO_PORT 27017)
  pw=$(env_get MONGO_ROOT_PASSWORD)
  local auth='if (process.env.MONGO_ROOT_PASSWORD) { db.getSiblingDB("admin").auth("root", process.env.MONGO_ROOT_PASSWORD); }'
  MONGO_ROOT_PASSWORD="$pw" docker exec -i -e MONGO_ROOT_PASSWORD "$cid" \
    mongosh --quiet --norc --port "$port" --eval "$auth $js"
}

# mongo_tools CONTAINER COMMAND... - runs a database tool (mongodump,
# mongorestore) in the container, authenticated as root when a password is
# set: the password goes into a 0600 config file inside the container that is
# removed afterwards. stdin and stdout pass through (archives).
mongo_tools() {
  local cid=$1; shift
  local port pw
  port=$(env_get MONGO_PORT 27017)
  pw=$(env_get MONGO_ROOT_PASSWORD)
  # shellcheck disable=SC2016
  MONGO_ROOT_PASSWORD="$pw" docker exec -i -e MONGO_ROOT_PASSWORD "$cid" bash -c '
    set -e
    umask 077
    cfg=$(mktemp)
    trap "rm -f $cfg" EXIT
    auth=()
    if [ -n "${MONGO_ROOT_PASSWORD:-}" ]; then
      printf "password: \"%s\"\n" "$MONGO_ROOT_PASSWORD" > "$cfg"
      auth=(--config "$cfg" --username root --authenticationDatabase admin)
    fi
    tool=$1; shift
    "$tool" --port "'"$port"'" "${auth[@]}" "$@"
  ' wt-tools "$@"
}
