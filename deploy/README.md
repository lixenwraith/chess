# Deployment Scripts

Each script's header documents its inputs. Day-to-day database work (psql,
accounts, deleting games, starting fresh) is in
[doc/database.md](../doc/database.md).

## FreeBSD jail

Full guide: [doc/deployment.md](../doc/deployment.md). Run in this order:

| Order | Where | Step | Purpose |
|---|---|---|---|
| 1 | Bastille host, once | `freebsd/host.sh <jail>` | `sysvshm=new` for PostgreSQL, jail restart |
| 2 | Jail, once | `pkg install postgresql18-server postgresql18-client stockfish` | Packages |
| 3 | Build machine | `make server-freebsd` | Static `bin/chess-server-freebsd-amd64` |
| 4 | Jail (root) | `freebsd/setup-jail.sh` | PostgreSQL setup, database, `chessd` service, optional log rotation and backups; rerun to upgrade |
| 5 | Bastille host | `nginx-chess.conf` | nginx locations for `/chess/api/`, `/chess/health`, and the `/chess` short link |
| 6 | Jail, once | `chess-server db user add -username <name>` | First account (see the guide) |

## Linux with systemd

Full guide: [doc/deployment-linux.md](../doc/deployment-linux.md) (Debian,
Ubuntu, Arch; PostgreSQL 17+ already running).

| Order | Where | Step | Purpose |
|---|---|---|---|
| 1 | Host | `make server` | Static `bin/chess-server` |
| 2 | Host (root) | `CHESS_BINARY=bin/chess-server linux/setup.sh` | Account, database, `chessd` systemd service, key, optional backup timer; rerun to upgrade |
| 3 | Host (root) | `chess-db user add -username <name>` | First account |
| 4 | nginx | `nginx-chess.conf` with `127.0.0.1:8080` | Optional reverse proxy |

## Files

- `postgresql/setup.sql` — role, database, and schema (owner or split mode)
- `postgresql/chess-backup.sh` — nightly `pg_dump` with 14-day retention
- `nginx-chess.conf` — nginx locations, with placeholders
- `freebsd/rc.d/chessd` — `daemon(8)` service script
- `linux/chessd.service` — sandboxed systemd unit; `linux/chess-backup.{service,timer}`

Keep deployment-specific values (addresses, host names) in `rc.conf`,
`/etc/chessd/chessd.env`, and the nginx configuration, not in this repository.
