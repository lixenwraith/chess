package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// MaxArchivePage bounds QueryArchive; callers request one extra row to detect
// a following page.
const MaxArchivePage = 101

var (
	// ErrCollectionRights refuses to publish a collection that does not name
	// its source, licence and attribution.
	ErrCollectionRights = errors.New("a published collection needs a source, a licence and an attribution")
	// ErrCollectionValue reports a field the schema refuses.
	ErrCollectionValue = errors.New("invalid collection value")

	collectionName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
)

// CollectionChange names what SetCollection writes: a nil field stays as it
// is, an empty string or a zero order clears one.
type CollectionChange struct {
	Title, SourceURL, Licence, Attribution *string
	Published                              *bool
	FeaturedOrder                          *int
}

// SetCollection changes the collection of that name, or creates it, which
// needs a title. It reports whether it created one.
func (s *Store) SetCollection(ctx context.Context, name string, change CollectionChange) (created bool, err error) {
	if !collectionName.MatchString(name) {
		return false, fmt.Errorf("%w: a name is 1 to 63 lowercase letters, digits and hyphens", ErrCollectionValue)
	}
	if source := change.SourceURL; source != nil && *source != "" {
		if link, err := url.Parse(*source); err != nil || link.Host == "" || (link.Scheme != "https" && link.Scheme != "http") {
			return false, fmt.Errorf("%w: a source is an http or https URL", ErrCollectionValue)
		}
	}
	ctx, cancel := opContext(ctx)
	defer cancel()
	result, err := s.db.ExecContext(ctx, `UPDATE collections SET
		title = COALESCE($2::text, title),
		source_url = CASE WHEN $3::text IS NULL THEN source_url ELSE NULLIF($3, '') END,
		licence = CASE WHEN $4::text IS NULL THEN licence ELSE NULLIF($4, '') END,
		attribution = CASE WHEN $5::text IS NULL THEN attribution ELSE NULLIF($5, '') END,
		published = COALESCE($6::boolean, published),
		featured_order = CASE WHEN $7::integer IS NULL THEN featured_order ELSE NULLIF($7, 0) END
		WHERE name = $1`,
		name, change.Title, change.SourceURL, change.Licence, change.Attribution, change.Published, change.FeaturedOrder)
	if err != nil {
		return false, collectionError(err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed > 0 {
		return false, err
	}
	if change.Title == nil {
		return false, fmt.Errorf("%w: a new collection needs a title", ErrCollectionValue)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO collections (
		name, title, source_url, licence, attribution, published, featured_order
	) VALUES ($1, $2, NULLIF($3::text, ''), NULLIF($4::text, ''), NULLIF($5::text, ''),
		COALESCE($6::boolean, false), NULLIF($7::integer, 0))`,
		name, change.Title, change.SourceURL, change.Licence, change.Attribution, change.Published, change.FeaturedOrder)
	return err == nil, collectionError(err)
}

func collectionError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		return err
	}
	if pgErr.ConstraintName == "collections_published_check" {
		return ErrCollectionRights
	}
	return fmt.Errorf("%w: %s", ErrCollectionValue, pgErr.ConstraintName)
}

// Collections lists the collections with their sizes, the featured first in
// their order, then by title; publishedOnly leaves out those not listed
// publicly.
func (s *Store) Collections(ctx context.Context, publishedOnly bool) ([]CollectionRecord, error) {
	ctx, cancel := opContext(ctx)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT c.name, c.title, c.source_url, c.licence, c.attribution,
		c.published, c.featured_order, (SELECT count(*) FROM archive_games a WHERE a.collection = c.name)
		FROM collections c WHERE c.published OR NOT $1
		ORDER BY c.featured_order NULLS LAST, c.title, c.name`, publishedOnly)
	if err != nil {
		return nil, fmt.Errorf("query collections: %w", err)
	}
	defer rows.Close()
	collections := make([]CollectionRecord, 0)
	for rows.Next() {
		var c CollectionRecord
		if err := scanCollection(rows, &c, &c.Games); err != nil {
			return nil, fmt.Errorf("scan collection: %w", err)
		}
		collections = append(collections, c)
	}
	return collections, rows.Err()
}

// scanCollection scans name, title, the three rights fields, published and
// the featured order, followed by any extra destinations.
func scanCollection(scanner rowScanner, c *CollectionRecord, extra ...any) error {
	var source, licence, attribution sql.NullString
	var featured sql.NullInt64
	dest := []any{&c.Name, &c.Title, &source, &licence, &attribution, &c.Published, &featured}
	if err := scanner.Scan(append(dest, extra...)...); err != nil {
		return err
	}
	c.SourceURL, c.Licence, c.Attribution = source.String, licence.String, attribution.String
	c.FeaturedOrder = int(featured.Int64)
	return nil
}

// DeleteCollection removes a collection and its games, a thousand games a
// transaction, and returns how many games went. sql.ErrNoRows means there is
// no such collection.
func (s *Store) DeleteCollection(ctx context.Context, name string) (games int64, err error) {
	for {
		batch, cancel := context.WithTimeout(ctx, writeTimeout)
		result, err := s.db.ExecContext(batch, `DELETE FROM games WHERE game_id IN (
			SELECT game_id FROM archive_games WHERE collection = $1 LIMIT 1000)`, name)
		cancel()
		if err != nil {
			return games, fmt.Errorf("delete archive games: %w", err)
		}
		deleted, err := result.RowsAffected()
		if err != nil {
			return games, err
		}
		if games += deleted; deleted == 0 {
			break
		}
	}
	ctx, cancel := opContext(ctx)
	defer cancel()
	result, err := s.db.ExecContext(ctx, `DELETE FROM collections WHERE name = $1`, name)
	if err != nil {
		return games, fmt.Errorf("delete collection: %w", err)
	}
	if deleted, _ := result.RowsAffected(); deleted == 0 {
		return games, sql.ErrNoRows
	}
	return games, nil
}

// ArchiveGame is a game for ImportArchiveGames: its row and line, the hash
// that identifies its content, and its archive row but for the collection.
// The keys are the players' names as a search compares them.
type ArchiveGame struct {
	Record             GameRecord
	Moves              []MoveRecord
	Hash               []byte
	WhiteKey, BlackKey string
	WhiteElo, BlackElo int
	ECO                string
}

// ArchivedHashes reports which of the content hashes the archive holds, keyed
// by the hash as a string.
func (s *Store) ArchivedHashes(ctx context.Context, hashes [][]byte) (map[string]bool, error) {
	ctx, cancel := opContext(ctx)
	defer cancel()
	rows, err := s.db.QueryContext(ctx,
		`SELECT content_hash FROM archive_games WHERE content_hash = ANY($1::bytea[])`, hashes)
	if err != nil {
		return nil, fmt.Errorf("query archive hashes: %w", err)
	}
	defer rows.Close()
	held := make(map[string]bool)
	for rows.Next() {
		var hash []byte
		if err := rows.Scan(&hash); err != nil {
			return nil, fmt.Errorf("scan archive hash: %w", err)
		}
		held[string(hash)] = true
	}
	return held, rows.Err()
}

// ImportArchiveGames stores the games in the collection in one transaction and
// returns once it has committed. A hash the archive already holds fails the
// whole batch: callers leave those out (ArchivedHashes).
func (s *Store) ImportArchiveGames(ctx context.Context, collection string, games []ArchiveGame) error {
	for i := range games {
		games[i].Record.Origin, games[i].Record.ImportedBy = OriginArchive, ""
		if err := checkWholeGame(games[i].Record, games[i].Moves); err != nil {
			return fmt.Errorf("game %d: %w", i+1, err)
		}
	}
	return s.enqueueWait(ctx, "import_archive_games", func(ctx context.Context, tx *sql.Tx) error {
		for _, game := range games {
			if err := insertWholeGame(ctx, tx, game.Record, game.Moves); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO archive_games (
				game_id, collection, content_hash, start_time_utc, white_key, black_key, white_elo, black_elo, eco
			) VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7::integer, 0), NULLIF($8::integer, 0), $9)`,
				game.Record.GameID, collection, game.Hash, game.Record.StartTimeUTC,
				nullableString(game.WhiteKey), nullableString(game.BlackKey),
				game.WhiteElo, game.BlackElo, nullableString(game.ECO),
			); err != nil {
				return err
			}
		}
		slog.Debug("storage archive games imported", "collection", collection, "count", len(games))
		return nil
	})
}

// FeatureArchiveGame gives an archive game its place among the featured, or
// with order 0 takes it away. sql.ErrNoRows means it is not an archive game.
func (s *Store) FeatureArchiveGame(ctx context.Context, gameID string, order int) error {
	if !validUUID(gameID) {
		return sql.ErrNoRows
	}
	ctx, cancel := opContext(ctx)
	defer cancel()
	result, err := s.db.ExecContext(ctx,
		`UPDATE archive_games SET featured_order = NULLIF($2::integer, 0) WHERE game_id = $1`, gameID, order)
	if err != nil {
		return fmt.Errorf("feature archive game: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// archiveOf reads a game's archive row with its collection.
func archiveOf(ctx context.Context, tx *sql.Tx, gameID string) (*ArchiveRecord, error) {
	var a ArchiveRecord
	var whiteElo, blackElo, featured sql.NullInt64
	var eco sql.NullString
	err := scanCollection(tx.QueryRowContext(ctx, `SELECT c.name, c.title, c.source_url, c.licence,
		c.attribution, c.published, c.featured_order, a.white_elo, a.black_elo, a.eco, a.featured_order
		FROM archive_games a JOIN collections c ON c.name = a.collection WHERE a.game_id = $1`, gameID),
		&a.Collection, &whiteElo, &blackElo, &eco, &featured)
	if err != nil {
		return nil, err
	}
	a.WhiteElo, a.BlackElo, a.ECO, a.FeaturedOrder = int(whiteElo.Int64), int(blackElo.Int64), eco.String, int(featured.Int64)
	return &a, nil
}

// ArchiveQuery selects a page of the published archive, newest first. After
// continues below that key. ECO and Player are prefixes, Player of a key (a
// lowercased name) on either side. Featured lists the featured games in
// their order instead, without a cursor.
type ArchiveQuery struct {
	Limit      int
	After      *GameCursor
	Collection string
	ECO        string
	Player     string
	Featured   bool
}

// prefixEnd bounds, in "C" order, every string that starts with prefix.
func prefixEnd(prefix string) string { return prefix + "\U0010FFFF" }

// QueryArchive returns a page of the games of published collections, each
// with its last position and its archive row.
func (s *Store) QueryArchive(ctx context.Context, q ArchiveQuery) ([]GameSummaryRecord, error) {
	if q.Limit < 1 || q.Limit > MaxArchivePage {
		return nil, fmt.Errorf("limit from 1 to %d is required", MaxArchivePage)
	}
	if q.After != nil && (q.Featured || !validUUID(q.After.GameID)) {
		return nil, errors.New("a cursor requires a valid game ID and no featured order")
	}
	args := []any{q.Limit}
	arg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	where := []string{"c.published"}
	if q.Collection != "" {
		where = append(where, "a.collection = "+arg(q.Collection))
	}
	if q.ECO != "" {
		where = append(where, "a.eco >= "+arg(q.ECO)+" AND a.eco < "+arg(prefixEnd(q.ECO)))
	}
	if q.Player != "" {
		low, high := arg(q.Player), arg(prefixEnd(q.Player))
		where = append(where, "(a.white_key >= "+low+" AND a.white_key < "+high+
			" OR a.black_key >= "+low+" AND a.black_key < "+high+")")
	}
	order := "a.start_time_utc DESC, a.game_id DESC"
	if q.Featured {
		where = append(where, "a.featured_order IS NOT NULL")
		order = "a.featured_order, a.game_id"
	}
	if q.After != nil {
		where = append(where, "(a.start_time_utc, a.game_id) < ("+arg(q.After.StartTimeUTC)+", "+arg(q.After.GameID)+")")
	}

	started := time.Now()
	ctx, cancel := opContext(ctx)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT `+gameSelectColumns+`,
		COALESCE(last.move_number, 0), COALESCE(last.fen_after_move, g.initial_fen),
		a.collection, a.white_elo, a.black_elo, a.eco, a.featured_order
		FROM archive_games a
		JOIN collections c ON c.name = a.collection
		JOIN games g ON g.game_id = a.game_id
		LEFT JOIN LATERAL (
			SELECT m.move_number, m.fen_after_move FROM moves m
			WHERE m.game_id = g.game_id
			ORDER BY m.move_number DESC
			LIMIT 1
		) last ON true
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY `+order+`
		LIMIT $1`, args...)
	if err != nil {
		return nil, fmt.Errorf("query archive: %w", err)
	}
	defer rows.Close()
	games := make([]GameSummaryRecord, 0)
	for rows.Next() {
		var summary GameSummaryRecord
		var a ArchiveRecord
		var whiteElo, blackElo, featured sql.NullInt64
		var eco sql.NullString
		if err := scanGame(rows, &summary.GameRecord, &summary.MoveCount, &summary.FinalFEN,
			&a.Collection.Name, &whiteElo, &blackElo, &eco, &featured); err != nil {
			return nil, fmt.Errorf("scan archive game: %w", err)
		}
		a.WhiteElo, a.BlackElo, a.ECO, a.FeaturedOrder = int(whiteElo.Int64), int(blackElo.Int64), eco.String, int(featured.Int64)
		summary.Archive = &a
		games = append(games, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate archive: %w", err)
	}
	slog.Debug("storage archive queried", "count", len(games), "limit", q.Limit, "duration", time.Since(started))
	return games, nil
}
