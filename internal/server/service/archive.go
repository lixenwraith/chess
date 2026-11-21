package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lixenwraith/chess/internal/server/chess"
	"github.com/lixenwraith/chess/internal/server/core"
	"github.com/lixenwraith/chess/internal/server/storage"
)

// archiveBatch is how many games one transaction of a bulk import stores.
const archiveBatch = 100

// ErrCollectionNotFound reports an import into a collection that does not exist.
var ErrCollectionNotFound = errors.New("collection not found")

var (
	ecoCode   = regexp.MustCompile(`^[A-E][0-9][0-9]`)
	ecoPrefix = regexp.MustCompile(`^[A-E][0-9]{0,2}$`)
	slugName  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
)

// ArchiveImport counts what ImportArchive did with a PGN database. Stored is
// what a dry run would have stored.
type ArchiveImport struct {
	Games     int
	Stored    int
	Duplicate int
	Rejected  []ArchiveReject
}

// ArchiveReject is a game left out: its place in the database, from 1, and why.
type ArchiveReject struct {
	Index  int
	Reason string
}

// ImportArchive reads a PGN database into a collection, a batch a
// transaction. A game the rules refuse is skipped with its reason; one whose
// content the archive holds, or that came earlier in this database, is a
// duplicate, so a repeated import stores only what is new. A dry run reads,
// replays and counts without writing.
func (s *Service) ImportArchive(ctx context.Context, collection string, database io.Reader, dryRun bool) (ArchiveImport, error) {
	var done ArchiveImport
	if s.store == nil {
		return done, ErrStorageDisabled
	}
	collections, err := s.store.Collections(ctx, false)
	if err != nil {
		return done, err
	}
	known := false
	for _, c := range collections {
		known = known || c.Name == collection
	}
	if !known {
		return done, fmt.Errorf("%w: %s", ErrCollectionNotFound, collection)
	}

	seen := make(map[string]bool)
	batch := make([]storage.ArchiveGame, 0, archiveBatch)
	store := func() error {
		hashes := make([][]byte, len(batch))
		for i, game := range batch {
			hashes[i] = game.Hash
		}
		held, err := s.store.ArchivedHashes(ctx, hashes)
		if err != nil {
			return err
		}
		fresh := batch[:0]
		for _, game := range batch {
			if !held[string(game.Hash)] {
				fresh = append(fresh, game)
			}
		}
		done.Duplicate += len(batch) - len(fresh)
		batch = batch[:0]
		if !dryRun && len(fresh) > 0 {
			if err := s.store.ImportArchiveGames(ctx, collection, fresh); err != nil {
				return err
			}
		}
		done.Stored += len(fresh)
		return nil
	}
	for text, err := range chess.SplitPGN(database) {
		if err != nil && !errors.Is(err, chess.ErrInvalidPGN) {
			return done, fmt.Errorf("read PGN database: %w", err)
		}
		done.Games++
		var game storage.ArchiveGame
		if err == nil {
			game, err = archiveGame(text)
		}
		switch {
		case err != nil:
			done.Rejected = append(done.Rejected, ArchiveReject{
				Index: done.Games, Reason: strings.TrimPrefix(err.Error(), "invalid PGN: "),
			})
		case seen[string(game.Hash)]:
			done.Duplicate++
		default:
			seen[string(game.Hash)] = true
			if batch = append(batch, game); len(batch) == archiveBatch {
				if err := store(); err != nil {
					return done, err
				}
			}
		}
	}
	return done, store()
}

// archiveGame reads one PGN game as the archive stores it. Its hash covers
// what identifies the game: the start, the line, the result and the tags of
// the roster that name the occasion and the players.
func archiveGame(text string) (storage.ArchiveGame, error) {
	pgn, record, moves, err := readPGNGame(text)
	if err != nil {
		return storage.ArchiveGame{}, err
	}
	identity := []string{record.InitialFEN, "", pgn.Result}
	for _, move := range moves {
		identity[1] += move.MoveUCI + " "
	}
	for _, tag := range []string{"Event", "Site", "Date", "Round", "White", "Black"} {
		identity = append(identity, strings.TrimSpace(pgn.Tag(tag)))
	}
	hash := sha256.Sum256([]byte(strings.Join(identity, "\n")))
	game := storage.ArchiveGame{
		Record: record, Moves: moves, Hash: hash[:],
		WhiteKey: strings.ToLower(record.WhiteName), BlackKey: strings.ToLower(record.BlackName),
		WhiteElo: importElo(pgn.Tag("WhiteElo")), BlackElo: importElo(pgn.Tag("BlackElo")),
		ECO: ecoCode.FindString(strings.ToUpper(strings.TrimSpace(pgn.Tag("ECO")))),
	}
	return game, nil
}

// importElo is a rating tag's number; 0 for "?", "-" and anything else.
func importElo(value string) int {
	elo, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || elo < 1 || elo > 9999 {
		return 0
	}
	return elo
}

func archiveInfo(a *storage.ArchiveRecord) *core.ArchiveInfo {
	if a == nil {
		return nil
	}
	return &core.ArchiveInfo{
		Collection: a.Collection.Name, Title: a.Collection.Title, SourceURL: a.Collection.SourceURL,
		Licence: a.Collection.Licence, Attribution: a.Collection.Attribution,
		WhiteElo: a.WhiteElo, BlackElo: a.BlackElo, ECO: a.ECO,
	}
}

// ArchiveOptions selects a page of the published archive. ECO is a prefix of
// an opening code ("B", "B9", "B90"), Player a prefix of either player's name
// in any case, at least two characters. Featured lists the featured games in
// their order, without a cursor.
type ArchiveOptions struct {
	Limit      int
	Cursor     string
	Collection string
	ECO        string
	Player     string
	Featured   bool
}

// GetArchive returns a page of the games of published collections.
func (s *Service) GetArchive(ctx context.Context, opts ArchiveOptions) (*core.ArchiveListResponse, error) {
	if s.store == nil {
		return nil, ErrStorageDisabled
	}
	query := storage.ArchiveQuery{
		Limit: opts.Limit + 1, Collection: opts.Collection, Featured: opts.Featured,
		ECO: strings.ToUpper(strings.TrimSpace(opts.ECO)), Player: strings.ToLower(strings.TrimSpace(opts.Player)),
	}
	switch {
	case query.Collection != "" && !slugName.MatchString(query.Collection):
		return nil, fmt.Errorf("%w: collection is a lowercase name", ErrInvalidListQuery)
	case query.ECO != "" && !ecoPrefix.MatchString(query.ECO):
		return nil, fmt.Errorf("%w: eco is a prefix of a code from A00 to E99", ErrInvalidListQuery)
	case query.Player != "" && (utf8.RuneCountInString(query.Player) < 2 || utf8.RuneCountInString(query.Player) > 64):
		return nil, fmt.Errorf("%w: player is 2 to 64 characters", ErrInvalidListQuery)
	case opts.Cursor != "" && opts.Featured:
		return nil, fmt.Errorf("%w: a featured list has no cursor", ErrInvalidListQuery)
	}
	if opts.Cursor != "" {
		after, err := decodeCursor(opts.Cursor)
		if err != nil {
			return nil, err
		}
		query.After = after
	}

	records, err := s.store.QueryArchive(ctx, query)
	if err != nil {
		if isStorageUnavailable(err) {
			return nil, fmt.Errorf("%w: %v", ErrStorageUnavailable, err)
		}
		return nil, fmt.Errorf("get archive: %w", err)
	}
	response := &core.ArchiveListResponse{Games: make([]core.GameSummary, 0, len(records)), Limit: opts.Limit}
	if len(records) > opts.Limit {
		records = records[:opts.Limit]
		if last := records[len(records)-1]; !opts.Featured {
			response.NextCursor = encodeCursor(last.StartTimeUTC, last.GameID)
		}
	}
	for _, record := range records {
		response.Games = append(response.Games, gameSummary(record))
	}
	return response, nil
}

// GetCollections lists the published collections.
func (s *Service) GetCollections(ctx context.Context) (*core.CollectionListResponse, error) {
	if s.store == nil {
		return nil, ErrStorageDisabled
	}
	records, err := s.store.Collections(ctx, true)
	if err != nil {
		if isStorageUnavailable(err) {
			return nil, fmt.Errorf("%w: %v", ErrStorageUnavailable, err)
		}
		return nil, fmt.Errorf("get collections: %w", err)
	}
	response := &core.CollectionListResponse{Collections: make([]core.Collection, 0, len(records))}
	for _, c := range records {
		response.Collections = append(response.Collections, core.Collection{
			Name: c.Name, Title: c.Title, SourceURL: c.SourceURL, Licence: c.Licence,
			Attribution: c.Attribution, Games: c.Games,
		})
	}
	return response, nil
}
