#!/bin/sh
# Install or update chess-server with PostgreSQL 18 inside a FreeBSD jail.
# Run as root inside the jail, from a copy of this repository:
#
#   CHESS_BINARY=/tmp/chess-server-freebsd-amd64 \
#   API_HOST=10.17.89.10 TRUSTED_PROXIES=10.17.89.1 \
#   sh deploy/freebsd/setup-jail.sh
#
# Before the first run:
#   1. On the host: sh deploy/freebsd/host.sh <jail>   (sysvshm=new, restart)
#   2. On a build machine: make server-freebsd, then copy
#      bin/chess-server-freebsd-amd64 into the jail
#   3. For an existing SQLite deployment: note the database path; the old
#      service is stopped before the import
#
# Required:
#   CHESS_BINARY     new chess-server binary (FreeBSD/amd64, static)
#   API_HOST         address chess-server listens on (the jail IP)
#
# Optional:
#   API_PORT         default 8080
#   TRUSTED_PROXIES  host nginx address(es) as seen from the jail; without it
#                    all proxied clients share one rate-limit bucket
#   CHESS_HOME       default: the chess account's home, else /usr/local/chess
#   LOG_DIR          default /var/log/chess
#   SQLITE_DB        v0.11 SQLite database to import once into an empty schema
#   OLD_SERVICE      rc.d name of the previous chess service to stop and
#                    disable (for example chess); skipped when unset
#   SPLIT_PRIVILEGES yes: runtime role gets DML only, schema owned by
#                    chess_owner (default no; see deploy/postgresql/setup.sql)
#   EXTRA_FLAGS      additional chess-server flags (e.g. "-max-users 500")
#   PGDATA           default /var/db/postgres/data18
#
# The script is idempotent: rerun it to upgrade the binary, change flags, or
# repair configuration. It never drops data. Steps:
#   packages -> PostgreSQL init and hardening -> role/database/schema ->
#   chess account, binary, JWT key, log dir -> stop old service -> schema
#   migration -> optional SQLite import -> rc.d/rc.conf -> log rotation ->
#   nightly backup -> start and health check
set -eu
PATH=/sbin:/bin:/usr/sbin:/usr/bin:/usr/local/sbin:/usr/local/bin
umask 022

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)

: "${CHESS_BINARY:?set CHESS_BINARY to the new chess-server binary}"
: "${API_HOST:?set API_HOST to the address chess-server listens on}"
API_PORT=${API_PORT:-8080}
TRUSTED_PROXIES=${TRUSTED_PROXIES:-}
LOG_DIR=${LOG_DIR:-/var/log/chess}
SQLITE_DB=${SQLITE_DB:-}
OLD_SERVICE=${OLD_SERVICE:-}
SPLIT_PRIVILEGES=${SPLIT_PRIVILEGES:-no}
EXTRA_FLAGS=${EXTRA_FLAGS:-}
PGDATA=${PGDATA:-/var/db/postgres/data18}
# Install locations; overridable only for testing the script.
RC_DIR=${RC_DIR:-/usr/local/etc/rc.d}
NEWSYSLOG_DIR=${NEWSYSLOG_DIR:-/usr/local/etc/newsyslog.conf.d}
CRON_DIR=${CRON_DIR:-/usr/local/etc/cron.d}
SBIN_DIR=${SBIN_DIR:-/usr/local/sbin}
BACKUP_DIR=${BACKUP_DIR:-/var/db/postgres/backups}

# The database role is named after the OS account: peer authentication maps
# one to the other, so no password exists anywhere.
account=chess
dsn='postgres:///chess?host=/tmp'

log() { printf '==> %s\n' "$*"; }
die() { printf 'setup-jail: %s\n' "$*" >&2; exit 1; }

# Run SQL from stdin as the postgres superuser against database $1.
pg_sql() {
	su -m postgres -c "psql -X -A -t -q -v ON_ERROR_STOP=1 -d $1"
}

# --- preflight ---------------------------------------------------------------
[ "$(id -u)" -eq 0 ] || die "run as root"
[ "$(uname -s)" = FreeBSD ] || die "this script targets FreeBSD"
[ -f "$CHESS_BINARY" ] || die "CHESS_BINARY not found: $CHESS_BINARY"
for f in "$repo/deploy/postgresql/setup.sql" "$repo/deploy/postgresql/migrate-sqlite.sh" \
	"$here/rc.d/chess_server" "$here/chess-backup.sh"; do
	[ -f "$f" ] || die "missing $f; run from a complete repository copy"
done
case $SPLIT_PRIVILEGES in yes | no) ;; *) die "SPLIT_PRIVILEGES must be yes or no" ;; esac
if [ -n "$SQLITE_DB" ] && [ ! -r "$SQLITE_DB" ]; then
	die "SQLITE_DB not readable: $SQLITE_DB"
fi
[ -n "$TRUSTED_PROXIES" ] ||
	log "WARNING: TRUSTED_PROXIES unset; behind nginx every client shares one rate limit"

# --- packages ----------------------------------------------------------------
packages="postgresql18-server postgresql18-client stockfish"
[ -n "$SQLITE_DB" ] && packages="$packages sqlite3"
log "installing packages: $packages"
# shellcheck disable=SC2086 # word splitting intended
pkg install -y $packages

# --- PostgreSQL ----------------------------------------------------------------
sysrc -q postgresql_enable=YES >/dev/null
sysrc -q postgresql_data="$PGDATA" >/dev/null
sysrc -q postgresql_initdb_flags="--encoding=UTF8 --locale-provider=builtin --builtin-locale=C.UTF-8 --auth-local=peer --auth-host=reject" >/dev/null

if [ ! -f "$PGDATA/PG_VERSION" ]; then
	log "initializing PostgreSQL cluster in $PGDATA"
	if ! service postgresql initdb; then
		die "initdb failed. If it reports a shared memory error, run deploy/freebsd/host.sh <jail> on the host (sysvshm=new) and retry"
	fi
fi

# Only the postgres superuser and the chess role, each over the Unix socket
# with peer authentication. No host lines: TCP is disabled below.
hba="$PGDATA/pg_hba.conf"
if ! grep -q '^# Managed by chess setup-jail.sh' "$hba" 2>/dev/null; then
	[ -f "$hba.orig" ] || cp -p "$hba" "$hba.orig"
	log "writing $hba (original kept as pg_hba.conf.orig)"
	cat >"$hba" <<'HBA'
# Managed by chess setup-jail.sh; edits are kept on rerun.
# TYPE  DATABASE  USER      METHOD
local   all       postgres  peer
local   chess     chess     peer
HBA
	chown postgres:postgres "$hba"
	chmod 600 "$hba"
fi

log "starting PostgreSQL"
service postgresql status >/dev/null 2>&1 || service postgresql start
# ALTER SYSTEM writes postgresql.auto.conf, leaving postgresql.conf untouched.
pg_sql postgres <<'SQL'
ALTER SYSTEM SET listen_addresses = '';
ALTER SYSTEM SET unix_socket_directories = '/tmp';
ALTER SYSTEM SET password_encryption = 'scram-sha-256';
SQL
# listen_addresses and unix_socket_directories apply only after a restart.
service postgresql restart

if [ -z "$(echo "SELECT 1 FROM pg_roles WHERE rolname = 'chess'" | pg_sql postgres)" ]; then
	log "provisioning role, database, and schema (split privileges: $SPLIT_PRIVILEGES)"
	split=false
	[ "$SPLIT_PRIVILEGES" = yes ] && split=true
	su -m postgres -c "psql -X -q -d postgres -v split=$split -f -" <"$repo/deploy/postgresql/setup.sql"
else
	log "role chess exists; leaving database provisioning unchanged"
fi

# --- chess account and files ---------------------------------------------------
if ! id "$account" >/dev/null 2>&1; then
	CHESS_HOME=${CHESS_HOME:-/usr/local/chess}
	log "creating account $account (home $CHESS_HOME, no shell, no password)"
	pw useradd -n "$account" -c "Chess server" -d "$CHESS_HOME" -s /usr/sbin/nologin -w no
else
	CHESS_HOME=${CHESS_HOME:-$(pw usershow "$account" | cut -d: -f9)}
	shell=$(pw usershow "$account" | cut -d: -f10)
	case $shell in
	*/nologin | */false) ;;
	*) log "WARNING: $account has login shell $shell; consider: pw usermod $account -s /usr/sbin/nologin" ;;
	esac
fi
[ -d "$CHESS_HOME" ] || install -d -o root -g "$account" -m 0750 "$CHESS_HOME"
install -d -o "$account" -g "$account" -m 0750 "$LOG_DIR"

binary="$CHESS_HOME/chess-server"
if [ -f "$binary" ] && ! cmp -s "$CHESS_BINARY" "$binary"; then
	log "keeping the previous binary as $binary.prev"
	cp -p "$binary" "$binary.prev"
fi
# Root-owned: the service account cannot replace its own executable.
install -o root -g wheel -m 0555 "$CHESS_BINARY" "$binary"

key="$CHESS_HOME/jwt.key"
if [ ! -f "$key" ]; then
	log "generating JWT signing key $key"
	(umask 077 && openssl rand -base64 48 >"$key")
fi
chown "$account:$account" "$key"
chmod 600 "$key"

# --- stop the previous service ------------------------------------------------
if [ -n "$OLD_SERVICE" ] && [ "$OLD_SERVICE" != chess_server ]; then
	log "stopping and disabling previous service $OLD_SERVICE"
	service "$OLD_SERVICE" onestop || true
	sysrc -q "${OLD_SERVICE}_enable=NO" >/dev/null || true
fi
service chess_server onestatus >/dev/null 2>&1 && service chess_server onestop

# --- schema ------------------------------------------------------------------
log "creating or migrating the chess schema"
if [ "$SPLIT_PRIVILEGES" = yes ]; then
	# The postgres account migrates as chess_owner. It may not be able to
	# traverse the chess home, so it runs a temporary copy of the binary.
	migrator=$(mktemp /tmp/chess-migrate.XXXXXX)
	install -o root -g wheel -m 0555 "$CHESS_BINARY" "$migrator"
	su -m postgres -c "$migrator db init -dsn \"dbname=chess user=postgres options='-c role=chess_owner -c search_path=chess'\"" ||
		{ rm -f "$migrator"; die "schema migration failed"; }
	rm -f "$migrator"
else
	su -m "$account" -c "$binary db init -dsn '$dsn'"
fi

# --- optional SQLite import ----------------------------------------------------
if [ -n "$SQLITE_DB" ]; then
	if [ -n "$(echo 'SELECT 1 FROM chess.users UNION ALL SELECT 1 FROM chess.games LIMIT 1' | pg_sql chess)" ]; then
		log "chess schema already has data; skipping SQLite import"
	else
		log "importing $SQLITE_DB"
		work=$(mktemp -d /tmp/chess-import.XXXXXX)
		# Copy WAL companions too: after an unclean stop they hold recent writes.
		for suffix in "" -wal -shm; do
			if [ -f "$SQLITE_DB$suffix" ]; then
				cp "$SQLITE_DB$suffix" "$work/legacy.db$suffix"
			fi
		done
		chown -R postgres "$work"
		su -m postgres -c "cd '$work' && CHESS_SCHEMA=chess sh -s -- '$work/legacy.db' dbname=chess" \
			<"$repo/deploy/postgresql/migrate-sqlite.sh"
		rm -rf "$work"
	fi
fi

# --- service configuration -------------------------------------------------------
if [ -f "$RC_DIR/chess_server" ] && ! cmp -s "$here/rc.d/chess_server" "$RC_DIR/chess_server"; then
	cp -p "$RC_DIR/chess_server" "$RC_DIR/chess_server.orig"
fi
install -d "$RC_DIR"
install -o root -g wheel -m 0555 "$here/rc.d/chess_server" "$RC_DIR/chess_server"

flags="-api-host $API_HOST -api-port $API_PORT -dsn $dsn -jwt-secret-file $key -log-level info"
[ -n "$TRUSTED_PROXIES" ] && flags="$flags -trusted-proxies $TRUSTED_PROXIES"
[ -n "$EXTRA_FLAGS" ] && flags="$flags $EXTRA_FLAGS"
sysrc -q chess_server_enable=YES >/dev/null
sysrc -q chess_server_account="$account" >/dev/null
sysrc -q chess_server_binary="$binary" >/dev/null
sysrc -q chess_server_logfile="$LOG_DIR/chess-server.log" >/dev/null
sysrc -q chess_server_env="PATH=/usr/local/bin:/usr/bin:/bin" >/dev/null
sysrc -q chess_server_flags="$flags" >/dev/null

# Rotate daily or at 1 MB, keep a week; SIGHUP makes daemon(8) reopen the log.
install -d "$NEWSYSLOG_DIR"
printf '%s\n' \
	"# Managed by chess setup-jail.sh" \
	"$LOG_DIR/chess-server.log	$account:$account	640	7	1000	@T00	JC	/var/run/chess_server.pid	1" \
	>"$NEWSYSLOG_DIR/chess_server.conf"

# Nightly pg_dump at 03:30, as postgres, keeping 14 days.
install -d "$SBIN_DIR"
install -o root -g wheel -m 0555 "$here/chess-backup.sh" "$SBIN_DIR/chess-backup"
install -d -o postgres -g postgres -m 0700 "$BACKUP_DIR"
install -d "$CRON_DIR"
printf '%s\n' \
	"# Managed by chess setup-jail.sh" \
	"SHELL=/bin/sh" \
	"PATH=/sbin:/bin:/usr/sbin:/usr/bin:/usr/local/sbin:/usr/local/bin" \
	"30	3	*	*	*	postgres	CHESS_BACKUP_DIR=$BACKUP_DIR $SBIN_DIR/chess-backup" \
	>"$CRON_DIR/chess-backup"

# --- start and verify -----------------------------------------------------------
log "starting chess_server"
service chess_server start
health=""
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
	if health=$(fetch -q -T 2 -o - "http://$API_HOST:$API_PORT/health" 2>/dev/null); then
		break
	fi
	sleep 2
done
case $health in
*'"storage":"ok"'*) log "healthy: $health" ;;
*) die "chess-server did not report healthy storage (got: ${health:-no response}); see $LOG_DIR/chess-server.log" ;;
esac

cat <<EOF

chess-server is running at http://$API_HOST:$API_PORT with PostgreSQL storage.

Remaining host step: add the location block from deploy/freebsd/nginx-chess.conf
(proxy_pass http://$API_HOST:$API_PORT/) to the host nginx, then
    nginx -t && service nginx reload

Administration inside the jail:
    su -m $account -c '$binary db user list -dsn "$dsn"'
    service chess_server status|restart
    tail -f $LOG_DIR/chess-server.log
EOF
