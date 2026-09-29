# Deployment: Linux with systemd and PostgreSQL

A native install on Debian, Ubuntu, Arch, or another systemd distribution
that already runs PostgreSQL. The service is `chessd`, running as a system
account `chess` without a login shell; it reaches PostgreSQL over the local
Unix socket with peer authentication, so no database password exists. A
reverse proxy (nginx) on the same host, if any, forwards `/chess/api/` to it.
The FreeBSD jail layout is in [deployment.md](deployment.md); database
maintenance is in [database.md](database.md).

```
browser ──TLS──▶ nginx ──HTTP 127.0.0.1:8080──▶ chessd (chess-server, user chess)
                                                    │ Unix socket, peer auth
                                                    ▼
                                             PostgreSQL 17+ (database chess)
```

## Prerequisites

PostgreSQL 17 or later (18 recommended), running, with its default local
socket; Stockfish; systemd; Go 1.27 to build.

**Debian / Ubuntu.** Ubuntu 24.04 ships PostgreSQL 16, so 18 comes from the
PostgreSQL project's repository (PGDG). With 18 already installed, check the
cluster with `pg_lsclusters` and skip this.

```sh
sudo apt install postgresql-common stockfish
sudo /usr/share/postgresql-common/pgdg/apt.postgresql.org.sh
sudo apt install postgresql-18        # creates and starts cluster 18/main
```

The default `pg_hba.conf` already maps local OS accounts to roles of the same
name (`local all all peer`). Stockfish installs as `/usr/games/stockfish`;
the service adds `/usr/games` to its `PATH`.

**Arch.** The package does not create a cluster; initialize one the first
time only:

```sh
sudo pacman -S postgresql stockfish
sudo -u postgres initdb -D /var/lib/postgres/data --encoding=UTF8 \
    --locale-provider=builtin --builtin-locale=C.UTF-8 \
    --auth-local=peer --auth-host=scram-sha-256
sudo systemctl enable --now postgresql
```

A cluster made by a plain `initdb` trusts every local connection. The chess
account connects either way, but `peer` is what you want: without it, any
local account can connect as any role.

**Existing clusters.** Other databases, roles, and applications on the
server are left alone: the setup creates one role and one database named
`chess`, changes no server settings, and edits `pg_hba.conf` only when asked
(`PG_HBA=yes`, below). It does not change `listen_addresses`; the service
itself only uses the socket.

## Install

```sh
make server                                         # bin/chess-server, static
sudo CHESS_BINARY=bin/chess-server TRUSTED_PROXIES=127.0.0.1 deploy/linux/setup.sh
sudo chess-db user add -username <name>             # prompts for the password
```

The script is idempotent: rerun it with a new binary to upgrade. It stops
`chessd`, installs everything, migrates the schema, starts the service, and
fails unless `/health` reports `"storage":"ok"`.

| Input | Default | Meaning |
|---|---|---|
| `CHESS_BINARY` | required | `chess-server` binary to install |
| `TRUSTED_PROXIES` | kept from `chessd.env` | Proxy address(es) whose `X-Real-IP` is trusted; `127.0.0.1` for nginx on this host |
| `CHESSD_HOST`, `CHESSD_PORT` | `127.0.0.1`, `8080` | Listen address, first install only |
| `SPLIT_PRIVILEGES` | `no` | `yes`: the service role only reads and writes rows ([database.md](database.md#typical-layout)) |
| `ENABLE_BACKUP` | `no` | `yes`: nightly `pg_dump` through a systemd timer |
| `PG_HBA` | `no` | `yes`: add `local chess chess peer` to `pg_hba.conf` if the chess account cannot connect |

What it installs:

| Path | Purpose |
|---|---|
| account `chess` | System account, no login shell, home `/var/lib/chessd` |
| `/usr/local/bin/chess-server` | The server, `root:root 0755`; the previous copy kept as `.prev` |
| `/usr/local/sbin/chess-db` | Runs `chess-server db ...` as `chess` against the database: `chess-db user list`, `chess-db verify` |
| `/etc/chessd/chessd.env` | Settings, `root:chess 0640`; written on the first install only |
| `/var/lib/chessd/jwt.key` | JWT signing key, `chess 0600`, generated once; sessions survive restarts |
| `/etc/systemd/system/chessd.service` | The service unit, replaced on every run |
| `chess-backup.service`, `.timer`, `/usr/local/sbin/chess-backup` | With `ENABLE_BACKUP=yes`: dumps to `backups/` in the postgres home, 14 days kept |

PostgreSQL steps, in order: check the server version (17+) and socket as
`postgres`; create role, database, and schema with
[`setup.sql`](../deploy/postgresql/setup.sql) when neither exists (a
half-present pair stops the script); check that the `chess` account can
connect.

### When the chess account cannot connect

The first matching line of `pg_hba.conf` decides. A cluster whose local
lines require a password (`scram-sha-256`, `md5`) refuses the passwordless
`chess` role, and the script stops with the file's path and this line to add
above the other `local` lines:

```
local   chess   chess   peer
```

Then reload (`sudo systemctl reload postgresql`) and rerun, or rerun with
`PG_HBA=yes` to have the line inserted and PostgreSQL reloaded (the previous
file is kept next to it with a timestamp).

## Settings

`/etc/chessd/chessd.env` is read by the unit; restart after a change
(`sudo systemctl restart chessd`).

| Setting | First-install value | Purpose |
|---|---|---|
| `CHESSD_HOST`, `CHESSD_PORT` | `127.0.0.1`, `8080` | Listen address; keep loopback when nginx runs on the host |
| `CHESSD_DSN` | `"host=<socket dir> dbname=chess"` | Database over the socket as the OS account |
| `CHESSD_JWT_KEY` | `/var/lib/chessd/jwt.key` | Stable signing key |
| `CHESSD_TRUSTED_PROXIES` | `TRUSTED_PROXIES` | Comma-separated proxy addresses |
| `CHESSD_FLAGS` | empty | Other server flags, e.g. `-max-users 500 -db-cleanup report` |

Server flags worth knowing: `-max-users` (registration cap, default 100),
`-anonymous-game-ttl` (default `24h`), `-finished-game-ttl` (default `1h`),
`-db-cleanup off|report|delete` (the integrity sweep, see
[database.md](database.md#integrity-sweep)), `-log-level`, `-log-http`.

Changes to the unit belong in a drop-in (`sudo systemctl edit chessd`), which
the script never overwrites.

## Operation

```sh
systemctl status chessd
journalctl -u chessd -f                       # server log
sudo chess-db user add -username <name>       # accounts; see database.md
sudo chess-db verify                          # check stored games; run after upgrades
curl -s http://127.0.0.1:8080/health          # "storage":"ok", "version" = the build
systemctl list-timers chess-backup.timer      # with ENABLE_BACKUP=yes
```

**Upgrade:** build, then rerun `setup.sh` with the new `CHESS_BINARY`.
Schema migrations run before the service starts. `/health` reports the
running build as `version` (`git describe` of the source); check it matches,
and upgrade the server before or with a web client served from elsewhere.

**Rollback:** until you delete it, the previous binary is kept:

```sh
sudo systemctl stop chessd
sudo cp /usr/local/bin/chess-server.prev /usr/local/bin/chess-server
sudo systemctl start chessd
```

A newer schema is not downgraded: a binary older than the database refuses to
start (see the log), so keep a dump from before the upgrade.

**Uninstall:**

```sh
sudo systemctl disable --now chessd chess-backup.timer
sudo rm -f /etc/systemd/system/chessd.service /etc/systemd/system/chess-backup.{service,timer} \
    /usr/local/bin/chess-server{,.prev} /usr/local/sbin/chess-{db,backup}
sudo systemctl daemon-reload
sudo rm -rf /etc/chessd /var/lib/chessd
sudo -u postgres psql -X -c 'DROP DATABASE chess WITH (FORCE)' -c 'DROP ROLE IF EXISTS chess_owner' -c 'DROP ROLE chess'
sudo userdel chess
```

## nginx on the Same Host

Use [`deploy/nginx-chess.conf`](../deploy/nginx-chess.conf) with
`<server-address>:<port>` = `127.0.0.1:8080`, and install with
`TRUSTED_PROXIES=127.0.0.1`, so the server keys rate limits on the client
address nginx sends in `X-Real-IP`. The browser clients and their
publishing steps are the same as on FreeBSD
([deployment.md](deployment.md#host-nginx-and-web-clients)).

## Service Sandbox

The unit runs the server with systemd's sandboxing: the whole file system is
read-only except `/var/lib/chessd`, home directories and `/tmp` are hidden,
no capabilities or privilege escalation, only Unix and IP sockets, and the
`@system-service` system-call set. Stockfish runs inside the same sandbox.
`systemd-analyze security chessd` summarizes the result.

## Running as Your Own Account (Development)

Without a `chess` account, the server runs as you, with a database owned by
your PostgreSQL role; tables then live in that database's `public` schema,
which is fine for development:

```sh
sudo -u postgres createuser --createdb "$USER"    # once, if you have no role yet
createdb chess_dev
bin/chess-server db init -dsn "dbname=chess_dev"
bin/chess-server -dev -dsn "dbname=chess_dev" -serve   # API :8080, web client :9090
bin/chess-server db user add -username me -dsn "dbname=chess_dev"
```

`-dev` relaxes rate limits and uses a fixed JWT key; never use it in
production. The test suites need a separate, disposable database whose
tables they drop ([test/README.md](../test/README.md)):

```sh
createdb chess_test
export CHESS_TEST_DSN="dbname=chess_test"
go test ./...
test/run-test-server.sh bin/chess-server     # then test/test-*.sh in another terminal
```

## Verification Checklist

```sh
systemctl is-active chessd                                  # active
curl -s http://127.0.0.1:8080/health                        # "storage":"ok"
ps -o user= -C chess-server                                 # chess
sudo -u postgres psql -X -d chess -c '\dt chess.*'          # five tables
sudo chess-db verify                                        # 0 problem(s)
sudo -u chess psql -X -d chess -Atc 'SHOW search_path'     # chess
```

The scripts were exercised on Ubuntu 24.04 with PostgreSQL 18.6 and
`systemctl` replaced by a stand-in that starts the unit's `ExecStart` as its
`User` with its `EnvironmentFile`: a first install that stops at
`pg_hba.conf`, the same with `PG_HBA=yes`, an idempotent rerun, an upgrade,
a trusted-proxy change, backups, and a fresh split-privilege install.
`systemd-analyze verify` accepts the units.
