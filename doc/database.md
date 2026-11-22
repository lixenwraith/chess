# Database Operations

Inspecting and maintaining the chess database: the server's own `db`
commands, and `psql` recipes as the PostgreSQL administrator. Installation is
in [deployment.md](deployment.md) (FreeBSD jail) and
[deployment-linux.md](deployment-linux.md) (Linux with systemd).

## Typical Layout

The server expects one database with its tables in one schema of the same
name, owned by a role that exists only for it:

| Object | Name | Notes |
|---|---|---|
| Database | `chess` | Owned by `postgres`; only the `chess` role may connect |
| Schema | `chess` | All tables; `public` is locked down and stays empty |
| Login role | `chess` | No password, no superuser, create-database, or create-role rights; 20 connections |
| Owner role (split mode only) | `chess_owner` | Cannot log in; owns the schema while `chess` only reads and writes rows |

The service runs as an OS account named `chess` and connects over the local
Unix socket with **peer** authentication, which maps the OS account to the
role of the same name, so no password exists anywhere. The `chess` role has
its `search_path` set to `chess` in this database, so the service never names
the schema. [`deploy/postgresql/setup.sql`](../deploy/postgresql/setup.sql)
creates all of this; the deployment scripts run it once.

Administration uses the `postgres` superuser account, which keeps the
distribution's defaults. Its `search_path` (`"$user", public`) does not
include `chess`, so **every recipe names the schema explicitly** (`chess.games`).
This keeps other databases and tools on the same server unaffected, and a
statement typed into the wrong database fails instead of acting on whatever
it finds.

## The Server's `db` Commands

Accounts, games, the archive and the schema are managed by the server binary
itself: `chess-server db <command>`, run as the `chess` account against the
service's database. The examples below give that command; on each system, run
it as:

```sh
# Linux
sudo chess-db <command>
# FreeBSD jail
su -m chess -c '/home/chess/bin/chess-server db <command> -dsn "postgres:///chess?host=/tmp"'
```

`chess-db` is not a program of its own but the wrapper `deploy/linux/setup.sh`
installs: it runs `/usr/local/bin/chess-server db <command>` as `chess` with
the service's `CHESSD_DSN` from `/etc/chessd/chessd.env`.

## Connecting

```sh
# Linux
sudo -u postgres psql -X -d chess
# FreeBSD jail
su -m postgres -c 'psql -X -d chess'
```

Working with the `chess` schema as `postgres`:

| Goal | How |
|---|---|
| List the tables | `\dt chess.*` (a plain `\dt` shows `public`, which is empty) |
| Describe a table | `\d chess.games` |
| List schemas and owners | `\dn+` |
| Query | `SELECT ... FROM chess.games` |
| Unqualified names for one session | `SET search_path = chess;` (lasts until you disconnect) |
| Unqualified names for one invocation | `sudo -u postgres env PGOPTIONS='-c search_path=chess' psql -X -d chess` |

Neither of the last two changes anything stored: the next session starts with
the defaults again. Avoid `ALTER ROLE postgres ... SET search_path`, which
would persist.

The tables:

- `chess.schema_version`: one row, the applied migration
- `chess.users`: accounts; lowercase username and email, Argon2id password
  hash
- `chess.sessions`: one active session per user; deleting it signs the user
  out
- `chess.password_resets`: one pending reset per account, the SHA-256 of the
  emailed token
- `chess.games`: players, claims (`*_claimed_by`), name snapshots, guest
  seats (`*_seat`, the SHA-256 of a secret only the guest has), `result`,
  `termination`, `concession_*`, times; for an imported game `imported_by`,
  `event`, `site`, `round`
- `chess.moves`: ply `move_number` 1..n, UCI move, FEN after the move
- `chess.collections`: the archive's collections; source, licence,
  attribution, `published`
- `chess.archive_games`: a collection's games, with the search keys (players,
  Elo, ECO) and the content hash that stores a game once

`result` is `white_wins`, `black_wins`, `stalemate`, or `draw`, and
`termination` says how: `checkmate`, `resignation`, `stalemate`,
`insufficient_material`, `threefold_repetition`, `fifty_move_rule`, or
`agreement`. Both are empty while a game is unfinished, and an undo empties
them again. `concession_result`, `concession_termination`, and
`concession_ply` keep the first resignation or agreed draw even when an undo
against the computer continued play:

```sql
SELECT game_id, concession_result, concession_ply,
       coalesce(result, 'unfinished') AS now
FROM chess.games
WHERE concession_result IS NOT NULL
  AND (result IS DISTINCT FROM concession_result
       OR termination IS DISTINCT FROM concession_termination);   -- continued after conceding
```

`\set g '<game-id>'` stores a game ID for the `:'g'` references below.

## Overview

```sql
SELECT version, updated_at FROM chess.schema_version;

SELECT (SELECT count(*) FROM chess.users)    AS users,
       (SELECT count(*) FROM chess.games)    AS games,
       (SELECT count(*) FROM chess.games WHERE result IS NULL) AS unfinished,
       (SELECT count(*) FROM chess.moves)    AS plies,
       (SELECT count(*) FROM chess.sessions WHERE expires_at > now()) AS signed_in;

SELECT coalesce(termination, 'unfinished') AS termination, count(*)
FROM chess.games GROUP BY 1 ORDER BY 2 DESC;
```

## Users

```sql
SELECT user_id, username, email, created_at, last_login_at
FROM chess.users ORDER BY created_at;
```

Passwords are not stored. `password_hash` is an Argon2id string in PHC
format, `$argon2id$v=19$m=<KiB>,t=<passes>,p=<lanes>$<salt>$<hash>`, from
which the password cannot be recovered. Show the parameters without the
secret parts:

```sql
SELECT username, split_part(password_hash, '$', 2) AS algorithm,
       split_part(password_hash, '$', 4) AS parameters
FROM chess.users;
```

Create, reset, and rename accounts with the server's CLI, not SQL: it
normalizes names, enforces uniqueness rules, and hashes passwords. Without
`-password` it prompts, which keeps the password out of the shell history and
the process list.

```sh
chess-server db user add -username <name> [-email <addr>]
chess-server db user set-password -username <name>
chess-server db user list
```

Other `user` subcommands: `delete`, `set-email`, `set-username`, `set-hash`
(an existing PHC string), and `reset-link -url <web client>`, which prints a
one-time password-reset link (valid an hour) to hand to someone who cannot
receive the recovery email. CLI-created accounts are identical to site
registrations, and the CLI ignores the registration cap.

A game imported from PGN (`POST /api/games/import`, `db game import`) has
`imported_by` set to its owner, no claims, the PGN's `event`, `site` and
`round`, and both sides human with the tag names as snapshots. It is listed
for its owner, never purged as anonymous, and deleted with its moves when the
owner removes it (`DELETE /api/users/me/games/{id}`) or by `db game delete
-gameId <id|prefix> -confirm`. A player removes a game they share with
another registered player by releasing their own claim instead.

Pending password resets live in `password_resets`: the SHA-256 of the token
in the emailed link, one per account, deleted when used, expired, or by the
hourly cleanup. The token itself is never stored.

Sign a user out everywhere (takes effect on their next request):

```sql
DELETE FROM chess.sessions
WHERE user_id = (SELECT user_id FROM chess.users WHERE username = 'alice');
```

Deleting a user removes their session; their games keep the claim and the
name snapshot. Games left with no remaining registered player are removed by
the optional integrity sweep (below) once idle for a day.

## Games

Recent games with players, outcome, and length:

```sql
SELECT g.game_id, g.start_time_utc,
       coalesce(g.white_name, CASE g.white_type WHEN 2 THEN 'Stockfish L' || g.white_level ELSE 'anonymous' END) AS white,
       coalesce(g.black_name, CASE g.black_type WHEN 2 THEN 'Stockfish L' || g.black_level ELSE 'anonymous' END) AS black,
       coalesce(g.result || ' by ' || g.termination, 'unfinished') AS outcome,
       (SELECT count(*) FROM chess.moves m WHERE m.game_id = g.game_id) AS plies
FROM chess.games g ORDER BY g.start_time_utc DESC LIMIT 20;
```

A user's games: add
`WHERE (SELECT user_id FROM chess.users WHERE username = 'alice') IN (g.white_claimed_by, g.black_claimed_by)`.
Find a game from the 8-digit prefix that `db query` prints:
`SELECT game_id FROM chess.games WHERE game_id::text LIKE '68007fd1%';`.

One game, header and every ply with its FEN:

```sql
\set g '68007fd1-8970-4d84-950d-2f6ec6375c0b'
SELECT * FROM chess.games WHERE game_id = :'g' \gx
SELECT move_number AS ply, player_color, move_uci, fen_after_move, move_time_utc
FROM chess.moves WHERE game_id = :'g' ORDER BY move_number;
```

Final position:

```sql
SELECT coalesce(
  (SELECT fen_after_move FROM chess.moves WHERE game_id = :'g' ORDER BY move_number DESC LIMIT 1),
  (SELECT initial_fen FROM chess.games WHERE game_id = :'g')) AS final_fen;
```

The move line in UCI with move numbers (numbering follows the initial FEN, so
a game that starts with Black to move begins `N...`):

```sql
WITH s AS (SELECT split_part(initial_fen, ' ', 2) AS side,
                  split_part(initial_fen, ' ', 6)::int AS fullmove
           FROM chess.games WHERE game_id = :'g')
SELECT string_agg(
         CASE WHEN m.player_color = 'w'
                THEN (s.fullmove + (m.move_number - CASE s.side WHEN 'w' THEN 1 ELSE 0 END) / 2) || '. '
              WHEN m.move_number = 1 THEN s.fullmove || '... '
              ELSE '' END || m.move_uci,
         ' ' ORDER BY m.move_number) AS uci_line
FROM chess.moves m, s WHERE m.game_id = :'g';
```

SQL has no SAN; the database stores UCI and notation is derived on read. For
real PGN use the CLI (a full ID or 8-digit prefix; `-ply N` stops after N
plies) or the API:

```sh
chess-server db pgn -gameId 68007fd1
curl -s https://<site>/chess/api/games/<game-id>/pgn
```

`db verify` checks every stored game against the rules (move numbering, each
move legal from the stored position before it, the stored position after it
reproduced, the result and its termination consistent with the final
position) and exits non-zero on any problem:

```sh
chess-server db verify
```

### The archive

Collections of games nobody owns, read from PGN databases and listed
publicly once published ([api.md](api.md#browse-the-archive)), managed by the
subcommands of `chess-server db archive`:

```sh
chess-server db archive collection set -name <name> -title <title>     # create; unpublished
chess-server db archive import -collection <name> -file <pgn> -dry-run # read, replay, count
chess-server db archive import -collection <name> -file <pgn>          # or -dir <dir>: every *.pgn
chess-server db archive collection set -name <name> -source <url> \
    -licence <text> -attribution <text> -published                     # list it publicly
chess-server db archive collection list
chess-server db archive feature -gameId <id|prefix> -order <n>         # 0 takes it away
chess-server db archive collection delete -name <name> -confirm        # with its games
```

- An import reports, for each file, the games stored, duplicate and rejected,
  each rejected one with its place in the file and the reason. A game the
  rules refuse is skipped; the rest of the file is stored.
- A game is stored once: its start position, moves, result and the `Event`,
  `Site`, `Date`, `Round`, `White` and `Black` tags identify it, so a
  repeated import, or the same game in a second file, stores nothing.
- Moves and tags are kept; comments, variations and annotations are dropped,
  as for every import. `WhiteElo`, `BlackElo` and `ECO` are kept where they
  are numbers and codes.
- `collection set` changes only the fields its flags name; `-featured <n>`
  orders the collections, `-published=false` withdraws one from the list.

**Before a collection is published**, check what its source allows; the
database refuses to publish one that names no source, licence and
attribution, and the web client shows all three with the collection.

- The moves of a game are widely held to be facts, but a compilation can
  carry a database right, and a source's terms can forbid redistributing
  what it serves, bulk downloads included.
- Annotations are authored text: they are dropped, so a collection whose
  value is its annotations has nothing to offer here.
- An unpublished collection is absent from the public list only: a game's
  history and PGN still answer to whoever has its ID.

### Deleting games

Delete finished or unfinished games at any time:

```sql
BEGIN;
DELETE FROM chess.games WHERE game_id = :'g';   -- its moves are removed by cascade
COMMIT;
```

All games of one user (both colors):

```sql
DELETE FROM chess.games
WHERE (SELECT user_id FROM chess.users WHERE username = 'alice') IN (white_claimed_by, black_claimed_by);
```

A game the server is still playing is unloaded when its next write finds the
row gone (that last move is not stored), or earlier by the integrity sweep;
the server stays healthy. Deleting individual `chess.moves` rows instead leaves a broken line
that replay cannot use: delete the whole game, or let the integrity sweep do
it.

Games without a registered player are already deleted by the server 24 hours
after their last activity (`-anonymous-game-ttl`).

### Integrity sweep

With the server flag `-db-cleanup report` (log only) or `-db-cleanup delete`,
an hourly sweep looks for data the server cannot use:

- games whose rows were deleted while the server held them (unloaded);
- games with missing plies, i.e. deleted move rows;
- games that fail `db verify`, 200 per run in ID order;
- games whose registered players were all deleted, once idle for the
  anonymous-game retention;
- accounts whose password hash is not a usable Argon2id string (nobody can
  sign in to them).

Each finding is logged at warning level with its reason; `delete` also
removes them. Start with `report`, read the log, then switch. Set it through
`CHESSD_FLAGS` in `/etc/chessd/chessd.env` (Linux) or `chessd_flags` in
`rc.conf` (FreeBSD), and restart the service.

## Starting Fresh

Every option below loses data: take a dump first if any of it matters.

```sh
sudo -u postgres pg_dump -Fc -d chess -f /var/lib/postgresql/chess-$(date +%F).dump   # Linux (Debian/Ubuntu path)
su -m postgres -c 'pg_dump -Fc -d chess -f /var/db/postgres/chess-$(date +%F).dump'  # FreeBSD
```

The service commands are `systemctl stop|start chessd` on Linux and
`service chessd stop|start` on FreeBSD.

**Clear the data, keep the schema** (accounts, sessions, games and the
archive), with the service stopped:

```sql
TRUNCATE chess.moves, chess.archive_games, chess.games, chess.collections,
         chess.password_resets, chess.sessions, chess.users;
```

**Recreate the tables** (drops and re-runs the migrations), with the service
stopped:

```sh
chess-server db delete -confirm
chess-server db init
```

`db init` prints `Database schema ready (version 8)`. In split mode the
`chess` role owns no tables and cannot drop or create them; run both commands
as `postgres` acting as the owner role instead, calling the binary itself
(`chess-db` runs as `chess`), e.g. on Linux:

```sh
sudo -u postgres /usr/local/bin/chess-server db delete -confirm \
    -dsn "dbname=chess user=postgres options='-c role=chess_owner -c search_path=chess'"
```

**Rebuild the database and role** (as on a new host), with the service
stopped:

```sql
DROP DATABASE IF EXISTS chess WITH (FORCE);
DROP ROLE IF EXISTS chess_owner;   -- exists only in split mode
DROP ROLE IF EXISTS chess;
```

Then rerun the deployment script (`deploy/linux/setup.sh` or
`deploy/freebsd/setup-jail.sh`) with the same inputs as before. With neither
role nor database present it runs `setup.sql`, creates the tables, starts the
service, and checks `/health`. The JWT key is kept; accounts are gone, so
create them again.

Verify any of them:

```sql
\dt chess.*                                   -- archive_games, collections, games, moves, password_resets, schema_version, sessions, users
SELECT version FROM chess.schema_version;     -- 7
\drds                                         -- chess: search_path and timeouts in database chess
```
