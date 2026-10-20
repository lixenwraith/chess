package service

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lixenwraith/chess/internal/server/chess"
	"github.com/lixenwraith/chess/internal/server/core"
	"github.com/lixenwraith/chess/internal/server/storage"

	"github.com/google/uuid"
)

// ErrInvalidPGN wraps chess.ErrInvalidPGN and move-legality failures of an
// import; the message says what was wrong.
var ErrInvalidPGN = chess.ErrInvalidPGN

// Imported is what ImportGame reports.
type Imported struct {
	GameID string
	Plies  int
	Result string // stored result, "" while unfinished
}

// ImportGame stores a game read from PGN for userID: tags give the names,
// event, site, round and date; every move is replayed by the rules core and
// refused if illegal; the result comes from the PGN's result token, its
// termination from the final position (mate, stalemate, a dead position,
// repetition or the fifty-move rule) or else resignation or agreement.
func (s *Service) ImportGame(userID, text string) (*Imported, error) {
	if s.store == nil {
		return nil, ErrStorageDisabled
	}
	if uuid.Validate(userID) != nil {
		return nil, errors.New("an import needs a signed-in user")
	}
	game, err := chess.ParsePGN(text)
	if err != nil {
		return nil, err
	}

	startFEN := chess.StartFEN
	if fen := game.Tag("FEN"); fen != "" || game.Tag("SetUp") == "1" {
		if fen == "" {
			return nil, fmt.Errorf("%w: SetUp without a FEN tag", ErrInvalidPGN)
		}
		normalized, err := chess.NormalizeFEN(fen)
		if err != nil {
			return nil, fmt.Errorf("%w: FEN tag: %v", ErrInvalidPGN, err)
		}
		startFEN = normalized
	}

	// Replay the moves, keeping the position after each ply.
	pos, err := chess.ParseFEN(startFEN)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPGN, err)
	}
	gameID := uuid.NewString()
	start := importStartTime(game)
	moves := make([]storage.MoveRecord, 0, len(game.SAN))
	positions := []*chess.Position{pos}
	for i, san := range game.SAN {
		m, err := pos.ParseSAN(san)
		if err != nil {
			return nil, fmt.Errorf("%w: ply %d (%s): %v", ErrInvalidPGN, i+1, san, err)
		}
		color := "w"
		if pos.Turn() == chess.Black {
			color = "b"
		}
		pos = pos.Play(m)
		positions = append(positions, pos)
		moves = append(moves, storage.MoveRecord{
			GameID: gameID, MoveNumber: i + 1, MoveUCI: m.UCI(), FENAfterMove: pos.FEN(),
			PlayerColor: color, MoveTimeUTC: start,
		})
	}

	result, termination, err := importOutcome(game.Result, positions)
	if err != nil {
		return nil, err
	}
	record := storage.GameRecord{
		GameID:        gameID,
		InitialFEN:    startFEN,
		WhitePlayerID: uuid.NewString(),
		WhiteType:     int(core.PlayerHuman),
		WhiteName:     importName(game.Tag("White")),
		BlackPlayerID: uuid.NewString(),
		BlackType:     int(core.PlayerHuman),
		BlackName:     importName(game.Tag("Black")),
		Result:        result,
		Termination:   termination,
		StartTimeUTC:  start,
		ImportedBy:    userID,
		Event:         importTag(game.Tag("Event"), 200),
		Site:          importTag(game.Tag("Site"), 200),
		Round:         importTag(game.Tag("Round"), 40),
	}
	if result != "" {
		end := start
		record.EndTimeUTC = &end
	}
	if err := s.store.ImportGame(record, moves); err != nil {
		return nil, fmt.Errorf("%w: import game: %v", ErrStorageUnavailable, err)
	}
	slog.Info("game imported", "game_id", gameID, "user_id", userID, "plies", len(moves), "result", result)
	return &Imported{GameID: gameID, Plies: len(moves), Result: result}, nil
}

// importOutcome maps the PGN result to the stored result and a termination
// the final position supports.
func importOutcome(token string, positions []*chess.Position) (result, termination string, err error) {
	final := positions[len(positions)-1]
	mate := final.InCheck() && !final.HasLegalMoves()
	stalemate := !final.InCheck() && !final.HasLegalMoves()
	switch token {
	case "1-0", "0-1":
		result = "white_wins"
		if token == "0-1" {
			result = "black_wins"
		}
		// A decisive result against the side to move at mate; otherwise the
		// loser resigned (or lost on time, which the schema has no word for).
		if mate && ((token == "1-0") == (final.Turn() == chess.Black)) {
			return result, string(core.TermCheckmate), nil
		}
		if mate || stalemate {
			return "", "", fmt.Errorf("%w: result %s does not match the final position", ErrInvalidPGN, token)
		}
		return result, string(core.TermResignation), nil
	case "1/2-1/2":
		switch {
		case mate:
			return "", "", fmt.Errorf("%w: result 1/2-1/2 but the final position is checkmate", ErrInvalidPGN)
		case stalemate:
			return "stalemate", string(core.TermStalemate), nil
		case final.InsufficientMaterial():
			return "draw", string(core.TermInsufficientMaterial), nil
		case chess.Repetitions(positions) >= 3:
			return "draw", string(core.TermThreefoldRepetition), nil
		case final.Halfmove() >= 100:
			return "draw", string(core.TermFiftyMoveRule), nil
		}
		return "draw", string(core.TermAgreement), nil
	}
	if mate || stalemate {
		return "", "", fmt.Errorf("%w: the game is over but the result is *", ErrInvalidPGN)
	}
	return "", "", nil
}

// importName is a player name from a tag: "?" and empty mean unknown.
func importName(value string) string {
	value = strings.TrimSpace(value)
	if value == "?" || value == "" {
		return ""
	}
	if utf8.RuneCountInString(value) > 64 {
		value = string([]rune(value)[:64])
	}
	return value
}

func importTag(value string, limit int) string {
	value = strings.TrimSpace(value)
	if value == "?" || value == "-" && limit == 40 || value == "" {
		return ""
	}
	if utf8.RuneCountInString(value) > limit {
		value = string([]rune(value)[:limit])
	}
	return value
}

// importStartTime reads UTCDate/UTCTime, else Date, else now. Unknown parts
// ("????.??.??") fall back to the current time.
func importStartTime(game *chess.ParsedPGN) time.Time {
	now := time.Now().UTC()
	date := game.Tag("UTCDate")
	if date == "" {
		date = game.Tag("Date")
	}
	parts := strings.Split(date, ".")
	if len(parts) != 3 {
		return now
	}
	year, err1 := strconv.Atoi(parts[0])
	month, err2 := strconv.Atoi(parts[1])
	day, err3 := strconv.Atoi(parts[2])
	if err1 != nil || year < 1400 || year > now.Year()+1 {
		return now
	}
	if err2 != nil || month < 1 || month > 12 {
		month = 1
	}
	if err3 != nil || day < 1 || day > 31 {
		day = 1
	}
	hour, minute, second := 0, 0, 0
	if clock := game.Tag("UTCTime"); clock != "" {
		if t, err := time.Parse("15:04:05", clock); err == nil {
			hour, minute, second = t.Clock()
		}
	}
	t := time.Date(year, time.Month(month), day, hour, minute, second, 0, time.UTC)
	if t.After(now.Add(24 * time.Hour)) {
		return now
	}
	return t
}

// RemoveGame removes a stored game from the user's list: the game is deleted
// when the user alone holds it, or the user's claim is released when another
// user claims a side (see storage.RemoveGameForUser). A deleted game is also
// unloaded from memory; a released one keeps playing for the other side.
func (s *Service) RemoveGame(gameID, userID string) (storage.RemovalOutcome, error) {
	if s.store == nil {
		return "", ErrStorageDisabled
	}
	outcome, err := s.store.RemoveGameForUser(gameID, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("%w: %s", ErrGameNotFound, gameID)
		}
		return "", fmt.Errorf("%w: remove game: %v", ErrStorageUnavailable, err)
	}
	s.mu.Lock()
	if g, ok := s.games[gameID]; ok {
		switch outcome {
		case storage.RemovedGame:
			delete(s.games, gameID)
		case storage.RemovedClaim:
			g.ReleaseUser(userID)
		}
	}
	s.mu.Unlock()
	if outcome == storage.RemovedGame {
		s.waiter.RemoveGame(gameID)
	}
	slog.Info("game removed for user", "game_id", gameID, "user_id", userID, "outcome", outcome)
	return outcome, nil
}
