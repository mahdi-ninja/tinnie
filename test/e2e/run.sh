#!/bin/sh
set -eu
cd "$(dirname "$0")"

dc() { docker compose -f compose.yml "$@"; }
sql() { dc exec -T postgres psql -U app -d app -tAq -c "$1"; }
my() { dc exec -T mariadb mariadb -ushop -pshop-pw shop -N -e "$1"; }
my8() { dc exec -T mysql mysql -ushop8 -pshop8-pw shop8 -N -e "$1" 2>/dev/null; }
s3() { dc run --rm -T --no-deps --entrypoint rclone -e RCLONE_CONFIG=/dev/null -e RCLONE_CONFIG_S_TYPE=s3 -e RCLONE_CONFIG_S_PROVIDER=Other -e RCLONE_CONFIG_S_ENDPOINT=http://storage:9000 -e RCLONE_CONFIG_S_ACCESS_KEY_ID=key -e RCLONE_CONFIG_S_SECRET_ACCESS_KEY=secret -e RCLONE_CONFIG_S_FORCE_PATH_STYLE=true tinnie "$@" 2>/dev/null; }
put() { printf '%s' "$2" | s3 rcat "s:assets/$1"; }
files() { s3 lsf -R s:assets | sort | tr '\n' ' '; }
site() { dc exec -T tinnie sh -c "cd /sources/site && $1"; }
running() { [ "$(dc ps -q --status running "$1")" != "" ]; }
fail() { echo "FAIL: $*"; dc logs tinnie | tail -40; exit 1; }
expect() { [ "$1" = "$2" ] || fail "$3: got '$1', want '$2'"; echo "ok: $3"; }

trap 'dc down -v --remove-orphans >/dev/null 2>&1' EXIT
dc down -v --remove-orphans >/dev/null 2>&1 || true

dc build tinnie
dc up -d --wait postgres storage webhook mariadb mysql app tool
dc up bucket >/dev/null 2>&1
sql "CREATE TABLE items (id int); INSERT INTO items VALUES (1), (2), (3); CREATE SCHEMA extra; CREATE TABLE extra.notes (body text); INSERT INTO extra.notes VALUES ('kept');"
my "CREATE TABLE products (id int PRIMARY KEY); INSERT INTO products VALUES (1), (2), (3); CREATE TABLE orders (id int, product int, FOREIGN KEY (product) REFERENCES products (id)); INSERT INTO orders VALUES (1, 1);"
my8 "CREATE TABLE items (id int PRIMARY KEY); INSERT INTO items VALUES (1), (2), (3);"
put one.txt "first"
put sub/two.txt "second"

dc up -d tinnie
for _ in $(seq 60); do dc logs tinnie | grep -q "backup finished" && break; sleep 2; done
dc logs tinnie | grep -q "no snapshots yet; backing up now" || fail "startup backup did not run"
dc logs tinnie | grep -q "backup finished" || fail "startup backup did not finish"
echo "ok: startup backup"
dc logs tinnie | grep -q "stopping app" || fail "app was not stopped for the backup"
dc logs tinnie | grep -q "starting app" || fail "app was not started after the backup"
running app || fail "app is not running after the backup"
echo "ok: backup stops and starts app"

dc exec -T tinnie tinnie health >/dev/null || fail "not healthy after backup"
echo "ok: healthy"
expect "$(dc exec -T tinnie tinnie check | grep -c "✓")" 9 "check passes docker, stop, every source and the repository"
dc logs webhook | grep -qE '"title": *"tinnie e2e: backup [0-9a-f]+ saved"' || fail "success notification not received"
echo "ok: success notification"

sql "INSERT INTO items VALUES (4); DROP TABLE extra.notes; CREATE SCHEMA junk;"
my "INSERT INTO products VALUES (4); DROP TABLE orders; CREATE TABLE junk (id int);"
my8 "INSERT INTO items VALUES (4); CREATE TABLE junk (id int);"
s3 deletefile s:assets/one.txt
put three.txt "third"
site "echo changed > index.php && chown 0:0 sub/page.txt && rm link && echo new > new.txt && echo after > cache/after.txt"
dc exec -T tool rm -f /tmp/restored

out=$(dc exec -T tinnie tinnie restore latest --yes 2>&1) || { echo "$out"; fail "restore failed"; }
expect "$(sql 'SELECT count(*) FROM items')" 3 "restore brings back the rows"
expect "$(sql 'SELECT body FROM extra.notes')" kept "restore brings back dropped tables"
expect "$(sql "SELECT count(*) FROM pg_namespace WHERE nspname = 'junk'")" 0 "restore removes schemas added since"
expect "$(files)" "one.txt sub/ sub/two.txt " "restore makes the bucket match the snapshot"
expect "$(my 'SELECT count(*) FROM products')" 3 "mysql restore brings back the rows"
expect "$(my 'SELECT product FROM orders')" 1 "mysql restore brings back dropped tables"
expect "$(my "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'shop' AND table_name = 'junk'")" 0 "mysql restore removes tables added since"
expect "$(my8 'SELECT count(*) FROM items')" 3 "mysql 8 restore brings back the rows"
expect "$(my8 "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'shop8' AND table_name = 'junk'")" 0 "mysql 8 restore removes tables added since"
expect "$(site 'cat index.php sub/page.txt' | tr '\n' ' ')" "hello nested " "volume restore brings back the files"
expect "$(site 'stat -c %u:%g index.php sub sub/page.txt' | tr '\n' ' ')" "33:33 33:33 33:33 " "volume restore keeps owners"
expect "$(site 'readlink link')" sub/page.txt "volume restore brings back the symlink"
expect "$(site 'ls' | tr '\n' ' ')" "cache index.php link sub " "volume restore deletes files added since"
expect "$(site 'ls cache' | tr '\n' ' ')" "after.txt old.txt " "volume restore leaves excluded paths alone"
expect "$(dc exec -T tool cat /tmp/restored)" "$(dc exec -T tool cat /etc/hostname)
payload" "command restore gets the backed-up output"
echo "$out" | grep -q "stopping app" || fail "app was not stopped for the restore"
echo "$out" | grep -q "starting app" || fail "app was not started after the restore"
running app || fail "app is not running after the restore"
echo "ok: restore stops and starts app"

sql "INSERT INTO items VALUES (5);"
put four.txt "fourth"
dc exec -T tinnie tinnie restore latest db --yes >/dev/null
expect "$(sql 'SELECT count(*) FROM items')" 3 "partial restore replaces the database"
expect "$(files)" "four.txt one.txt sub/ sub/two.txt " "partial restore leaves other sources alone"

dc exec -T tinnie tinnie restore latest nope --yes >/dev/null 2>&1 && fail "unknown source accepted"
echo "ok: unknown source rejected"
dc exec -T tinnie tinnie restore latest </dev/null >/dev/null 2>&1 && fail "restore ran without confirmation"
echo "ok: restore needs confirmation"

dc exec -T tinnie tinnie backup >/dev/null
expect "$(dc exec -T tinnie tinnie snapshots | grep -c ' e2e ')" 2 "manual backup adds a snapshot"

console=$(printf 'echo -e "\\033[2J\\033[H"\nexit\n' | dc exec -T tinnie /bin/bash -i 2>&1)
expect "$(echo "$console" | grep -c '^Sources:')" 2 "console shows help on open and after Portainer clears"

# As if tinnie had been killed after stopping app: the next backup starts it first.
app_id=$(dc ps -q app)
dc stop app >/dev/null 2>&1
dc exec -T tinnie sh -c "echo '[{\"service\":\"app\",\"id\":\"$app_id\",\"name\":\"app\"}]' > /var/lib/tinnie/stopped.json"
dc exec -T tinnie tinnie backup >/dev/null || fail "backup after a crash failed"
running app || fail "app left stopped by a crash was not started again"
dc exec -T tinnie test ! -e /var/lib/tinnie/stopped.json || fail "stopped list left behind"
echo "ok: services left stopped by a crash are started again"

sql "ALTER USER app PASSWORD 'changed';"
dc exec -T tinnie tinnie backup >/dev/null 2>&1 && fail "backup succeeded with a wrong password"
dc exec -T tinnie tinnie health >/dev/null 2>&1 && fail "healthy after a failed backup"
dc logs webhook | grep -q '"title": *"tinnie e2e: backup failed"' || fail "failure notification not received"
echo "ok: failed backup is reported"
running app || fail "app is not running after a failed backup"
echo "ok: app runs again after a failed backup"

echo "all e2e checks passed"
