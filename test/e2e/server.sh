#!/bin/sh
# End-to-end test of tinnie-server: a tinnie client backs up to it, clients can only append, and the server
# runs retention.
set -eu
cd "$(dirname "$0")"

dc() { docker compose -f server.yml "$@"; }
srv() { dc exec -T server "$@"; }
# HTTP status of a GET to the server from inside the network, with optional user:password in the URL.
status() { out=$(dc run --rm -T --no-deps --entrypoint sh client -c "wget -q -O /dev/null 'http://${2:+$2@}server:8000$1' 2>&1" || true); code=$(echo "$out" | grep -o 'HTTP/[0-9.]* [0-9]*' | cut -d' ' -f2); echo "${code:-200}"; }
denied() { case "$1" in 401|403) echo "ok: $2";; *) fail "$2: got '$1', want 401 or 403";; esac; }
fail() { echo "FAIL: $*"; dc logs server client | tail -60; exit 1; }
expect() { [ "$1" = "$2" ] || fail "$3: got '$1', want '$2'"; echo "ok: $3"; }
count_snapshots() { srv tinnie-server snapshots mysite | grep -c ' mysite ' || true; }

trap 'dc down -v --remove-orphans >/dev/null 2>&1' EXIT
dc down -v --remove-orphans >/dev/null 2>&1 || true

dc build server client
dc up -d --wait server webhook
srv tinnie-server health >/dev/null || fail "server not healthy after start"
echo "ok: server starts and is healthy"
dc logs server | grep -q "rest-server listening on port 8000" || fail "rest-server did not start"
expect "$(srv cat /data/.tinnie-server/htpasswd | cut -d: -f1 | tr '\n' ' ')" "mysite other " "password file lists every stack"

denied "$(status /mysite/config)" "no credentials are refused"
denied "$(status /mysite/config mysite:wrong)" "a wrong password is refused"
denied "$(status /other/config mysite:mysite-rest)" "a stack cannot reach another stack's repository"

dc up -d client
for _ in $(seq 60); do dc logs client | grep -q "backup finished" && break; sleep 2; done
dc logs client | grep -q "backup finished" || fail "client backup did not finish"
echo "ok: client backs up to the server"
expect "$(count_snapshots)" 1 "server lists the client's snapshot"
expect "$(srv tinnie-server check | grep -c '✓')" 2 "check passes rest-server and the backup_password"
srv tinnie-server check | grep -q -- "- other: no backup_password" || fail "check does not report the stack without a backup_password"

dc exec -T client tinnie backup >/dev/null
dc exec -T client tinnie backup >/dev/null
expect "$(count_snapshots)" 3 "more backups add snapshots"

# Clients may only append: a forget from the client side is refused by the server.
dc exec -T -e RESTIC_REPOSITORY=rest:http://mysite:mysite-rest@server:8000/mysite -e RESTIC_PASSWORD=mysite-backup client restic forget --keep-last 1 --prune >/dev/null 2>&1 && fail "client could delete snapshots"
expect "$(count_snapshots)" 3 "client cannot delete snapshots"

srv tinnie-server forget >/dev/null || fail "server retention failed"
expect "$(count_snapshots)" 1 "server retention keeps the last snapshot"
srv tinnie-server health >/dev/null || fail "not healthy after retention"
dc logs webhook | grep -q '"title": *"tinnie-server: retention done"' || fail "retention notification not received"
echo "ok: retention is notified"

help=$(srv tinnie-server help)
echo "$help" | grep -q "rest:http://mysite:<password>@server:8000/mysite" || fail "help does not show the client url"
echo "$help" | grep -q "last snapshot" || fail "help does not show the last snapshot"
echo "$help" | grep -q "other" || fail "help does not list the stack without backups"
echo "ok: help shows stacks and urls"
console=$(printf 'echo -e "\\033[2J\\033[H"\nexit\n' | dc exec -T server /bin/bash -i 2>&1)
expect "$(echo "$console" | grep -c '^Stacks:')" 2 "console shows help on open and after Portainer clears"

expect "$(srv tinnie-server restic mysite snapshots --json | grep -o '"short_id"' | wc -l | tr -d ' ')" 1 "restic passthrough works"

# A restore from the client still works against the append-only server.
dc exec -T client sh -c "echo changed > /sources/site/index.html"
dc exec -T client tinnie restore latest --yes >/dev/null 2>&1 || fail "client restore failed"
expect "$(dc exec -T client cat /sources/site/index.html)" hello "client restores from the server"

echo "all server e2e checks passed"
