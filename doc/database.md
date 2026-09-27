# Database Operations

`psql` recipes for inspecting and maintaining the `chess` database in the
jail. Deployment and provisioning are in [deployment.md](deployment.md).
Paths and the DSN below are the `rc.d/chessd` defaults.

## Connecting

```sh
jail# su -m postgres -c 'psql -X -d chess'   # administrator (superuser)
jail# su -m chess -c 'psql -X -d chess'      # the service role
```

Everything lives in schema **`chess`**, not `public`. The `chess` role
resolves names there by itself (`search_path` is set for it by `setup.sql`);
a `postgres` session does not, so `\dt` reports no tables until the schema
is selected:

```sql
SET search_path = chess;   -- this session; or qualify names: \dt chess.*
\dt
```

To make it permanent for the administrator in this database only:

```sql
ALTER ROLE postgres IN DATABASE chess SET search_path = chess, public;
```

The database is owned by `postgres` and `chess` holds only `CONNECT` on it
(`\l` shows `chess=c/postgres`); in the default owner mode `chess` owns schema
`chess` and its tables, and nothing else.

| Table | Contents |
|---|---|
| `schema_version` | One row: the applied migration |
| `users` | Accounts: lowercase username and email, Argon2id password hash |
| `sessions` | One active session per user; deleting it signs the user out |
| `games` | Players, claims (`*_claimed_by`), name snapshots, result, times |
| `moves` | Ply `move_number` 1..n, UCI move, FEN after the move |

The recipes below assume `SET search_path = chess;`. `\set g '<game-id>'`
stores a game ID for the `:'g'` references.

## Overview

```sql
SELECT version, updated_at FROM schema_version;

SELECT (SELECT count(*) FROM users)    AS users,
       (SELECT count(*) FROM games)    AS games,
       (SELECT count(*) FROM games WHERE result IS NULL) AS unfinished,
       (SELECT count(*) FROM moves)    AS plies,
       (SELECT count(*) FROM sessions WHERE expires_at > now()) AS signed_in;
```

## Users

```sql
SELECT user_id, username, email, created_at, last_login_at
FROM users ORDER BY created_at;
```

Passwords are not stored. `password_hash` is an Argon2id string in PHC
format, `$argon2id$v=19$m=<KiB>,t=<passes>,p=<lanes>$<salt>$<hash>`, from
which the password cannot be recovered. Show the parameters without the
secret parts:

```sql
SELECT username, split_part(password_hash, '$', 2) AS algorithm,
       split_part(password_hash, '$', 4) AS parameters
FROM users;
```

Create, reset, and rename accounts with the server CLI, not SQL: it
normalizes names, enforces uniqueness rules, and hashes passwords. Run it as
`chess` so peer authentication selects the right role; without `-password`
it prompts, which keeps the password out of the shell history and `ps`.

```sh
jail# su -m chess -c '/home/chess/bin/chess-server db user add -username <name> [-email <addr>] -dsn "postgres:///chess?host=/tmp"'
jail# su -m chess -c '/home/chess/bin/chess-server db user set-password -username <name> -dsn "postgres:///chess?host=/tmp"'
jail# su -m chess -c '/home/chess/bin/chess-server db user list -dsn "postgres:///chess?host=/tmp"'
```

Other `db user` subcommands: `delete`, `set-email`, `set-username`, and
`set-hash` (an existing PHC string). CLI-created accounts are identical to
site registrations, and the CLI ignores the registration cap.

Sign a user out everywhere (takes effect on their next request):

```sql
DELETE FROM sessions WHERE user_id = (SELECT user_id FROM users WHERE username = 'alice');
```

Deleting a user removes their session; their games keep the claim and the
name snapshot.

## Games

Recent games with players and length:

```sql
SELECT g.game_id, g.start_time_utc,
       coalesce(g.white_name, CASE g.white_type WHEN 2 THEN 'Stockfish L' || g.white_level ELSE 'anonymous' END) AS white,
       coalesce(g.black_name, CASE g.black_type WHEN 2 THEN 'Stockfish L' || g.black_level ELSE 'anonymous' END) AS black,
       coalesce(g.result, 'ongoing') AS result,
       (SELECT count(*) FROM moves m WHERE m.game_id = g.game_id) AS plies
FROM games g ORDER BY g.start_time_utc DESC LIMIT 20;
```

A user's games: add
`WHERE (SELECT user_id FROM users WHERE username = 'alice') IN (g.white_claimed_by, g.black_claimed_by)`.
Find a game from the 8-digit prefix that `db query` prints:
`SELECT game_id FROM games WHERE game_id::text LIKE '68007fd1%';`.

One game, header and every ply with its FEN:

```sql
\set g '68007fd1-8970-4d84-950d-2f6ec6375c0b'
SELECT * FROM games WHERE game_id = :'g' \gx
SELECT move_number AS ply, player_color, move_uci, fen_after_move, move_time_utc
FROM moves WHERE game_id = :'g' ORDER BY move_number;
```

Final position:

```sql
SELECT coalesce(
  (SELECT fen_after_move FROM moves WHERE game_id = :'g' ORDER BY move_number DESC LIMIT 1),
  (SELECT initial_fen FROM games WHERE game_id = :'g')) AS final_fen;
```

The move line in UCI with move numbers (numbering follows the initial FEN, so
a game that starts with Black to move begins `N...`):

```sql
WITH s AS (SELECT split_part(initial_fen, ' ', 2) AS side,
                  split_part(initial_fen, ' ', 6)::int AS fullmove
           FROM games WHERE game_id = :'g')
SELECT string_agg(
         CASE WHEN m.player_color = 'w'
                THEN (s.fullmove + (m.move_number - CASE s.side WHEN 'w' THEN 1 ELSE 0 END) / 2) || '. '
              WHEN m.move_number = 1 THEN s.fullmove || '... '
              ELSE '' END || m.move_uci,
         ' ' ORDER BY m.move_number) AS uci_line
FROM moves m, s WHERE m.game_id = :'g';
```

SQL has no SAN; the database stores UCI and the server derives notation on
read. For real PGN use the CLI (a full ID or 8-digit prefix; `-ply N` stops
after N plies) or the API:

```sh
jail# su -m chess -c '/home/chess/bin/chess-server db pgn -gameId 68007fd1 -dsn "postgres:///chess?host=/tmp"'
host# curl -s https://<site>/chess/api/games/<game-id>/pgn
```

Check every stored game against the rules (each move legal from the stored
position before it, the stored position after it reproduced, results
consistent with the final position); it exits non-zero on any problem:

```sh
jail# su -m chess -c '/home/chess/bin/chess-server db verify -dsn "postgres:///chess?host=/tmp"'
```

### Deleting games

The server keeps unfinished games, and finished ones for `-finished-game-ttl`
(default 1 hour), in memory. Deleting the row of a game it still holds makes
that game's next write fail, which marks storage `degraded` until `chessd`
restarts. Delete old finished games freely; for anything else, stop `chessd`
first (`service chessd stop`, delete, `service chessd start`).

```sql
BEGIN;
DELETE FROM games WHERE game_id = :'g';   -- its moves are removed by cascade
COMMIT;
```

All games of one user (both colors):

```sql
DELETE FROM games
WHERE (SELECT user_id FROM users WHERE username = 'alice') IN (white_claimed_by, black_claimed_by);
```

Games without a registered player are already deleted by the server 24
hours after their last activity (`-anonymous-game-ttl`).

## Starting Fresh

Every option below loses data: take a dump first if any of it matters
(`su -m postgres -c 'pg_dump -Fc -d chess -f /var/db/postgres/chess-<date>.dump'`).

**Clear the data, keep the schema** (accounts, sessions, and games):

```sh
jail# service chessd stop
jail# su -m postgres -c 'psql -X -d chess -c "TRUNCATE chess.moves, chess.games, chess.sessions, chess.users"'
jail# service chessd start
```

**Recreate the tables** (drops and re-runs the migrations):

```sh
jail# service chessd stop
jail# su -m chess -c '/home/chess/bin/chess-server db delete -confirm -dsn "postgres:///chess?host=/tmp"'
jail# su -m chess -c '/home/chess/bin/chess-server db init -dsn "postgres:///chess?host=/tmp"'
jail# service chessd start
```

`db init` prints `Database schema ready (version 1)`. In owner mode the
server would also create missing tables at startup; running `db init`
first surfaces any error before the service starts.

**Rebuild the database and role** (as on a new jail):

```sh
jail# service chessd stop
jail# su -m postgres -c 'psql -X -d postgres' <<'SQL'
DROP DATABASE IF EXISTS chess WITH (FORCE);
DROP ROLE IF EXISTS chess_owner;   -- exists only in split mode
DROP ROLE IF EXISTS chess;
SQL
jail# CHESS_BINARY=/home/chess/bin/chess-server TRUSTED_PROXIES=<nginx-address> \
      sh deploy/freebsd/setup-jail.sh
```

With neither role nor database present, `setup-jail.sh` runs `setup.sql`,
creates the tables, starts `chessd`, and checks `/health`. The JWT key is
kept; sessions are gone with the users, so everyone signs in again (and
accounts must be created again).

Verify any of them:

```sql
\dt chess.*                                   -- games, moves, schema_version, sessions, users
SELECT version FROM chess.schema_version;     -- 1
\drds                                         -- chess: search_path, statement/lock/idle timeouts
```
