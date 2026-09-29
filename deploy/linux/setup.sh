#!/usr/bin/env bash
# Install or upgrade chess-server as the systemd service `chessd` on a Linux
# host (Debian, Ubuntu, Arch, or another systemd distribution) that already
# runs PostgreSQL 17 or later. Run as root from a checkout:
#
#   sudo CHESS_BINARY=bin/chess-server deploy/linux/setup.sh
#
# Prerequisites: PostgreSQL 17+ running and reachable by the postgres account
# over its Unix socket; stockfish; the chess-server binary (`make server`).
#
# Inputs (environment):
#   CHESS_BINARY      required: chess-server binary to install
#   TRUSTED_PROXIES   reverse proxy address(es) whose X-Real-IP is trusted,
#                     e.g. 127.0.0.1 for nginx on this host; stored in
#                     chessd.env (kept from the existing file when unset)
#   CHESSD_HOST       listen address for a first install (default 127.0.0.1)
#   CHESSD_PORT       listen port for a first install (default 8080)
#   SPLIT_PRIVILEGES  yes|no (default no); see deploy/postgresql/setup.sql
#   ENABLE_BACKUP     yes|no (default no): nightly pg_dump systemd timer
#   PG_HBA            yes|no (default no): if the chess role cannot connect,
#                     add `local chess chess peer` to pg_hba.conf and reload
#
# What it installs:
#   account chess (system, no login shell, home /var/lib/chessd)
#   /usr/local/bin/chess-server        (previous copy kept as .prev)
#   /usr/local/sbin/chess-db           runs `chess-server db ...` as chess
#   /etc/chessd/chessd.env             settings, written on first install only
#   /var/lib/chessd/jwt.key            JWT signing key, generated once
#   /etc/systemd/system/chessd.service (+ chess-backup.{service,timer})
#
# Steps (idempotent; rerun to upgrade):
#   1. checks: root, systemd, PostgreSQL version and socket, stockfish
#   2. role, database, and schema from deploy/postgresql/setup.sql when
#      neither exists; the chess role must connect over the socket
#   3. stop chessd; install files; key; schema migration
#   4. enable and start chessd; check /health
set -euo pipefail
umask 022

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)

: "${CHESS_BINARY:?set CHESS_BINARY to the chess-server binary to install}"
TRUSTED_PROXIES=${TRUSTED_PROXIES-__keep__}
CHESSD_HOST=${CHESSD_HOST:-127.0.0.1}
CHESSD_PORT=${CHESSD_PORT:-8080}
SPLIT_PRIVILEGES=${SPLIT_PRIVILEGES:-no}
ENABLE_BACKUP=${ENABLE_BACKUP:-no}
PG_HBA=${PG_HBA:-no}
# System locations and the service manager; overridable only to test the
# script.
BIN_DIR=${BIN_DIR:-/usr/local/bin}
SBIN_DIR=${SBIN_DIR:-/usr/local/sbin}
CONF_DIR=${CONF_DIR:-/etc/chessd}
STATE_DIR=${STATE_DIR:-/var/lib/chessd}
UNIT_DIR=${UNIT_DIR:-/etc/systemd/system}
SYSTEMCTL=${SYSTEMCTL:-systemctl}

user=chess
bin="$BIN_DIR/chess-server"
env_file="$CONF_DIR/chessd.env"
key="$STATE_DIR/jwt.key"

log() { printf '==> %s\n' "$*"; }
warn() { printf 'setup: warning: %s\n' "$*" >&2; }
die() { printf 'setup: %s\n' "$*" >&2; exit 1; }

# pg runs psql as the postgres account over the default socket (or $PGHOST).
pg() { runuser -u postgres -- psql -X -w -A -t -q -v ON_ERROR_STOP=1 "$@"; }

# --- 1. checks ---------------------------------------------------------------
[ "$(id -u)" -eq 0 ] || die "run as root"
for setting in SPLIT_PRIVILEGES ENABLE_BACKUP PG_HBA; do
	case "${!setting}" in
	yes | no) ;;
	*) die "$setting must be yes or no" ;;
	esac
done
if [ "$SYSTEMCTL" = systemctl ] && [ ! -d /run/systemd/system ]; then
	die "systemd is not running; see doc/deployment-linux.md to run chess-server another way"
fi
for tool in runuser psql useradd; do
	command -v "$tool" >/dev/null || die "$tool not found"
done
[ -f "$CHESS_BINARY" ] || die "CHESS_BINARY not found: $CHESS_BINARY"
# A binary for another OS or architecture fails here rather than at start.
case "$("$CHESS_BINARY" db 2>&1 || true)" in
*"subcommand required"*) ;;
*) die "$CHESS_BINARY does not run on this host (wrong OS or architecture?)" ;;
esac
for f in "$repo/deploy/postgresql/setup.sql" "$here/chessd.service" \
	"$here/chess-backup.service" "$here/chess-backup.timer" "$repo/deploy/postgresql/chess-backup.sh"; do
	[ -f "$f" ] || die "missing $f; run from a complete checkout"
done
if ! command -v stockfish >/dev/null && [ ! -x /usr/games/stockfish ]; then
	warn "stockfish not found: install it (Debian/Ubuntu: apt install stockfish; Arch: pacman -S stockfish)"
fi

version=$(pg -d postgres -c 'SHOW server_version_num') ||
	die "cannot reach PostgreSQL as postgres over its Unix socket: start it (systemctl start postgresql; on Arch run initdb first, see doc/deployment-linux.md) or set PGHOST"
[ "$version" -ge 170000 ] || die "PostgreSQL 17 or later required (found $version)"
socket=${PGHOST:-$(pg -d postgres -c 'SHOW unix_socket_directories' | cut -d, -f1 | tr -d ' ')}
case "$socket" in
/*) ;;
*) die "no usable Unix socket directory ($socket); set PGHOST to it" ;;
esac
dsn="host=$socket dbname=chess"
log "PostgreSQL $version, socket $socket"

# --- 2. account, role, database ----------------------------------------------
if ! id "$user" >/dev/null 2>&1; then
	nologin=$(command -v nologin || echo /usr/sbin/nologin)
	log "creating system account $user"
	useradd --system --user-group --home-dir "$STATE_DIR" --no-create-home \
		--shell "$nologin" --comment "chess-server" "$user"
fi
group=$(id -gn "$user")

role=$(pg -d postgres -c "SELECT 1 FROM pg_roles WHERE rolname = 'chess'")
database=$(pg -d postgres -c "SELECT 1 FROM pg_database WHERE datname = 'chess'")
case "$role$database" in
11) log "role and database chess exist" ;;
"")
	log "creating role chess, database chess, and schema chess (split privileges: $SPLIT_PRIVILEGES)"
	split=false
	[ "$SPLIT_PRIVILEGES" = yes ] && split=true
	# Fed on stdin: postgres may not be able to read the checkout.
	pg -d postgres -v split="$split" -f - <"$repo/deploy/postgresql/setup.sql"
	;;
*) die "only one of role chess and database chess exists; drop it or create the other (doc/database.md)" ;;
esac

chess_can_connect() {
	runuser -u "$user" -- psql -X -w -A -t -q -h "$socket" -d chess -c 'SELECT current_user' >/dev/null 2>&1
}
if ! chess_can_connect; then
	hba=$(pg -d postgres -c 'SHOW hba_file')
	if [ "$PG_HBA" != yes ]; then
		die "the chess account cannot connect to database chess over $socket. Add this line to
$hba above the other 'local' lines, then reload PostgreSQL (or rerun with PG_HBA=yes):
    local   chess   chess   peer"
	fi
	saved="$hba.chessd-$(date -u +%Y%m%dT%H%M%SZ)"
	cp -p "$hba" "$saved"
	awk 'BEGIN { done = 0 }
		!done && /^[[:space:]]*local[[:space:]]/ { print "local   chess   chess   peer"; done = 1 }
		{ print }
		END { if (!done) print "local   chess   chess   peer" }' "$saved" >"$hba"
	pg -d postgres -c 'SELECT pg_reload_conf()' >/dev/null
	log "pg_hba.conf: added local chess chess peer (previous copy $saved)"
	chess_can_connect || die "the chess account still cannot connect; check $hba"
fi

# --- 3. files ----------------------------------------------------------------
if "$SYSTEMCTL" is-active --quiet chessd 2>/dev/null; then
	log "stopping chessd"
	"$SYSTEMCTL" stop chessd
fi

install -d -m 0755 "$BIN_DIR" "$SBIN_DIR"
if [ -f "$bin" ] && cmp -s "$CHESS_BINARY" "$bin"; then
	chown root:root "$bin"
	chmod 0755 "$bin"
else
	if [ -f "$bin" ]; then
		log "keeping the previous binary as $bin.prev"
		cp -p "$bin" "$bin.prev"
	fi
	install -o root -g root -m 0755 "$CHESS_BINARY" "$bin"
fi

cat >"$SBIN_DIR/chess-db.new" <<EOF
#!/bin/sh
# Runs \`chess-server db ...\` as the chess account against the chess database,
# e.g. chess-db user add -username alice. Installed by deploy/linux/setup.sh.
set -a
. "$env_file"
exec runuser -u $user -- env CHESS_DSN="\$CHESSD_DSN" "$bin" db "\$@"
EOF
chmod 0755 "$SBIN_DIR/chess-db.new"
mv "$SBIN_DIR/chess-db.new" "$SBIN_DIR/chess-db"

install -d -o root -g "$group" -m 0750 "$CONF_DIR"
if [ ! -f "$env_file" ]; then
	proxies=$TRUSTED_PROXIES
	[ "$proxies" = __keep__ ] && proxies=
	log "writing $env_file"
	cat >"$env_file" <<EOF
# chessd settings, read by chessd.service; restart chessd after a change.
CHESSD_HOST=$CHESSD_HOST
CHESSD_PORT=$CHESSD_PORT
# PostgreSQL over the Unix socket as the OS account chess (peer or trust).
CHESSD_DSN="$dsn"
CHESSD_JWT_KEY=$key
# Reverse proxy address(es), comma-separated, whose X-Real-IP is trusted.
CHESSD_TRUSTED_PROXIES=$proxies
# Extra server flags, e.g. -max-users 500 -db-cleanup report
CHESSD_FLAGS=
EOF
elif [ "$TRUSTED_PROXIES" != __keep__ ]; then
	sed -i "s|^CHESSD_TRUSTED_PROXIES=.*|CHESSD_TRUSTED_PROXIES=$TRUSTED_PROXIES|" "$env_file"
	log "$env_file: CHESSD_TRUSTED_PROXIES=$TRUSTED_PROXIES"
fi
chown root:"$group" "$env_file"
chmod 0640 "$env_file"
# The settings in effect, which may differ from this run's defaults.
# shellcheck disable=SC1090
settings=$(set -a && . "$env_file" && printf '%s|%s|%s|%s' \
	"$CHESSD_HOST" "$CHESSD_PORT" "$CHESSD_TRUSTED_PROXIES" "$CHESSD_DSN")
IFS='|' read -r host port proxies dsn <<<"$settings"
[ -n "$proxies" ] || warn "no trusted proxy: behind a reverse proxy every client shares one rate-limit bucket (set TRUSTED_PROXIES)"

install -d -o "$user" -g "$group" -m 0700 "$STATE_DIR"
if [ ! -f "$key" ]; then
	log "generating JWT signing key $key"
	(umask 077 && head -c 48 /dev/urandom | base64 -w0 >"$key")
fi
chown "$user:$group" "$key"
chmod 0600 "$key"

install -o root -g root -m 0644 "$here/chessd.service" "$UNIT_DIR/chessd.service"
if [ "$ENABLE_BACKUP" = yes ]; then
	install -o root -g root -m 0755 "$repo/deploy/postgresql/chess-backup.sh" "$SBIN_DIR/chess-backup"
	install -o root -g root -m 0644 "$here/chess-backup.service" "$UNIT_DIR/chess-backup.service"
	install -o root -g root -m 0644 "$here/chess-backup.timer" "$UNIT_DIR/chess-backup.timer"
fi
"$SYSTEMCTL" daemon-reload

log "creating or migrating the chess schema"
if [ "$SPLIT_PRIVILEGES" = yes ]; then
	runuser -u postgres -- "$bin" db init \
		-dsn "host=$socket dbname=chess user=postgres options='-c role=chess_owner -c search_path=chess'"
else
	runuser -u "$user" -- "$bin" db init -dsn "$dsn"
fi

# --- 4. start ----------------------------------------------------------------
if [ "$ENABLE_BACKUP" = yes ]; then
	"$SYSTEMCTL" enable --now chess-backup.timer
	log "backups: chess-backup.timer -> $(getent passwd postgres | cut -d: -f6)/backups"
fi
"$SYSTEMCTL" enable chessd
"$SYSTEMCTL" start chessd

case "$host" in
0.0.0.0 | "" | ::) probe=127.0.0.1 ;;
*:*) probe="[$host]" ;;
*) probe=$host ;;
esac
url="http://$probe:$port/health"
health=
for _ in $(seq 1 30); do
	if health=$(curl -fsS --max-time 2 "$url" 2>/dev/null); then
		break
	fi
	sleep 0.5
done
case "$health" in
*'"storage":"ok"'*) log "chessd is up: $health" ;;
*) die "chessd did not report healthy storage at $url (journalctl -u chessd): ${health:-no answer}" ;;
esac
log "done. Create an account with: chess-db user add -username <name>"
