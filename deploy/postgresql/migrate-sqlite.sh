#!/bin/sh
# One-time import of a chess SQLite database (schema v2, chess <= v0.11) into
# the PostgreSQL schema created by `chess-server db init`.
#
# Usage: migrate-sqlite.sh /path/to/chess.db [psql-conninfo]
#
#   conninfo  libpq connection string for the chess database; defaults to
#             $CHESS_DSN. Run as a role that can write the chess tables and
#             create temporary tables (the postgres superuser is simplest).
#   CHESS_SCHEMA  schema holding the chess tables (default: chess)
#
# Stop the old server first so the SQLite WAL is checkpointed. The import runs
# in one transaction and refuses a target that already holds users or games.
#
# - Every account is imported as a regular account; the old temporary/permanent
#   distinction and account expiry no longer exist.
# - Expired sessions are dropped.
# - Legacy games recorded before claim columns existed get claims backfilled
#   from player IDs that match an imported user, and every claim records the
#   claimant's username.
# - Games without a claim are imported too; the server deletes them once they
#   have been idle for -anonymous-game-ttl (24 hours by default).
set -eu

die() {
	echo "migrate-sqlite: $*" >&2
	exit 1
}

[ $# -ge 1 ] && [ $# -le 2 ] || die "usage: $0 /path/to/chess.db [psql-conninfo]"
sqlite_db=$1
conninfo=${2:-${CHESS_DSN:-}}
schema=${CHESS_SCHEMA:-chess}

for tool in sqlite3 psql; do
	command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
done
[ -r "$sqlite_db" ] || die "cannot read $sqlite_db"
[ -n "$conninfo" ] || die "no connection string: pass one or set CHESS_DSN"

version=$(sqlite3 -readonly "$sqlite_db" 'PRAGMA user_version;')
[ "$version" = 2 ] || die "SQLite schema version is $version; start chess v0.11 on it once to upgrade to version 2"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM

export_table() {
	sqlite3 -readonly -csv -nullvalue '\N' "$sqlite_db" "$2" >"$work/$1.csv"
}
export_table users 'SELECT user_id, username, email, password_hash, account_type,
	created_at, expires_at, last_login_at FROM users'
export_table sessions 'SELECT session_id, user_id, created_at, expires_at FROM sessions'
export_table games 'SELECT game_id, initial_fen,
	white_player_id, white_type, white_level, white_search_time, white_claimed_by,
	black_player_id, black_type, black_level, black_search_time, black_claimed_by,
	start_time_utc, result, end_time_utc FROM games'
export_table moves 'SELECT game_id, move_number, move_uci, fen_after_move,
	player_color, move_time_utc FROM moves'

# psql's \copy paths are resolved relative to the working directory.
cd "$work"
psql -X -q -v ON_ERROR_STOP=1 -v schema="$schema" "$conninfo" <<'SQL'
BEGIN;
SET LOCAL search_path TO :"schema";
-- Legacy CURRENT_TIMESTAMP defaults carry no offset and were written in UTC.
SET LOCAL TIME ZONE 'UTC';

DO $$
BEGIN
	-- Separate statements: PL/pgSQL plans each one only when it is reached.
	IF to_regclass('schema_version') IS NULL THEN
		RAISE EXCEPTION 'no chess schema in search_path; run chess-server db init first';
	END IF;
	IF (SELECT version FROM schema_version) <> 1 THEN
		RAISE EXCEPTION 'target schema must be at version 1';
	END IF;
	IF EXISTS (SELECT 1 FROM users) OR EXISTS (SELECT 1 FROM games) THEN
		RAISE EXCEPTION 'target already contains chess data; import only into an empty schema';
	END IF;
END $$;

CREATE TEMP TABLE import_users (
	user_id text, username text, email text, password_hash text, account_type text,
	created_at text, expires_at text, last_login_at text
) ON COMMIT DROP;
CREATE TEMP TABLE import_sessions (
	session_id text, user_id text, created_at text, expires_at text
) ON COMMIT DROP;
CREATE TEMP TABLE import_games (
	game_id text, initial_fen text,
	white_player_id text, white_type smallint, white_level smallint, white_search_time integer,
	white_claimed_by text,
	black_player_id text, black_type smallint, black_level smallint, black_search_time integer,
	black_claimed_by text,
	start_time_utc text, result text, end_time_utc text
) ON COMMIT DROP;
CREATE TEMP TABLE import_moves (
	game_id text, move_number integer, move_uci text, fen_after_move text,
	player_color text, move_time_utc text
) ON COMMIT DROP;

\copy import_users FROM 'users.csv' WITH (FORMAT csv, NULL '\N')
\copy import_sessions FROM 'sessions.csv' WITH (FORMAT csv, NULL '\N')
\copy import_games FROM 'games.csv' WITH (FORMAT csv, NULL '\N')
\copy import_moves FROM 'moves.csv' WITH (FORMAT csv, NULL '\N')

INSERT INTO users (user_id, username, email, password_hash, created_at, last_login_at)
SELECT user_id::uuid, lower(username), NULLIF(lower(email), ''), password_hash,
	created_at::timestamptz, NULLIF(last_login_at, '')::timestamptz
FROM import_users;

INSERT INTO sessions (session_id, user_id, created_at, expires_at)
SELECT s.session_id::uuid, s.user_id::uuid, s.created_at::timestamptz, s.expires_at::timestamptz
FROM import_sessions s
WHERE s.expires_at::timestamptz > now()
	AND s.user_id IN (SELECT user_id FROM import_users);

-- A claim may be empty in legacy rows; before claim columns existed, an
-- authenticated creator was recorded only as the slot's player ID. Names are
-- filled from the imported users below.
INSERT INTO games (game_id, initial_fen,
	white_player_id, white_type, white_level, white_search_time, white_claimed_by,
	black_player_id, black_type, black_level, black_search_time, black_claimed_by,
	start_time_utc, result, end_time_utc)
SELECT g.game_id::uuid, g.initial_fen,
	g.white_player_id::uuid, g.white_type, g.white_level, g.white_search_time,
	COALESCE(NULLIF(g.white_claimed_by, '')::uuid,
		(SELECT u.user_id::uuid FROM import_users u WHERE u.user_id = g.white_player_id)),
	g.black_player_id::uuid, g.black_type, g.black_level, g.black_search_time,
	COALESCE(NULLIF(g.black_claimed_by, '')::uuid,
		(SELECT u.user_id::uuid FROM import_users u WHERE u.user_id = g.black_player_id)),
	g.start_time_utc::timestamptz,
	NULLIF(g.result, ''),
	CASE WHEN NULLIF(g.result, '') IS NOT NULL
		THEN COALESCE(NULLIF(g.end_time_utc, '')::timestamptz, g.start_time_utc::timestamptz)
	END
FROM import_games g;

UPDATE games g SET
	white_name = (SELECT u.username FROM users u WHERE u.user_id = g.white_claimed_by),
	black_name = (SELECT u.username FROM users u WHERE u.user_id = g.black_claimed_by);

INSERT INTO moves (game_id, move_number, move_uci, fen_after_move, player_color, move_time_utc)
SELECT m.game_id::uuid, m.move_number, m.move_uci, m.fen_after_move, m.player_color,
	m.move_time_utc::timestamptz
FROM import_moves m
WHERE m.game_id IN (SELECT game_id FROM import_games);

-- Replay listings read the move count from the last move number, so every
-- game's line must be numbered 1..n without gaps.
DO $$
DECLARE broken integer;
BEGIN
	SELECT count(*) INTO broken FROM (
		SELECT game_id FROM moves GROUP BY game_id HAVING max(move_number) <> count(*)
	) gaps;
	IF broken > 0 THEN
		RAISE EXCEPTION '% game(s) have gaps in move numbering; import aborted', broken;
	END IF;
END $$;

SELECT format('imported: %s users (%s dropped), %s sessions (%s expired or orphaned), %s games (%s anonymous), %s moves (%s orphaned)',
	(SELECT count(*) FROM users), (SELECT count(*) FROM import_users) - (SELECT count(*) FROM users),
	(SELECT count(*) FROM sessions), (SELECT count(*) FROM import_sessions) - (SELECT count(*) FROM sessions),
	(SELECT count(*) FROM games),
	(SELECT count(*) FROM games WHERE white_claimed_by IS NULL AND black_claimed_by IS NULL),
	(SELECT count(*) FROM moves), (SELECT count(*) FROM import_moves) - (SELECT count(*) FROM moves)) AS summary
\gset
\echo :summary
COMMIT;
SQL
