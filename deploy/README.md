# Deployment Scripts

Run in this order; each script's header documents its inputs. The full guide is
[doc/deployment.md](../doc/deployment.md); day-to-day database work (psql,
accounts, deleting games, starting fresh) is in
[doc/database.md](../doc/database.md).

| Order | Where | Step | Purpose |
|---|---|---|---|
| 1 | Bastille host, once | `freebsd/host.sh <jail>` | `sysvshm=new` for PostgreSQL, jail restart |
| 2 | Jail, once | `pkg install postgresql18-server postgresql18-client stockfish` | Packages |
| 3 | Build machine | `make server-freebsd` | Static `bin/chess-server-freebsd-amd64` |
| 4 | Jail (root) | `freebsd/setup-jail.sh` | PostgreSQL setup, database, `chessd` service, optional log rotation and backups; rerun to upgrade |
| 5 | Bastille host | `freebsd/nginx-chess.conf` | nginx locations for `/chess/api/`, `/chess/health`, and the `/chess` short link |
| 6 | Jail, once | `chess-server db user add -username <name>` | First account (see the guide) |

Supporting files, installed by `setup-jail.sh`:

- `postgresql/setup.sql` — role, database, and schema (owner or split mode)
- `freebsd/rc.d/chessd` — `daemon(8)` service script
- `freebsd/chess-backup.sh` — nightly `pg_dump` with 14-day retention

Keep deployment-specific values (addresses, host names) in `rc.conf` and the
host nginx configuration, not in this repository.
