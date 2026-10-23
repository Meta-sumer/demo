#!/usr/bin/env bash
# Turns on MongoDB access control for wintratrade.online, and keeps it on.
# Called by deploy.sh and first-setup.sh; safe to run any number of times.
#
# Users (both defined in the admin database):
#   root                 role root           - backups, restore, operators
#   MONGO_APP_USERNAME   readWrite on binary_trading - the API and the seed
# Passwords: MONGO_ROOT_PASSWORD and MONGO_APP_PASSWORD in .env (generated
# by deploy.sh / first-setup.sh, never printed).
#
# What it does depends on what is running:
#
#   no mongo container   start it (docker-compose.prod.yml: --auth --keyFile);
#                        on a fresh volume the users are created through the
#                        localhost exception
#   running, auth OFF    (an install from before access control) create the
#                        users while auth is still off, check the app user can
#                        sign in, then recreate the container with --auth
#                        --keyFile and check again. If anything fails before
#                        the restart, nothing has changed.
#   running, auth ON     sign in as root and make sure both users exist with
#                        the passwords in .env (a no-op on a normal deploy)
#
# The data volume is never touched. Override COMPOSE to run against another
# compose project (the local test uses -p wintratrade-authtest).
set -euo pipefail

. "$(dirname "$0")/lib.sh"
cd "$WT_APP_DIR"

COMPOSE=${COMPOSE:-"docker compose -f docker-compose.yml -f docker-compose.prod.yml --profile full"}
PORT=$(env_get MONGO_PORT 27017)
APP_USER=$(env_get MONGO_APP_USERNAME wintratrade)

for k in MONGO_ROOT_PASSWORD MONGO_APP_PASSWORD; do
  env_has "$k" || { echo "$k is missing from $WT_ENV_FILE"; exit 1; }
done
case $APP_USER in root|admin|''|*[!A-Za-z0-9_.-]*) echo "invalid MONGO_APP_USERNAME"; exit 1 ;; esac

cid() { $COMPOSE ps -q mongo 2>/dev/null | head -n 1; }

running() {
  local id; id=$(cid)
  [ -n "$id" ] && [ "$(docker inspect -f '{{.State.Running}}' "$id" 2>/dev/null)" = true ]
}

wait_healthy() {
  local id i
  for i in $(seq 1 90); do
    id=$(cid)
    if [ -n "$id" ] && [ "$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{end}}' "$id" 2>/dev/null)" = healthy ]; then
      return 0
    fi
    sleep 2
  done
  echo "MongoDB did not become healthy - docker logs $(cid) --tail 50"
  return 1
}

# Runs JS with the credentials in the exec environment only.
js() {
  local id; id=$(cid)
  MONGO_ROOT_PASSWORD=$(env_get MONGO_ROOT_PASSWORD) MONGO_APP_PASSWORD=$(env_get MONGO_APP_PASSWORD) MONGO_APP_USERNAME=$APP_USER \
    docker exec -i -e MONGO_ROOT_PASSWORD -e MONGO_APP_PASSWORD -e MONGO_APP_USERNAME "$id" \
    mongosh --quiet --norc --port "$PORT" --eval "$1"
}

# "off" when an unauthenticated client can list databases, "on" when it is
# refused for lack of authentication.
auth_state() {
  js 'try { db.adminCommand({ listDatabases: 1, nameOnly: true }); print("off") }
      catch (e) { print(e.code === 13 || e.codeName === "Unauthorized" ? "on" : "error: " + e.message) }' | tail -n 1
}

ensure_users() {
  js '
    const admin = db.getSiblingDB("admin");
    const rootPw = process.env.MONGO_ROOT_PASSWORD, appUser = process.env.MONGO_APP_USERNAME, appPw = process.env.MONGO_APP_PASSWORD;
    const authAs = (u, p) => { try { admin.auth(u, p); return true } catch (e) { return false } };
    if (!authAs("root", rootPw)) {
      // Access control is off, or on with no user yet (localhost exception).
      try {
        admin.createUser({ user: "root", pwd: rootPw, roles: [{ role: "root", db: "admin" }] });
        print("   created MongoDB user root");
      } catch (e) {
        if (!/already exists/.test(e.message)) throw e;
        // root exists with another password: fixable only while auth is off.
        admin.updateUser("root", { pwd: rootPw });
        print("   reset the password of MongoDB user root to the one in .env");
      }
      if (!authAs("root", rootPw)) throw new Error("cannot sign in as root after creating it");
    }
    const roles = [{ role: "readWrite", db: "binary_trading" }];
    if (admin.getUser(appUser)) {
      admin.updateUser(appUser, { pwd: appPw, roles });
      print("   MongoDB user " + appUser + " present (password and roles kept in line with .env)");
    } else {
      admin.createUser({ user: appUser, pwd: appPw, roles });
      print("   created MongoDB user " + appUser + " (readWrite on binary_trading)");
    }
  '
}

# The API user can sign in and read and write its database.
check_app_user() {
  js '
    const c = new Mongo("mongodb://localhost:'"$PORT"'/?directConnection=true");
    const a = c.getDB("admin");
    a.auth(process.env.MONGO_APP_USERNAME, process.env.MONGO_APP_PASSWORD);
    const d = c.getDB("binary_trading");
    d.getCollectionNames();
    print("   app user can sign in and read binary_trading");
  '
}

if ! running; then
  echo "== mongo: starting with access control"
  $COMPOSE up -d mongo
  wait_healthy
  ensure_users
  check_app_user
  exit 0
fi

state=$(auth_state)
case $state in
  off)
    echo "== mongo: access control is OFF - creating users, then restarting with --auth"
    ensure_users
    check_app_user
    $COMPOSE up -d mongo
    wait_healthy
    state=$(auth_state)
    [ "$state" = on ] || { echo "MongoDB restarted but access control is not on ($state)"; exit 1; }
    check_app_user
    echo "== mongo: access control is ON"
    ;;
  on)
    ensure_users
    ;;
  *)
    echo "cannot tell whether MongoDB access control is on: $state"
    exit 1
    ;;
esac
