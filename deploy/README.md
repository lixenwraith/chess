# Deployment Scripts

Run in this order; each script's header documents its inputs. The full guide is
[doc/deployment.md](../doc/deployment.md).

| Order | Where | Script | Purpose |
|---|---|---|---|
| 1 | Bastille host | `freebsd/host.sh <jail>` | `sysvshm=new` for PostgreSQL, jail restart |
| 2 | Build machine | `make server-freebsd` | Static `bin/chess-server-freebsd-amd64` |
| 3 | Jail (root) | `freebsd/setup-jail.sh` | PostgreSQL 18, database, account, service, backups, optional SQLite import |
| 4 | Bastille host | `freebsd/nginx-chess.conf` | Location block for the host nginx |

Supporting files, installed by `setup-jail.sh`:

- `postgresql/setup.sql` — role, database, and schema (owner or split mode)
- `postgresql/migrate-sqlite.sh` — one-time import of a v0.11 SQLite database
- `freebsd/rc.d/chess_server` — `daemon(8)` service script
- `freebsd/chess-backup.sh` — nightly `pg_dump` with 14-day retention
