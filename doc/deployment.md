# Deployment: FreeBSD Jail with PostgreSQL 18

Layout: host nginx terminates TLS and proxies `/chess/api/` and `/chess/health`
to `chess-server` in a Bastille jail. The rc.d service is `chessd`, running as
the unprivileged `chess` account (no shell, no password). PostgreSQL 18 runs in
the same jail and listens **only on its Unix socket**. The `chess` role reaches
only the `chess` database through peer authentication, so no database password
exists.

```
browser ──TLS──▶ host nginx ──HTTP──▶ jail: chessd (chess-server, API port)
                                            │ Unix socket, peer auth
                                            ▼
                                     jail: PostgreSQL 18 (no TCP listener)
```

`host#` runs on the Bastille host as root; `jail#` runs inside the jail as root
(`bastille console <jail>`). Addresses in this document are placeholders.
For a native Linux install with systemd, see
[deployment-linux.md](deployment-linux.md); database recipes are in
[database.md](database.md).

## Scripted Installation and Upgrade

The scripts in [`deploy/`](../deploy) are idempotent; rerun them to upgrade.
Their header comments list every input.

1. **Host, once:** give the jail its own System V shared-memory namespace,
   which PostgreSQL needs, and restart it:

   ```sh
   host# sh deploy/freebsd/host.sh <jail>
   ```

2. **Jail, once:** install the packages:

   ```sh
   jail# pkg install postgresql18-server postgresql18-client stockfish
   ```

3. **Build** the release (FreeBSD `make` or GNU make) and copy the server
   binary and a checkout of this repository into the jail:

   ```sh
   make server-freebsd          # bin/chess-server-freebsd-amd64
   ```

4. **Jail:** install or upgrade the service. `TRUSTED_PROXIES` is the address
   the host nginx connects from, as seen inside the jail.

   ```sh
   jail# CHESS_BINARY=/tmp/chess-server-freebsd-amd64 \
         TRUSTED_PROXIES=<nginx-address> \
         ENABLE_LOG_ROTATION=yes ENABLE_BACKUP=yes \
         sh deploy/freebsd/setup-jail.sh
   ```

   The script stops `chessd`, installs everything, starts it again, and fails
   unless `/health` reports `"storage":"ok"`.

5. **Host:** point nginx at the jail
   ([`deploy/nginx-chess.conf`](../deploy/nginx-chess.conf))
   and publish the web client (below).

`setup-jail.sh` inputs:

| Variable | Default | Meaning |
|---|---|---|
| `CHESS_BINARY` | required | `chess-server` binary to install |
| `TRUSTED_PROXIES` | unset (warns) | Stored as `chessd_trusted_proxies` in `rc.conf` |
| `SPLIT_PRIVILEGES` | `no` | `yes` for a DML-only runtime role (below) |
| `ENABLE_LOG_ROTATION` | `no` | newsyslog entry for the service log |
| `ENABLE_BACKUP` | `no` | Nightly `pg_dump` via cron |

The service layout (account, home, binary, log, listen address, DSN, key)
comes from `rc.conf` `chessd_*` settings, with the defaults of
[`rc.d/chessd`](../deploy/freebsd/rc.d/chessd): account `chess`, home
`/home/chess`, binary `~/bin/chess-server`, log `/var/log/chessd.log`,
`0.0.0.0:8080`, DSN `postgres:///chess?host=/tmp`, key `~/jwt.key`.

What the script changes, in order:

| Step | Result |
|---|---|
| Preflight | Requires root, PostgreSQL 17+ installed, `chessd_user=chess`; refuses an `rc.conf` `chessd_flags` that still contains `-storage-path`; warns if `stockfish` is missing |
| Cluster | `initdb` only when none exists: UTF-8, builtin `C.UTF-8` locale, peer local auth, host auth rejected |
| `pg_hba.conf` | Only `postgres` (all databases) and `chess` (database `chess`), both `local` + `peer`; original kept as `pg_hba.conf.orig`. This replaces the trust-everything default of FreeBSD's standard `initdb` flags |
| Listener | `ALTER SYSTEM SET listen_addresses = ''` (no TCP) and a restart, only when TCP is still enabled |
| Provisioning | [`setup.sql`](../deploy/postgresql/setup.sql) when neither role nor database `chess` exists |
| Service stop | `service chessd onestop` through the installed rc.d script |
| Binary | `root:wheel 0555`; the previous file kept as `chess-server.prev` |
| rc.d | [`rc.d/chessd`](../deploy/freebsd/rc.d/chessd) installed; a differing previous copy saved as `/var/backups/chessd.rc.<time>` (not inside `rc.d`, where rc(8) would run it) |
| JWT key | `~/jwt.key`, `chess:chess 0600`, generated once |
| Schema | `chess-server db init` (as `chess`, or as `chess_owner` in split mode) |
| `rc.conf` | `chessd_enable=YES`, `chessd_trusted_proxies` when given; removes the unused `chessd_storage_path` and `chessd_dir` |
| Log rotation (opt-in) | `/usr/local/etc/newsyslog.conf.d/chessd.conf`: daily or 1 MB, 7 kept, SIGHUP to `daemon(8)` |
| Backups (opt-in) | `/usr/local/sbin/chess-backup` via `/usr/local/etc/cron.d/chess-backup`: `pg_dump` at 03:30 as `postgres`, 14 days kept in `/var/db/postgres/backups` |
| Start | `service chessd start`, then `/health` on the configured port |

Files of an earlier SQLite release are not touched; delete them once the new
release is verified.

The script and rc.d script were exercised on Linux with the FreeBSD-only
commands (`rc.subr`, `daemon`, `service`, `sysrc`, `pw`, `fetch`) stubbed and
everything else real (PostgreSQL 18.6, `su`, both server generations). The
tested runs were an upgrade from a running v0.11 SQLite `chessd`, an idempotent
rerun, and a fresh split-privilege install.

## Accounts and Data Retention

A new database has no accounts. Create the first one (for example your own)
with the CLI; without `-password` it prompts, so the password stays out of
the shell history and the process list:

```sh
jail# su -m chess -c '/home/chess/bin/chess-server db user add -username <name> -dsn "postgres:///chess?host=/tmp"'
```

The row lands in `chess.users` with an Argon2id hash in `password_hash`; the
password itself is stored nowhere and cannot be read back, only replaced
(`db user set-password`). The tables are in schema `chess`, not `public`:
as `postgres`, list them with `\dt chess.*` and name them as `chess.games`.
[database.md](database.md) explains the layout and has the psql recipes
(inspecting and deleting games, sessions, starting fresh).

- Accounts created by registration on the site and by `chess-server db user
  add` are identical and never expire. Public registration closes at
  `-max-users` accounts (default 100; `0` removes the limit); the CLI is not
  limited.
- A game belongs to a registered user when they created it while signed in or
  claimed a slot with their first move. Such games are kept indefinitely and
  record the player's username at claim time.
- Games with no registered player are **deleted 24 hours after their last
  activity**, from memory and from the database, by the hourly cleanup.
  `-anonymous-game-ttl` changes the window; `0` keeps them.
- Deleting a user removes their sessions; their games keep the claim and name.
  With `-db-cleanup delete`, games left without any registered player are
  deleted like anonymous ones.
- A game row deleted by hand while the server still plays it no longer
  degrades storage: the server unloads that game and carries on.

## Service (`rc.d/chessd`)

`rc.conf` settings, with defaults:

| Setting | Default | Purpose |
|---|---|---|
| `chessd_enable` | `NO` | Start at boot |
| `chessd_user`, `chessd_group` | `chess` | Service account; must stay `chess` for peer authentication |
| `chessd_home` | `/home/chess` | Account home |
| `chessd_bin` | `${chessd_home}/bin/chess-server` | Server binary |
| `chessd_logs` | `/var/log/chessd.log` | Log file |
| `chessd_host`, `chessd_port` | `0.0.0.0`, `8080` | Listen address |
| `chessd_dsn` | `postgres:///chess?host=/tmp` | Database `chess` over the socket as the OS user |
| `chessd_jwt_key` | `${chessd_home}/jwt.key` | JWT signing key; created (0600) on first start if missing |
| `chessd_trusted_proxies` | empty | Proxy address(es) whose `X-Real-IP` is trusted |
| `chessd_flags` | empty | Extra flags, e.g. `-max-users 500 -db-cleanup report` |

The script runs `daemon(8)` as root, which writes the supervisor pidfile
(`/var/run/chessd.pid`), drops to `chess`, restarts the server 5 seconds after
an unexpected exit, and reopens the log on SIGHUP. It declares `REQUIRE:
postgresql`, so the database starts first at boot. The environment passed to
the server is `HOME` and a `PATH` that includes `/usr/local/bin` for Stockfish.

Server flags:

| Flag | Environment default | Purpose |
|---|---|---|
| `-dsn` | `CHESS_DSN` | PostgreSQL connection string; empty disables persistence |
| `-jwt-secret-file` | `CHESS_JWT_SECRET_FILE` | Stable JWT signing key |
| `-trusted-proxies` | | Proxy address(es) trusted for the client-IP header |
| `-proxy-header` | | Client-IP header set by the proxy (default `X-Real-IP`) |
| `-max-users` | | Registration cap (default 100, `0` = none) |
| `-anonymous-game-ttl` | | Retention of games without a registered player (default `24h`) |
| `-finished-game-ttl` | | Memory retention of finished games (default `1h`) |
| `-db-cleanup` | | Hourly integrity sweep: `off` (default), `report` (log findings), or `delete` ([database.md](database.md#integrity-sweep)) |

Administrative CLI commands run as `chess` so peer authentication selects the
right role; root may use `su -m` although the account has no shell:

```sh
jail# su -m chess -c '/home/chess/bin/chess-server db user list -dsn "postgres:///chess?host=/tmp"'
```

`db` subcommands: `init`, `delete -confirm` (drops the tables), `query`
(games, with 8-digit ID prefixes), `pgn -gameId <id|prefix> [-ply N]`,
`verify [-gameId <id|prefix>]` (replays stored games against the rules and
exits non-zero on a mismatch; run it after an upgrade), and `user add|delete|
set-password|set-hash|set-email|set-username|list`.

## Host nginx and Web Clients

The API has no version segment: routes are `/api/...` and `/health`. With the
layout in [`nginx-chess.conf`](../deploy/nginx-chess.conf), browsers
call `<origin>/chess/api/...` and nginx maps `/chess/api/` to `/api/`. The
bare `/chess` and `/chess/` redirect (302) to the page that embeds the
client, as a short link.

- `X-Real-IP` must carry the real client address (`$remote_addr`, after
  `real_ip` processing when PROXY protocol is in front of nginx). The server
  honors it only on connections from `chessd_trusted_proxies`.
- `proxy_set_header Connection "";` lets nginx reuse the upstream `keepalive`
  connections; without it, every request opens a new connection.
- The existing security headers are unaffected: the API is same-origin under
  `/chess/`.

Browser clients are static files published by the host, and must match the
server's API paths:

- **Web client:** copy `internal/server/webserver/chess-client-web/`
  (`index.html`, `app.js`, `style.css`) to the site directory. Without the
  embedded `/config` endpoint, it uses the `/chess` API prefix.
- **WASM terminal client (if published):** `make wasm` (xterm.js is committed
  under `lib/`), then copy `web/chess-client-wasm/`. It derives its API base
  as `<origin>/chess`. Its API paths are compiled into `chess-client.wasm`,
  so rebuild it whenever `internal/client/api` changes; publish it together
  with the `wasm_exec.js` that `make wasm` copies from the same Go toolchain.

`web/chess-client-web` is a symlink to the embedded web client; copy with
`cp -RL` or `rsync -L` so the files, not the link, reach the site. Publish the
clients in the same change as the server, and expect browsers to hold cached
copies until a hard refresh.

## Reference

### PostgreSQL configuration

What `setup-jail.sh` applies, for review or manual installation:

```sh
jail# sysrc postgresql_enable=YES
jail# sysrc postgresql_initdb_flags="--encoding=UTF8 --locale-provider=builtin --builtin-locale=C.UTF-8 --auth-local=peer --auth-host=reject"
jail# service postgresql initdb            # only when no cluster exists
jail# service postgresql start
jail# su -m postgres -c "psql -X -d postgres" <<'SQL'
ALTER SYSTEM SET listen_addresses = '';
ALTER SYSTEM SET unix_socket_directories = '/tmp';
SQL
jail# service postgresql restart
```

`/var/db/postgres/data18/pg_hba.conf`:

```
# TYPE  DATABASE  USER      METHOD
local   all       postgres  peer
local   chess     chess     peer
```

Database administration uses the `postgres` account over the socket:
`su -m postgres -c psql`.

### Provisioning and privilege models

[`setup.sql`](../deploy/postgresql/setup.sql) creates the login role `chess`
(no superuser, create-db, create-role, replication, or RLS bypass; 20
connections), the `chess` database (`template0`, builtin `C.UTF-8`), and the
`chess` schema. It revokes `PUBLIC` access to the database and to schema
`public`, and sets role defaults for `search_path`, `statement_timeout`,
`lock_timeout`, and `idle_in_transaction_session_timeout`.

| Mode | Schema owner | `chess` role can | Schema migrations |
|---|---|---|---|
| Owner (default) | `chess` | DML and DDL in schema `chess` only | Automatic at server start |
| Split (`SPLIT_PRIVILEGES=yes`) | `chess_owner` (NOLOGIN) | `SELECT/INSERT/UPDATE/DELETE` only | `setup-jail.sh`, or manually as `postgres` |

In split mode the server refuses to start while a migration is pending.

### JWT signing key

The key is 48 random bytes, base64-encoded, readable only by `chess`. Without a
key file, production mode generates a new key at every start and every session
ends on restart. To rotate, replace the file and restart `chessd`; users sign
in again.

### Rollback

Until the old release's files are deleted, a failed upgrade can be reverted:

```sh
jail# service chessd onestop
jail# cp /var/backups/chessd.rc.<time> /usr/local/etc/rc.d/chessd
jail# cp /home/chess/bin/chess-server.prev /home/chess/bin/chess-server
jail# service chessd start
```

Restore the previous web client files on the host as well.

### Backups and restores

- `chess-backup` ([`deploy/postgresql/chess-backup.sh`](../deploy/postgresql/chess-backup.sh),
  shared with Linux) writes `pg_dump --format=custom` files nightly and
  removes those older than 14 days. Copy them off the jail.
- Restore into a freshly provisioned database:
  `su -m postgres -c 'pg_restore --no-owner --role=chess -d chess <dump>'`
  (`--role=chess_owner` in split mode).
- PostgreSQL restarts: the server retries in-flight writes across a brief
  outage. A write that still fails marks storage `degraded` in `/health`; live
  games continue in memory, but durable history stops until `chessd` restarts.

### Verification checklist

```sh
jail# service chessd status
jail# fetch -qo - http://127.0.0.1:8080/health                    # "storage":"ok"
jail# sockstat -4 -6 -l | grep postgres                           # no TCP listener
jail# sockstat -4 -c | grep ':8080'                               # peer = nginx address
jail# su -m chess -c 'psql -X -d chess -Atc "SHOW search_path"'   # chess
jail# su -m chess -c 'psql -X -d postgres -c "select 1"'          # rejected by pg_hba
host# curl -s https://<site>/chess/health                         # through nginx
```
