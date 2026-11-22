package storage

import "time"

// UserRecord represents a user account in the database. Accounts created by
// public registration and by the CLI are identical and do not expire.
type UserRecord struct {
	UserID       string
	Username     string
	Email        string // empty when the account has no email (stored as NULL)
	PasswordHash string
	CreatedAt    time.Time
	LastLoginAt  *time.Time
}

// SessionRecord represents an active user session
type SessionRecord struct {
	SessionID string
	UserID    string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// GameRecord represents a row in the games table
type GameRecord struct {
	GameID          string
	InitialFEN      string
	WhitePlayerID   string
	WhiteType       int
	WhiteLevel      int
	WhiteSearchTime int
	WhiteClaimedBy  string
	WhiteName       string // claimant's username when the claim was recorded
	WhiteSeat       []byte // SHA-256 of the secret a guest holds the side with
	BlackPlayerID   string
	BlackType       int
	BlackLevel      int
	BlackSearchTime int
	BlackClaimedBy  string
	BlackName       string
	BlackSeat       []byte
	Result          string
	Termination     string // how Result was reached; empty while unfinished
	StartTimeUTC    time.Time
	EndTimeUTC      *time.Time
	// The first resignation or agreed draw, kept when an undo continues play;
	// ConcessionResult is empty when there was none.
	ConcessionResult      string
	ConcessionTermination string
	ConcessionPly         int
	ConcessionTimeUTC     *time.Time
	// Origin is how the game came to be stored: OriginPlayed, OriginImported
	// (by ImportedBy, until that account is deleted) or OriginArchive. Event,
	// Site and Round come from the PGN's tags.
	Origin     string
	ImportedBy string
	Event      string
	Site       string
	Round      string
	// Archive is set for an archive game read with its history or in a list.
	Archive *ArchiveRecord
}

const (
	OriginPlayed   = "played"
	OriginImported = "imported"
	OriginArchive  = "archive"
)

// FromPGN reports a game read from PGN, imported or archived: it is replayed
// and never played on.
func (r GameRecord) FromPGN() bool { return r.Origin == OriginImported || r.Origin == OriginArchive }

// CollectionRecord is a curated set of archive games and the rights it is
// published under: a collection is listed only once it names its source,
// licence and attribution. Games is its size, where a query counts it.
type CollectionRecord struct {
	Name          string // lowercase slug, the key
	Title         string
	SourceURL     string
	Licence       string
	Attribution   string
	Published     bool
	FeaturedOrder int // place among the collections; 0 when not featured
	Games         int
}

// ArchiveRecord is what the archive keeps of a game beside its row. A list
// fills only the collection's name; a history reads the whole collection.
type ArchiveRecord struct {
	Collection    CollectionRecord
	WhiteElo      int
	BlackElo      int
	ECO           string
	FeaturedOrder int // place among the featured games; 0 when not featured
}

// GameSummaryRecord is a game row plus its replay extent. MoveCount and
// FinalFEN come from the last move, so a picker needs no move-list read.
type GameSummaryRecord struct {
	GameRecord
	MoveCount int
	FinalFEN  string
}

// MoveRecord represents a row in the moves table
type MoveRecord struct {
	GameID       string
	MoveNumber   int
	MoveUCI      string
	FENAfterMove string
	PlayerColor  string
	MoveTimeUTC  time.Time
}

// MovePersistence groups changes caused by one accepted move so the move,
// first-move slot claim, and terminal result commit in one transaction.
type MovePersistence struct {
	Move        MoveRecord
	ClaimColor  string
	ClaimedBy   string
	Result      string
	Termination string
	EndTimeUTC  *time.Time
}

// schemaVersion is the newest schema this binary understands. Migrations are
// applied in order inside one transaction; PostgreSQL DDL is transactional, so
// a failed upgrade leaves the previous version intact.
const schemaVersion = 8

// migrations[v-1] upgrades the schema from version v-1 to v. Never edit a
// released migration; append a new one.
var migrations = []string{
	// v1: the initial schema. Usernames and emails are stored lowercase, so
	// plain unique constraints are case-insensitive without citext. A game
	// belongs to a user by claim, which survives reconfiguration; the name is
	// copied in when the claim is written, so replays keep it after a rename
	// or deletion. Games with no claim are purged after inactivity.
	`
CREATE TABLE schema_version (
	singleton  boolean PRIMARY KEY DEFAULT true CHECK (singleton),
	version    integer NOT NULL CHECK (version > 0),
	updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE users (
	user_id       uuid PRIMARY KEY,
	username      text NOT NULL,
	email         text,
	password_hash text NOT NULL,
	created_at    timestamptz NOT NULL DEFAULT now(),
	last_login_at timestamptz,
	CONSTRAINT users_username_key UNIQUE (username),
	CONSTRAINT users_email_key UNIQUE (email),
	CONSTRAINT users_username_check
		CHECK (username = lower(username) AND char_length(username) BETWEEN 1 AND 64),
	CONSTRAINT users_email_check
		CHECK (email = lower(email) AND char_length(email) BETWEEN 3 AND 254)
);

CREATE TABLE sessions (
	session_id uuid PRIMARY KEY,
	user_id    uuid NOT NULL REFERENCES users (user_id) ON DELETE CASCADE,
	created_at timestamptz NOT NULL DEFAULT now(),
	expires_at timestamptz NOT NULL,
	CONSTRAINT sessions_user_id_key UNIQUE (user_id)
);

CREATE TABLE games (
	game_id           uuid PRIMARY KEY,
	initial_fen       text NOT NULL CHECK (initial_fen <> ''),
	white_player_id   uuid NOT NULL,
	white_type        smallint NOT NULL CHECK (white_type IN (1, 2)),
	white_level       smallint NOT NULL DEFAULT 0 CHECK (white_level BETWEEN 0 AND 20),
	white_search_time integer NOT NULL DEFAULT 0 CHECK (white_search_time >= 0),
	white_claimed_by  uuid,
	white_name        text CHECK (char_length(white_name) BETWEEN 1 AND 64),
	black_player_id   uuid NOT NULL,
	black_type        smallint NOT NULL CHECK (black_type IN (1, 2)),
	black_level       smallint NOT NULL DEFAULT 0 CHECK (black_level BETWEEN 0 AND 20),
	black_search_time integer NOT NULL DEFAULT 0 CHECK (black_search_time >= 0),
	black_claimed_by  uuid,
	black_name        text CHECK (char_length(black_name) BETWEEN 1 AND 64),
	start_time_utc    timestamptz NOT NULL DEFAULT now(),
	result            text CHECK (result IN ('white_wins', 'black_wins', 'draw', 'stalemate')),
	end_time_utc      timestamptz,
	CONSTRAINT games_result_end_time_check CHECK ((result IS NULL) = (end_time_utc IS NULL))
);

-- Moves are numbered 1..n without gaps; the primary key serves ordered replay
-- reads and the last-move probe used by game listings.
CREATE TABLE moves (
	game_id        uuid NOT NULL REFERENCES games (game_id) ON DELETE CASCADE,
	move_number    integer NOT NULL CHECK (move_number > 0),
	move_uci       text NOT NULL CHECK (move_uci ~ '^[a-h][1-8][a-h][1-8][qrbn]?$'),
	fen_after_move text NOT NULL CHECK (fen_after_move <> ''),
	player_color   text NOT NULL CHECK (player_color IN ('w', 'b')),
	move_time_utc  timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (game_id, move_number)
);

CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);
-- Candidates for the anonymous-game purge: a game that started after the
-- cutoff cannot have been idle since it.
CREATE INDEX games_anonymous_start_idx ON games (start_time_utc)
	WHERE white_claimed_by IS NULL AND black_claimed_by IS NULL;
CREATE INDEX games_white_claimed_idx ON games (white_claimed_by, start_time_utc DESC, game_id DESC)
	WHERE white_claimed_by IS NOT NULL;
CREATE INDEX games_black_claimed_idx ON games (black_claimed_by, start_time_utc DESC, game_id DESC)
	WHERE black_claimed_by IS NOT NULL;
`,

	// v2: how a result was reached. Results before v2 came only from
	// checkmate and stalemate (the server never produced "draw"), so the
	// backfill is exact for them; a hand-written draw becomes "agreement".
	`
ALTER TABLE games ADD COLUMN termination text;
UPDATE games SET termination = CASE result
	WHEN 'stalemate' THEN 'stalemate'
	WHEN 'draw' THEN 'agreement'
	ELSE 'checkmate' END
WHERE result IS NOT NULL;
ALTER TABLE games
	ADD CONSTRAINT games_result_termination_check CHECK ((result IS NULL) = (termination IS NULL)),
	ADD CONSTRAINT games_termination_check CHECK (
		(result IN ('white_wins', 'black_wins') AND termination IN ('checkmate', 'resignation'))
		OR (result = 'stalemate' AND termination = 'stalemate')
		OR (result = 'draw' AND termination IN
			('insufficient_material', 'threefold_repetition', 'fifty_move_rule', 'agreement')));
`,

	// v3: the first resignation or agreed draw. An undo against the computer
	// may reopen such a game, which clears result and termination; these
	// columns keep the concession on record. Ply is the number of moves
	// played when it was made. Existing concessions are backfilled from the
	// current result and move count.
	`
ALTER TABLE games
	ADD COLUMN concession_result text,
	ADD COLUMN concession_termination text,
	ADD COLUMN concession_ply integer,
	ADD COLUMN concession_time_utc timestamptz;
UPDATE games g SET
	concession_result = g.result,
	concession_termination = g.termination,
	concession_ply = (SELECT count(*) FROM moves m WHERE m.game_id = g.game_id),
	concession_time_utc = g.end_time_utc
WHERE g.termination IN ('resignation', 'agreement');
ALTER TABLE games ADD CONSTRAINT games_concession_check CHECK (
	(concession_result IS NULL AND concession_termination IS NULL
		AND concession_ply IS NULL AND concession_time_utc IS NULL)
	OR (concession_ply >= 0 AND concession_time_utc IS NOT NULL AND (
		(concession_result IN ('white_wins', 'black_wins') AND concession_termination = 'resignation')
		OR (concession_result = 'draw' AND concession_termination = 'agreement'))));
`,

	// v4: password recovery. One pending reset per account; the token the
	// email carries is never stored, only its SHA-256, and it expires after
	// an hour. Consuming a token deletes the row.
	`
CREATE TABLE password_resets (
	user_id    uuid PRIMARY KEY REFERENCES users (user_id) ON DELETE CASCADE,
	token_hash bytea NOT NULL,
	created_at timestamptz NOT NULL DEFAULT now(),
	expires_at timestamptz NOT NULL,
	CONSTRAINT password_resets_token_hash_key UNIQUE (token_hash),
	CONSTRAINT password_resets_token_hash_check CHECK (octet_length(token_hash) = 32)
);
CREATE INDEX password_resets_expires_at_idx ON password_resets (expires_at);
`,

	// v5: imported games. A game read from PGN belongs to the account that
	// imported it (imported_by), has no claims, and keeps the PGN's event,
	// site and round for its export. Imported games are not anonymous, so
	// the purge index excludes them.
	`
ALTER TABLE games
	ADD COLUMN imported_by uuid,
	ADD COLUMN event text CHECK (char_length(event) BETWEEN 1 AND 200),
	ADD COLUMN site text CHECK (char_length(site) BETWEEN 1 AND 200),
	ADD COLUMN round text CHECK (char_length(round) BETWEEN 1 AND 40);
DROP INDEX games_anonymous_start_idx;
CREATE INDEX games_anonymous_start_idx ON games (start_time_utc)
	WHERE white_claimed_by IS NULL AND black_claimed_by IS NULL AND imported_by IS NULL;
CREATE INDEX games_imported_by_idx ON games (imported_by, start_time_utc DESC, game_id DESC)
	WHERE imported_by IS NOT NULL;
`,

	// v6: guest seats. A guest holds a side with a secret the server shows
	// once and stores as its SHA-256; an account's claim and a seat exclude
	// each other. A seat does not make a game a registered player's: the
	// anonymous purge still applies.
	`
ALTER TABLE games
	ADD COLUMN white_seat bytea,
	ADD COLUMN black_seat bytea,
	ADD CONSTRAINT games_white_seat_check
		CHECK (white_seat IS NULL OR (octet_length(white_seat) = 32 AND white_claimed_by IS NULL)),
	ADD CONSTRAINT games_black_seat_check
		CHECK (black_seat IS NULL OR (octet_length(black_seat) = 32 AND black_claimed_by IS NULL));
`,

	// v7: the curated archive. origin says how a game was stored; an import
	// disowned by its account's deletion keeps it. Archive games belong to a
	// collection, not an account, and the anonymous purge passes them by.
	// Search keys are lowercased by the server, so no locale decides a match.
	`
ALTER TABLE games ADD COLUMN origin text NOT NULL DEFAULT 'played'
	CONSTRAINT games_origin_check CHECK (origin IN ('played', 'imported', 'archive'));
UPDATE games SET origin = 'imported'
	WHERE imported_by IS NOT NULL OR event IS NOT NULL OR site IS NOT NULL OR round IS NOT NULL;
DROP INDEX games_anonymous_start_idx;
CREATE INDEX games_anonymous_start_idx ON games (start_time_utc)
	WHERE white_claimed_by IS NULL AND black_claimed_by IS NULL AND imported_by IS NULL
		AND origin <> 'archive';

CREATE TABLE collections (
	name           text PRIMARY KEY CHECK (name ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
	title          text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 200),
	source_url     text CHECK (char_length(source_url) BETWEEN 1 AND 500),
	licence        text CHECK (char_length(licence) BETWEEN 1 AND 200),
	attribution    text CHECK (char_length(attribution) BETWEEN 1 AND 500),
	published      boolean NOT NULL DEFAULT false,
	featured_order integer CHECK (featured_order > 0),
	created_at     timestamptz NOT NULL DEFAULT now(),
	CONSTRAINT collections_published_check CHECK (NOT published
		OR (source_url IS NOT NULL AND licence IS NOT NULL AND attribution IS NOT NULL))
);

-- start_time_utc copies the game's, which never changes, so one index orders
-- a collection's list. content_hash makes a repeated import store nothing.
CREATE TABLE archive_games (
	game_id        uuid PRIMARY KEY REFERENCES games (game_id) ON DELETE CASCADE,
	collection     text NOT NULL REFERENCES collections (name) ON UPDATE CASCADE,
	content_hash   bytea NOT NULL CHECK (octet_length(content_hash) = 32),
	start_time_utc timestamptz NOT NULL,
	white_key      text COLLATE "C",
	black_key      text COLLATE "C",
	white_elo      integer CHECK (white_elo BETWEEN 1 AND 9999),
	black_elo      integer CHECK (black_elo BETWEEN 1 AND 9999),
	eco            text COLLATE "C" CHECK (eco ~ '^[A-E][0-9][0-9]$'),
	featured_order integer CHECK (featured_order > 0),
	CONSTRAINT archive_games_content_hash_key UNIQUE (content_hash)
);
CREATE INDEX archive_games_collection_idx ON archive_games (collection, start_time_utc DESC, game_id DESC);
CREATE INDEX archive_games_start_idx ON archive_games (start_time_utc DESC, game_id DESC);
CREATE INDEX archive_games_eco_idx ON archive_games (eco) WHERE eco IS NOT NULL;
CREATE INDEX archive_games_white_key_idx ON archive_games (white_key) WHERE white_key IS NOT NULL;
CREATE INDEX archive_games_black_key_idx ON archive_games (black_key) WHERE black_key IS NOT NULL;
CREATE INDEX archive_games_featured_idx ON archive_games (featured_order) WHERE featured_order IS NOT NULL;
`,

	// v8: the record of password-reset mail, by the SHA-256 of the address it
	// went to, which outlives the account: deleting an account and registering
	// the address again buys no further mail. Rows older than a day are purged.
	`
CREATE TABLE reset_mails (
	address_hash bytea NOT NULL CHECK (octet_length(address_hash) = 32),
	sent_at      timestamptz NOT NULL
);
CREATE INDEX reset_mails_address_idx ON reset_mails (address_hash, sent_at);
CREATE INDEX reset_mails_sent_at_idx ON reset_mails (sent_at);
`,
}

// ownedTables lists every table created by migrations, children first.
var ownedTables = []string{
	"moves", "archive_games", "games", "collections", "reset_mails", "password_resets", "sessions", "users",
	"schema_version",
}
