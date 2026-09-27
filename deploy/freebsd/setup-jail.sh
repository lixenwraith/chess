#!/bin/sh
# Install or upgrade chess-server (rc.d service `chessd`) with PostgreSQL
# inside a FreeBSD jail. Run as root inside the jail from a copy of this
# repository:
#
#   CHESS_BINARY=/tmp/chess-server TRUSTED_PROXIES=<nginx-address> \
#       sh deploy/freebsd/setup-jail.sh
#
# Prerequisites:
#   - postgresql18-server, postgresql18-client, and stockfish packages
#   - the jail has its own System V shared memory (deploy/freebsd/host.sh)
#   - the release binary, built with `make server-freebsd` (or `make server`
#     on FreeBSD)
#
# Inputs (environment):
#   CHESS_BINARY         required: chess-server binary to install
#   TRUSTED_PROXIES      address(es) the host nginx connects from, stored as
#                        chessd_trusted_proxies in rc.conf. Without it, every
#                        proxied client shares one rate-limit bucket.
#   SPLIT_PRIVILEGES     yes|no (default no); see deploy/postgresql/setup.sql
#   ENABLE_LOG_ROTATION  yes|no (default no): newsyslog entry for the log
#   ENABLE_BACKUP        yes|no (default no): nightly pg_dump via cron
#
# The service layout comes from rc.conf (chessd_*) with the defaults of
# deploy/freebsd/rc.d/chessd: account chess, home /home/chess, binary
# ~/bin/chess-server, log /var/log/chessd.log, 0.0.0.0:8080.
#
# Steps (idempotent; rerun to upgrade):
#   1. PostgreSQL: initdb only if no cluster exists; pg_hba.conf allows only
#      postgres and chess over the Unix socket (peer); TCP listener disabled
#   2. role, database, and schema from deploy/postgresql/setup.sql (first run)
#   3. stop chessd; install the binary (previous kept as .prev) and the rc.d
#      script (previous copy saved under /var/backups)
#   4. JWT key; schema migration; rc.conf settings
#   5. optional log rotation and backups
#   6. start chessd and check /health
# Files of a previous SQLite release are left untouched; remove them once the
# new release is verified.
set -eu
PATH=/sbin:/bin:/usr/sbin:/usr/bin:/usr/local/sbin:/usr/local/bin
umask 022

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)

: "${CHESS_BINARY:?set CHESS_BINARY to the chess-server binary to install}"
TRUSTED_PROXIES=${TRUSTED_PROXIES:-}
SPLIT_PRIVILEGES=${SPLIT_PRIVILEGES:-no}
ENABLE_LOG_ROTATION=${ENABLE_LOG_ROTATION:-no}
ENABLE_BACKUP=${ENABLE_BACKUP:-no}
# System locations; overridable only to test the script.
RC_DIR=${RC_DIR:-/usr/local/etc/rc.d}
RC_BACKUP_DIR=${RC_BACKUP_DIR:-/var/backups}
NEWSYSLOG_DIR=${NEWSYSLOG_DIR:-/usr/local/etc/newsyslog.conf.d}
CRON_DIR=${CRON_DIR:-/usr/local/etc/cron.d}
SBIN_DIR=${SBIN_DIR:-/usr/local/sbin}
BACKUP_DIR=${BACKUP_DIR:-/var/db/postgres/backups}

log() { printf '==> %s\n' "$*"; }
die() { printf 'setup-jail: %s\n' "$*" >&2; exit 1; }

# SQL from stdin, run as the postgres superuser against database $1.
pg_sql() {
	su -m postgres -c "psql -X -A -t -q -v ON_ERROR_STOP=1 -d $1"
}

# rc.conf value of $1, or the default $2.
rc_value() {
	_value=$(sysrc -n "$1" 2>/dev/null) || _value=""
	if [ -n "$_value" ]; then echo "$_value"; else echo "$2"; fi
}

yes_no() {
	case $2 in yes | no) ;; *) die "$1 must be yes or no" ;; esac
}

# --- preflight -----------------------------------------------------------------
[ "$(id -u)" -eq 0 ] || die "run as root"
[ "$(uname -s)" = FreeBSD ] || die "this script targets FreeBSD"
[ -f "$CHESS_BINARY" ] || die "CHESS_BINARY not found: $CHESS_BINARY"
for f in "$repo/deploy/postgresql/setup.sql" "$here/rc.d/chessd" "$here/chess-backup.sh"; do
	[ -f "$f" ] || die "missing $f; run from a complete repository copy"
done
yes_no SPLIT_PRIVILEGES "$SPLIT_PRIVILEGES"
yes_no ENABLE_LOG_ROTATION "$ENABLE_LOG_ROTATION"
yes_no ENABLE_BACKUP "$ENABLE_BACKUP"

command -v postgres >/dev/null 2>&1 || die "PostgreSQL is not installed (pkg install postgresql18-server postgresql18-client)"
pg_major=$(postgres --version | sed -n 's/^postgres (PostgreSQL) \([0-9][0-9]*\).*/\1/p')
[ -n "$pg_major" ] && [ "$pg_major" -ge 17 ] || die "PostgreSQL 17 or newer is required (found: $(postgres --version))"
command -v stockfish >/dev/null 2>&1 || log "WARNING: stockfish not found in PATH; computer moves will fail"

user=$(rc_value chessd_user chess)
group=$(rc_value chessd_group chess)
[ "$user" = chess ] || die "chessd_user is $user; peer authentication requires the OS account and database role to both be chess"
if ! id "$user" >/dev/null 2>&1; then
	log "creating account $user (home /home/$user, no shell, no password)"
	pw useradd -n "$user" -c "Chess server" -d "/home/$user" -m -s /usr/sbin/nologin -w no
fi
# chessd_home: rc.conf, else the account's home. rc.d/chessd defaults to
# /home/chess, so any other home is recorded in rc.conf.
account_home=$(pw usershow "$user" | cut -d: -f9)
home=$(rc_value chessd_home "$account_home")
if [ "$home" != /home/chess ] && [ -z "$(rc_value chessd_home "")" ]; then
	sysrc -q chessd_home="$home" >/dev/null
fi
bin=$(rc_value chessd_bin "$home/bin/chess-server")
host=$(rc_value chessd_host 0.0.0.0)
port=$(rc_value chessd_port 8080)
dsn=$(rc_value chessd_dsn 'postgres:///chess?host=/tmp')
key=$(rc_value chessd_jwt_key "$home/jwt.key")

case $(rc_value chessd_flags "") in
*-storage-path*) die "rc.conf chessd_flags contains -storage-path, which no longer exists; remove it (sysrc -x chessd_flags) and rerun" ;;
esac

# --- PostgreSQL -------------------------------------------------------------------
pgdata=$(rc_value postgresql_data "/var/db/postgres/data$pg_major")
if [ ! -f "$pgdata/PG_VERSION" ]; then
	log "initializing PostgreSQL cluster in $pgdata"
	sysrc -q postgresql_enable=YES >/dev/null
	sysrc -q postgresql_initdb_flags="--encoding=UTF8 --locale-provider=builtin --builtin-locale=C.UTF-8 --auth-local=peer --auth-host=reject" >/dev/null
	service postgresql initdb ||
		die "initdb failed; a shared memory error means the jail needs sysvshm=new (deploy/freebsd/host.sh)"
fi
sysrc -q postgresql_enable=YES >/dev/null
if ! service postgresql status >/dev/null 2>&1; then
	log "starting PostgreSQL"
	service postgresql start
fi

# Only the postgres superuser and the chess role, each over the Unix socket
# with peer authentication. A cluster initialized with FreeBSD's default flags
# trusts every local connection; this replaces that.
hba="$pgdata/pg_hba.conf"
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
	service postgresql reload
fi

# Unix socket only. ALTER SYSTEM writes postgresql.auto.conf and leaves
# postgresql.conf untouched; the setting needs a restart.
if [ -n "$(echo 'SHOW listen_addresses' | pg_sql postgres)" ]; then
	log "disabling the PostgreSQL TCP listener"
	pg_sql postgres <<'SQL'
ALTER SYSTEM SET listen_addresses = '';
ALTER SYSTEM SET unix_socket_directories = '/tmp';
SQL
	service postgresql restart
	if [ -n "$(echo 'SHOW listen_addresses' | pg_sql postgres)" ]; then
		log "WARNING: listen_addresses is still set, from a source that overrides postgresql.auto.conf (postgresql_flags?); PostgreSQL still listens on TCP"
	fi
fi

have_role=$(echo "SELECT 1 FROM pg_roles WHERE rolname = 'chess'" | pg_sql postgres)
have_db=$(echo "SELECT 1 FROM pg_database WHERE datname = 'chess'" | pg_sql postgres)
case "$have_role$have_db" in
11) log "role and database chess exist; provisioning unchanged" ;;
"")
	log "provisioning role, database, and schema (split privileges: $SPLIT_PRIVILEGES)"
	split=false
	[ "$SPLIT_PRIVILEGES" = yes ] && split=true
	su -m postgres -c "psql -X -q -d postgres -v split=$split -f -" <"$repo/deploy/postgresql/setup.sql"
	;;
*) die "only one of role chess and database chess exists; inspect with: su -m postgres -c psql" ;;
esac

# --- chessd --------------------------------------------------------------------
if service chessd onestatus >/dev/null 2>&1; then
	log "stopping chessd"
	service chessd onestop
fi

bindir=$(dirname "$bin")
[ -d "$bindir" ] || install -d -o root -g wheel -m 0755 "$bindir"
if [ -f "$bin" ] && ! cmp -s "$CHESS_BINARY" "$bin"; then
	log "keeping the previous binary as $bin.prev"
	cp -p "$bin" "$bin.prev"
fi
# Root-owned: the service account cannot replace its own executable file.
install -o root -g wheel -m 0555 "$CHESS_BINARY" "$bin"

[ -d "$RC_DIR" ] || install -d "$RC_DIR"
if [ -f "$RC_DIR/chessd" ] && ! cmp -s "$here/rc.d/chessd" "$RC_DIR/chessd"; then
	# Saved outside rc.d: rc(8) would run a second copy found there.
	[ -d "$RC_BACKUP_DIR" ] || install -d -m 0750 "$RC_BACKUP_DIR"
	saved="$RC_BACKUP_DIR/chessd.rc.$(date -u +%Y%m%dT%H%M%SZ)"
	cp -p "$RC_DIR/chessd" "$saved"
	log "previous rc.d script saved as $saved"
fi
install -o root -g wheel -m 0555 "$here/rc.d/chessd" "$RC_DIR/chessd"

if [ ! -f "$key" ]; then
	log "generating JWT signing key $key"
	(umask 077 && openssl rand -base64 48 >"$key")
fi
chown "$user:$group" "$key"
chmod 600 "$key"

log "creating or migrating the chess schema"
if [ "$SPLIT_PRIVILEGES" = yes ]; then
	# The postgres account migrates as chess_owner. It may be unable to reach
	# the chess home, so it runs a temporary copy of the binary.
	migrator=$(mktemp /tmp/chess-migrate.XXXXXX)
	install -o root -g wheel -m 0555 "$CHESS_BINARY" "$migrator"
	su -m postgres -c "$migrator db init -dsn \"dbname=chess user=postgres options='-c role=chess_owner -c search_path=chess'\"" ||
		{ rm -f "$migrator"; die "schema migration failed"; }
	rm -f "$migrator"
else
	su -m "$user" -c "$bin db init -dsn '$dsn'"
fi

sysrc -q chessd_enable=YES >/dev/null
if [ -n "$TRUSTED_PROXIES" ]; then
	sysrc -q chessd_trusted_proxies="$TRUSTED_PROXIES" >/dev/null
fi
[ -n "$(rc_value chessd_trusted_proxies "")" ] ||
	log "WARNING: chessd_trusted_proxies is unset; behind nginx every client shares one rate limit"
# Settings of the SQLite-era rc.d script that nothing reads any more.
for stale in chessd_storage_path chessd_dir; do
	if sysrc -n "$stale" >/dev/null 2>&1; then
		sysrc -q -x "$stale" >/dev/null
		log "removed unused rc.conf setting $stale"
	fi
done

# --- optional maintenance -------------------------------------------------------------
logs=$(rc_value chessd_logs /var/log/chessd.log)
if [ "$ENABLE_LOG_ROTATION" = yes ]; then
	# Daily or at 1 MB, keep 7; SIGHUP makes daemon(8) (-H) reopen the log.
	[ -d "$NEWSYSLOG_DIR" ] || install -d "$NEWSYSLOG_DIR"
	printf '%s\n' \
		"# Managed by chess setup-jail.sh" \
		"$logs	$user:$group	640	7	1000	@T00	JC	/var/run/chessd.pid	1" \
		>"$NEWSYSLOG_DIR/chessd.conf"
	log "log rotation: $NEWSYSLOG_DIR/chessd.conf"
fi
if [ "$ENABLE_BACKUP" = yes ]; then
	# Nightly pg_dump at 03:30 as postgres, keeping 14 days.
	[ -d "$SBIN_DIR" ] || install -d "$SBIN_DIR"
	install -o root -g wheel -m 0555 "$here/chess-backup.sh" "$SBIN_DIR/chess-backup"
	[ -d "$BACKUP_DIR" ] || install -d -o postgres -g postgres -m 0700 "$BACKUP_DIR"
	[ -d "$CRON_DIR" ] || install -d "$CRON_DIR"
	printf '%s\n' \
		"# Managed by chess setup-jail.sh" \
		"SHELL=/bin/sh" \
		"PATH=/sbin:/bin:/usr/sbin:/usr/bin:/usr/local/sbin:/usr/local/bin" \
		"30	3	*	*	*	postgres	CHESS_BACKUP_DIR=$BACKUP_DIR $SBIN_DIR/chess-backup" \
		>"$CRON_DIR/chess-backup"
	log "backups: $CRON_DIR/chess-backup -> $BACKUP_DIR"
fi

# --- start and verify -----------------------------------------------------------------
log "starting chessd"
service chessd start
health_host=$host
[ "$health_host" = 0.0.0.0 ] && health_host=127.0.0.1
url="http://$health_host:$port/health"
health=""
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
	if health=$(fetch -q -T 2 -o - "$url" 2>/dev/null); then
		break
	fi
	sleep 2
done
case $health in
*'"storage":"ok"'*) log "healthy: $health" ;;
*) die "no healthy response from $url (got: ${health:-nothing}); see $logs, and check that the jail firewall allows loopback" ;;
esac

cat <<EOF

chessd is running with PostgreSQL storage ($url).

Administration inside the jail:
    su -m $user -c '$bin db user list -dsn "$dsn"'
    service chessd status|restart
    tail -f $logs
EOF
