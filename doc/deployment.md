# Deployment: FreeBSD Jail with PostgreSQL 18

This guide covers the production layout: host nginx terminates TLS and
reverse-proxies to `chess-server` in a Bastille jail; PostgreSQL 18 runs in the
same jail and listens **only on its Unix socket**. The `chess` OS account has no
shell or password, and its database role can reach only the `chess` database.

```
internet ──TLS──▶ host nginx ──HTTP──▶ jail: chess-server (API port)
                                              │ Unix socket, peer auth
                                              ▼
                                       jail: PostgreSQL 18 (no TCP listener)
```

Commands prefixed with `jail#` run inside the jail as root
(`bastille console chess`); `host#` runs on the host.

## 1. Jail prerequisites

PostgreSQL allocates a small System V shared-memory segment even though its
main buffers use `mmap`. Give the jail its own SysV namespace (preferred over
the older, shared `allow.sysvipc`):

```sh
host# bastille config chess set sysvshm new
host# bastille restart chess
```

Without it, `initdb` fails with `could not create shared memory segment`.

## 2. Install and initialize PostgreSQL

```sh
jail# pkg install postgresql18-server postgresql18-client stockfish
jail# sysrc postgresql_enable=YES
jail# sysrc postgresql_initdb_flags="--encoding=UTF8 --locale-provider=builtin --builtin-locale=C.UTF-8 --auth-local=peer --auth-host=reject"
jail# service postgresql initdb
```

PostgreSQL 18 enables data checksums by default. The builtin `C.UTF-8` locale
provider gives case mapping and ordering that do not change with OS library
upgrades. The data directory is `/var/db/postgres/data18`.

Edit `/var/db/postgres/data18/postgresql.conf`:

```ini
listen_addresses = ''              # Unix socket only; nothing listens on TCP
unix_socket_directories = '/tmp'   # FreeBSD default; chess-server finds it here
password_encryption = scram-sha-256
log_connections = on               # optional audit trail
```

Replace the rules in `/var/db/postgres/data18/pg_hba.conf` so each OS account
reaches only its own database through the socket:

```
# TYPE  DATABASE  USER      METHOD
local   all       postgres  peer
local   chess     chess     peer
```

There are no `host` lines. Peer authentication maps the connecting OS user to
the role of the same name, so no database password exists to store or leak.

```sh
jail# service postgresql start
```

## 3. Provision the database

Run [`deploy/postgresql/setup.sql`](../deploy/postgresql/setup.sql) once as the
superuser. It creates the login role `chess` (no superuser, create-db,
create-role, replication, or RLS bypass; 20 connections), the `chess` database
(`template0`, builtin `C.UTF-8`), and the `chess` schema. It revokes `PUBLIC`
access to the database and to schema `public`, and sets role defaults for
`search_path`, `statement_timeout`, `lock_timeout`, and
`idle_in_transaction_session_timeout`.

```sh
jail# su -m postgres -c 'psql -X -d postgres -f /path/to/setup.sql'
```

Choose a privilege model:

| Mode | Schema owner | `chess` role can | Schema migrations |
|---|---|---|---|
| Owner (default) | `chess` | DML and DDL in schema `chess` only | Automatic at server start |
| Split (`-v split=true`) | `chess_owner` (NOLOGIN) | `SELECT/INSERT/UPDATE/DELETE` only | Administrator runs `db init` as `chess_owner` |

In split mode, migrate before starting a new server version:

```sh
jail# su -m postgres -c "/usr/local/chess/chess-server db init \
    -dsn \"dbname=chess user=postgres options='-c role=chess_owner -c search_path=chess'\""
```

A runtime role limited to DML starts normally while the schema is current; the
server refuses to start if a migration is pending.

## 4. The `chess` OS account

The account already exists as an unprivileged service user. Its requirements:

| Resource | Requirement |
|---|---|
| Shell/password | `/usr/sbin/nologin`, no password (`pw usermod chess -s /usr/sbin/nologin -w no`) |
| Home | Holds `chess-server` (mode 0755, owned by root) and the JWT key |
| JWT key | `~chess/jwt.key`, owner `chess`, mode 0600 |
| Log directory | Writable by `chess` (for example `/var/log/chess`, 0750) |
| Database | Connects as role `chess` over `/tmp/.s.PGSQL.5432`; nothing else |
| Network | Listens on the API port (unprivileged, above 1024) |
| Engine | `stockfish` resolvable in `PATH` (`/usr/local/bin`) |

`chess-server` is statically linked and needs no C libraries or CGO. Build it on
any host:

```sh
make server-freebsd     # bin/chess-server-freebsd-amd64
```

Run administrative CLI commands as `chess` so peer authentication selects the
right role (root may use `su -m` even though the account has no shell):

```sh
jail# su -m chess -c '/usr/local/chess/chess-server db user list -dsn dbname=chess'
jail# su -m chess -c '/usr/local/chess/chess-server db user promote -dsn dbname=chess -username alice'
```

## 5. JWT signing key

Without a key file, production mode generates a new signing key at every start,
which invalidates every browser session on restart. Provision a stable key:

```sh
jail# openssl rand -base64 48 > /usr/local/chess/jwt.key
jail# chown chess:chess /usr/local/chess/jwt.key
jail# chmod 600 /usr/local/chess/jwt.key
```

The server refuses a key file that is group- or world-accessible, not a regular
file, or shorter than 32 bytes. To rotate, replace the file and restart; every
existing token then fails validation and users sign in again.

## 6. Service configuration

Flags relevant to this deployment (see `chess-server -h`):

| Flag | Environment default | Purpose |
|---|---|---|
| `-dsn` | `CHESS_DSN` | PostgreSQL connection string; empty disables persistence |
| `-jwt-secret-file` | `CHESS_JWT_SECRET_FILE` | Stable JWT signing key |
| `-trusted-proxies` | | nginx address(es) as seen from the jail |
| `-proxy-header` | | Client-IP header set by nginx (default `X-Real-IP`) |
| `-api-host`, `-api-port` | | Listen address |

`dbname=chess` is enough as a DSN: the OS user supplies the role and the socket
directory `/tmp` is found automatically. The space-free URL form
`postgres:///chess?host=/tmp` is equivalent and avoids quoting in `rc.conf`.

Example `rc.conf` entries for a `daemon(8)`-based `rc.d` script named
`chess_server`:

```sh
chess_server_enable="YES"
chess_server_env="PATH=/usr/local/bin:/usr/bin:/bin"
chess_server_flags="-api-host 10.17.89.10 -api-port 8080 -dsn postgres:///chess?host=/tmp -jwt-secret-file /usr/local/chess/jwt.key -trusted-proxies 10.17.89.1 -log-level info"
```

`rc.d` starts services with a minimal `PATH`; include `/usr/local/bin` or the
engine will not start. Declare `# REQUIRE: postgresql` in the script so the
database starts first. A minimal script, if you do not already have one:

```sh
#!/bin/sh
# PROVIDE: chess_server
# REQUIRE: LOGIN postgresql
# KEYWORD: shutdown
. /etc/rc.subr

name=chess_server
rcvar=chess_server_enable
load_rc_config $name

: ${chess_server_enable:="NO"}
: ${chess_server_account:="chess"}
: ${chess_server_binary:="/usr/local/chess/chess-server"}
: ${chess_server_logfile:="/var/log/chess/chess-server.log"}

# daemon(8) starts as root, writes the pidfile, and drops to the account with
# -u. Do not set chess_server_user: rc.subr would su before daemon runs.
# -P records the supervisor, so stop also ends the -r restart loop.
pidfile="/var/run/${name}.pid"
procname="daemon"
command="/usr/sbin/daemon"
command_args="-f -r -R 5 -H -P ${pidfile} -o ${chess_server_logfile} -u ${chess_server_account} ${chess_server_binary} ${chess_server_flags}"

run_rc_command "$1"
```

Rename `-storage-path` in an existing script: the SQLite flag no longer exists
and the server exits with `flag provided but not defined`.

## 7. Host nginx

Pass the client address in a header nginx overwrites, and allow for 30-second
long-polls:

```nginx
location /chess/ {
    proxy_pass http://10.17.89.10:8080/;
    proxy_http_version 1.1;
    proxy_set_header Connection "";
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_read_timeout 45s;
}
```

Set `-trusted-proxies` to the address nginx uses to reach the jail. The server
then keys rate limits on `X-Real-IP` only for connections from that address;
any other peer is keyed by its own address, so a client cannot choose its key.
Without `-trusted-proxies`, every proxied request shares one rate-limit bucket
(the startup log says which mode is active). Do not use `X-Forwarded-For`
here: its first entry comes from the client.

The existing security headers (HSTS, `X-Frame-Options`, CSP with
`connect-src 'self'`) are unaffected: the API is same-origin under `/chess/`,
and this release does not change the web client.

## 8. Migrating existing SQLite data

The import script needs `sqlite3` and `psql` (`pkg install sqlite3`) and reads a
schema-version-2 database (v0.11). It imports users (with their Argon2id
hashes), unexpired sessions, games, claims, results, and moves in one
transaction, and refuses a target that already has data.

```sh
jail# service chess_server stop                   # checkpoints the SQLite WAL
jail# cp /usr/local/chess/db/chess.db /tmp/legacy.db && chmod 644 /tmp/legacy.db
jail# su -m chess -c '/usr/local/chess/chess-server db init -dsn dbname=chess'   # owner mode
jail# su -m postgres -c 'sh /path/to/migrate-sqlite.sh /tmp/legacy.db dbname=chess'
imported: 4 users (0 dropped), 3 sessions (0 expired or orphaned), 5 games, 10 moves (0 orphaned)
jail# rm /tmp/legacy.db
```

`CHESS_SCHEMA` selects a schema other than `chess`. Keep the old SQLite file
until the new deployment is verified; the import does not modify it. Sessions
carry over, but tokens signed with the old per-restart key do not, so users sign
in once after the switch.

## 9. Upgrades, backups, and restores

- **Schema migrations** are transactional and serialized by an advisory lock.
  A server refuses to start against a schema newer than it supports.
- **Backups:** a logical dump is small and consistent while the server runs:

  ```sh
  jail# su -m postgres -c 'pg_dump -Fc -d chess -f /var/db/postgres/backups/chess-$(date +%F).dump'
  ```

  Schedule it from the `postgres` crontab and copy dumps off the jail.
- **Restore** into a freshly provisioned database (section 3), then
  `pg_restore --no-owner --role=chess -d chess <dump>` as `postgres`
  (`--role=chess_owner` in split mode).
- **PostgreSQL restarts:** the server retries in-flight writes across a brief
  outage. A write that still fails marks storage `degraded` in `/health`; live
  games continue in memory, but durable history stops until the server is
  restarted.

## 10. Verification checklist

```sh
jail# su -m chess -c 'psql -X -d chess -Atc "SHOW search_path"'     # chess
jail# su -m chess -c 'psql -X -d postgres -c "select 1"'            # rejected by pg_hba
jail# sockstat -4 -6 -l | grep postgres                             # no TCP listener
jail# curl -s http://10.17.89.10:8080/health                        # "storage":"ok"
```
