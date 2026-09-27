# Deployment: FreeBSD Jail with PostgreSQL 18

Production layout: host nginx terminates TLS and reverse-proxies `/chess/` to
`chess-server` in a Bastille jail. PostgreSQL 18 runs in the same jail and
listens **only on its Unix socket**. The `chess` OS account has no shell or
password; its database role reaches only the `chess` database through peer
authentication, so no database password exists.

```
internet ──TLS──▶ host nginx ──HTTP──▶ jail: chess-server (API port)
                                              │ Unix socket, peer auth
                                              ▼
                                       jail: PostgreSQL 18 (no TCP listener)
```

`host#` runs on the Bastille host as root; `jail#` runs inside the jail as root
(`bastille console <jail>`).

## Scripted Deployment

The scripts in [`deploy/`](../deploy) perform every step below and are safe to
rerun for upgrades. Their header comments list all inputs.

1. **Host:** give the jail its own System V shared-memory namespace, which
   PostgreSQL needs, and restart it:

   ```sh
   host# sh deploy/freebsd/host.sh chess
   ```

2. **Build** the static server on any machine with Go 1.26+, then copy the
   binary and a checkout of this repository into the jail:

   ```sh
   make server-freebsd          # bin/chess-server-freebsd-amd64
   ```

3. **Jail:** install and configure everything. `OLD_SERVICE` names the rc.d
   service of the SQLite deployment; `SQLITE_DB` imports its data once.

   ```sh
   jail# CHESS_BINARY=/tmp/chess-server-freebsd-amd64 \
         API_HOST=10.17.89.10 TRUSTED_PROXIES=10.17.89.1 \
         OLD_SERVICE=chess SQLITE_DB=/usr/local/chess/db/chess.db \
         sh deploy/freebsd/setup-jail.sh
   ```

   It ends with a health check that requires `"storage":"ok"`.

4. **Host:** add the location block from
   [`deploy/freebsd/nginx-chess.conf`](../deploy/freebsd/nginx-chess.conf) to
   the TLS server block, then `nginx -t && service nginx reload`.

5. **Verify** with the checklist at the end of this document. Keep the old
   SQLite file until you are satisfied; the import does not modify it.

`setup-jail.sh` inputs:

| Variable | Default | Meaning |
|---|---|---|
| `CHESS_BINARY` | required | New `chess-server` binary |
| `API_HOST` | required | Listen address (the jail IP) |
| `API_PORT` | `8080` | Listen port |
| `TRUSTED_PROXIES` | empty (warns) | nginx address(es) as seen from the jail |
| `CHESS_HOME` | account home, else `/usr/local/chess` | Binary and JWT key location |
| `LOG_DIR` | `/var/log/chess` | Server log directory |
| `SQLITE_DB` | none | v0.11 SQLite database to import into an empty schema |
| `OLD_SERVICE` | none | Previous rc.d service to stop and disable |
| `SPLIT_PRIVILEGES` | `no` | `yes` for a DML-only runtime role (below) |
| `EXTRA_FLAGS` | none | More server flags, e.g. `-max-users 500` |
| `PGDATA` | `/var/db/postgres/data18` | PostgreSQL data directory |

What the script changes, in order:

| Step | Result |
|---|---|
| Packages | `postgresql18-server`, `postgresql18-client`, `stockfish` (and `sqlite3` for an import) |
| PostgreSQL | `initdb` with UTF-8, builtin `C.UTF-8` locale, peer local auth, host auth rejected; data checksums are on by default in 18 |
| `pg_hba.conf` | Only `postgres` (all databases) and `chess` (database `chess`), both `local` + `peer`; original kept as `pg_hba.conf.orig` |
| `postgresql.auto.conf` | `listen_addresses = ''` (no TCP), socket in `/tmp`, `scram-sha-256` |
| Provisioning | [`deploy/postgresql/setup.sql`](../deploy/postgresql/setup.sql) on first run only |
| Account | `chess` created if missing (`nologin`, no password); a warning if an existing account has a login shell |
| Files | Binary `root:wheel 0555` (previous copy kept as `.prev`); `jwt.key` generated once, `chess:chess 0600`; log directory `chess:chess 0750` |
| Schema | `chess-server db init` (as `chess`, or as `chess_owner` in split mode) |
| Import | [`migrate-sqlite.sh`](../deploy/postgresql/migrate-sqlite.sh) when `SQLITE_DB` is set and the schema is empty |
| Service | [`rc.d/chess_server`](../deploy/freebsd/rc.d/chess_server) and `rc.conf` via `sysrc` |
| Log rotation | `/usr/local/etc/newsyslog.conf.d/chess_server.conf`: daily or 1 MB, 7 kept |
| Backups | `/usr/local/sbin/chess-backup` via `/usr/local/etc/cron.d/chess-backup`: `pg_dump` at 03:30 as `postgres`, 14 days kept |

The script was exercised on Linux with FreeBSD-only commands (`pkg`, `sysrc`,
`service`, `pw`, `fetch`) stubbed and everything else real: PostgreSQL 18.6,
`su`, provisioning in both privilege modes, schema migration, the SQLite
import, and the health check of the running server.

## Accounts and Data Retention

- Accounts created by registration on the site and by `chess-server db user
  add` are identical and never expire. Public registration closes at
  `-max-users` accounts (default 100; `0` removes the limit); the CLI is not
  limited.
- A game belongs to a registered user when they created it while signed in or
  claimed a slot with their first move. Such games are kept indefinitely and
  record the player's username at claim time.
- Games with no registered player are **deleted 24 hours after their last
  activity** (creation, move, undo, reconfiguration, or result), from memory and
  from the database, by the hourly cleanup. `-anonymous-game-ttl` changes the
  window; `0` keeps them.
- Deleting a user removes their sessions; their games keep the claim and name.

## Reference

The sections below describe what the scripts configure, for review or manual
installation.

### Jail prerequisites

PostgreSQL allocates a small System V shared-memory segment even though its
main buffers use `mmap`. `host.sh` runs:

```sh
host# bastille config chess set sysvshm new
host# bastille restart chess
```

`sysvshm=new` gives the jail an isolated namespace, unlike the older shared
`allow.sysvipc`. Without it, `initdb` fails with `could not create shared
memory segment`.

### PostgreSQL

```sh
jail# pkg install postgresql18-server postgresql18-client stockfish
jail# sysrc postgresql_enable=YES
jail# sysrc postgresql_initdb_flags="--encoding=UTF8 --locale-provider=builtin --builtin-locale=C.UTF-8 --auth-local=peer --auth-host=reject"
jail# service postgresql initdb
jail# service postgresql start
jail# su -m postgres -c "psql -X -d postgres" <<'SQL'
ALTER SYSTEM SET listen_addresses = '';
ALTER SYSTEM SET unix_socket_directories = '/tmp';
ALTER SYSTEM SET password_encryption = 'scram-sha-256';
SQL
jail# service postgresql restart
```

`/var/db/postgres/data18/pg_hba.conf`:

```
# TYPE  DATABASE  USER      METHOD
local   all       postgres  peer
local   chess     chess     peer
```

### Provisioning and privilege models

[`setup.sql`](../deploy/postgresql/setup.sql) creates the login role `chess`
(no superuser, create-db, create-role, replication, or RLS bypass; 20
connections), the `chess` database (`template0`, builtin `C.UTF-8`), and the
`chess` schema. It revokes `PUBLIC` access to the database and to schema
`public`, and sets role defaults for `search_path`, `statement_timeout`,
`lock_timeout`, and `idle_in_transaction_session_timeout`.

```sh
jail# su -m postgres -c 'psql -X -d postgres -f -' < deploy/postgresql/setup.sql
```

| Mode | Schema owner | `chess` role can | Schema migrations |
|---|---|---|---|
| Owner (default) | `chess` | DML and DDL in schema `chess` only | Automatic at server start |
| Split (`-v split=true`, `SPLIT_PRIVILEGES=yes`) | `chess_owner` (NOLOGIN) | `SELECT/INSERT/UPDATE/DELETE` only | `setup-jail.sh`, or manually as `postgres` (below) |

```sh
jail# su -m postgres -c "/usr/local/chess/chess-server db init \
    -dsn \"dbname=chess user=postgres options='-c role=chess_owner -c search_path=chess'\""
```

In split mode the server refuses to start while a migration is pending.

### The `chess` account

| Resource | Requirement |
|---|---|
| Shell/password | `/usr/sbin/nologin`, password disabled |
| Home | Holds `chess-server` (`root:wheel 0555`) and `jwt.key` |
| JWT key | `chess:chess 0600`, at least 32 bytes; the server refuses group- or world-readable keys |
| Log directory | `chess:chess 0750` |
| Database | Role `chess` over `/tmp/.s.PGSQL.5432`, database `chess` only |
| Network | Listens on the API port (unprivileged, above 1024) |
| Engine | `stockfish` in `PATH` (`/usr/local/bin`; set through `chess_server_env`) |

Administrative CLI commands run as `chess` so peer authentication selects the
right role; root may use `su -m` although the account has no shell:

```sh
jail# su -m chess -c '/usr/local/chess/chess-server db user list -dsn "postgres:///chess?host=/tmp"'
```

### JWT signing key

`setup-jail.sh` generates the key once with `openssl rand -base64 48`. Without
a key file, production mode generates a new key at every start and every
browser session ends on restart. To rotate, replace the file and restart the
service; users then sign in again.

### Service

`rc.conf` as written by `setup-jail.sh`:

```sh
chess_server_enable="YES"
chess_server_account="chess"
chess_server_binary="/usr/local/chess/chess-server"
chess_server_logfile="/var/log/chess/chess-server.log"
chess_server_env="PATH=/usr/local/bin:/usr/bin:/bin"
chess_server_flags="-api-host 10.17.89.10 -api-port 8080 -dsn postgres:///chess?host=/tmp -jwt-secret-file /usr/local/chess/jwt.key -log-level info -trusted-proxies 10.17.89.1"
```

The rc.d script starts `daemon(8)` as root, which writes the supervisor
pidfile, drops to `chess`, restarts the server if it exits, and reopens the log
on SIGHUP for newsyslog. It declares `REQUIRE: postgresql`. The SQLite-era
`-storage-path` flag no longer exists; a server started with it exits with
`flag provided but not defined`.

| Flag | Environment default | Purpose |
|---|---|---|
| `-dsn` | `CHESS_DSN` | PostgreSQL connection string; empty disables persistence |
| `-jwt-secret-file` | `CHESS_JWT_SECRET_FILE` | Stable JWT signing key |
| `-trusted-proxies` | | nginx address(es) as seen from the jail |
| `-proxy-header` | | Client-IP header set by nginx (default `X-Real-IP`) |
| `-max-users` | | Registration cap (default 100, `0` = none) |
| `-anonymous-game-ttl` | | Retention of games without a registered player (default `24h`) |
| `-finished-game-ttl` | | Memory retention of finished games (default `1h`) |

### Host nginx

[`nginx-chess.conf`](../deploy/freebsd/nginx-chess.conf) passes the client
address in `X-Real-IP` (which nginx overwrites), allows 30-second long-polls,
and bounds request bodies. The server keys rate limits on `X-Real-IP` only for
connections from `-trusted-proxies`; any other peer is keyed by its own
address. Without `-trusted-proxies`, every proxied request shares one bucket.
The existing security headers (HSTS, `X-Frame-Options`, CSP with
`connect-src 'self'`) are unaffected: the API is same-origin under `/chess/`,
and this release does not change the web client.

### SQLite import

[`migrate-sqlite.sh`](../deploy/postgresql/migrate-sqlite.sh) reads a
schema-version-2 SQLite database (v0.11) and imports, in one transaction, all
users as regular accounts with their Argon2id hashes, unexpired sessions,
games with claims and player names, results, and moves. It refuses a schema
that already has data. Imported games without a registered player follow the
24-hour retention. Sessions carry over, but tokens signed with the old
per-restart key do not, so users sign in once. Manual invocation:

```sh
jail# su -m postgres -c 'sh /path/to/migrate-sqlite.sh /tmp/legacy.db dbname=chess'
```

### Upgrades, backups, and restores

- **Upgrade:** build the new binary and rerun `setup-jail.sh` with the same
  inputs (omit `SQLITE_DB`). It keeps the previous binary as
  `chess-server.prev`, migrates the schema, and restarts the service. To roll
  back a release without a schema change, copy `.prev` back and restart.
- **Migrations** are transactional and serialized by an advisory lock. A
  server refuses to start against a schema newer than it supports.
- **Backups:** `chess-backup` writes `pg_dump --format=custom` files to
  `/var/db/postgres/backups` nightly and removes those older than 14 days. Copy
  them off the jail.
- **Restore** into a freshly provisioned database:
  `su -m postgres -c 'pg_restore --no-owner --role=chess -d chess <dump>'`
  (`--role=chess_owner` in split mode).
- **PostgreSQL restarts:** the server retries in-flight writes across a brief
  outage. A write that still fails marks storage `degraded` in `/health`; live
  games continue in memory, but durable history stops until the server is
  restarted.

### Verification checklist

```sh
jail# su -m chess -c 'psql -X -d chess -Atc "SHOW search_path"'     # chess
jail# su -m chess -c 'psql -X -d postgres -c "select 1"'            # rejected by pg_hba
jail# sockstat -4 -6 -l | grep postgres                             # no TCP listener
jail# service chess_server status
jail# fetch -qo - http://10.17.89.10:8080/health                    # "storage":"ok"
host# curl -s https://lixen.com/chess/health                        # through nginx
```
