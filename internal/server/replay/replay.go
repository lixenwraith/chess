// Package replay works on stored games: it derives SAN for their moves,
// exports them as PGN, and checks them against the rules. It reads the
// records that storage returns and never writes.
package replay

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/lixenwraith/chess/internal/server/chess"
	"github.com/lixenwraith/chess/internal/server/core"
	"github.com/lixenwraith/chess/internal/server/storage"
)

var (
	// ErrPlyOutOfRange reports a PGN ply beyond the stored line.
	ErrPlyOutOfRange = errors.New("ply out of range")
	// ErrNotation reports a stored move the rules core cannot notate.
	ErrNotation = errors.New("cannot derive notation")
)

// Notate derives SAN for each stored move from the stored position before
// it, so one unparsable ply does not affect the others. Plies it cannot
// notate are left empty and the first failure is returned.
func Notate(initialFEN string, moves []storage.MoveRecord) ([]string, error) {
	san := make([]string, len(moves))
	before := initialFEN
	var firstErr error
	for i, move := range moves {
		pos, err := chess.ParseFEN(before)
		if err == nil {
			var m chess.Move
			if m, err = pos.ParseUCI(move.MoveUCI); err == nil {
				san[i] = pos.SAN(m)
			}
		}
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("%w: ply %d (%s): %v", ErrNotation, move.MoveNumber, move.MoveUCI, err)
		}
		before = move.FENAfterMove
	}
	return san, firstErr
}

// PGN is an export and its suggested download name.
type PGN struct {
	Text     string
	Filename string
}

// BuildPGN exports a stored game. ply < 0 exports every move; otherwise the
// first ply moves, with result "*" and no termination when that stops short
// of the end of the line.
func BuildPGN(record *storage.GameRecord, moves []storage.MoveRecord, ply int) (*PGN, error) {
	if ply > len(moves) {
		return nil, fmt.Errorf("%w: game has %d plies", ErrPlyOutOfRange, len(moves))
	}
	san, err := Notate(record.InitialFEN, moves)
	if err != nil {
		return nil, err
	}

	complete := ply < 0 || ply == len(moves)
	if !complete {
		san = san[:ply]
	}
	result, comment := "*", ""
	if complete {
		result = chess.ResultToken(record.Result)
		comment = Describe(record.Result, record.Termination)
		if note := continuedAfter(record, len(moves)); note != "" {
			comment = strings.TrimSpace(note + " " + comment)
		}
	}
	start := record.StartTimeUTC.UTC()
	tags := []chess.Tag{
		{Name: "Event", Value: "Casual game"},
		{Name: "Date", Value: start.Format("2006.01.02")},
		{Name: "White", Value: playerName(record.WhiteType, record.WhiteLevel, record.WhiteName)},
		{Name: "Black", Value: playerName(record.BlackType, record.BlackLevel, record.BlackName)},
		{Name: "GameId", Value: record.GameID},
		{Name: "UTCDate", Value: start.Format("2006.01.02")},
		{Name: "UTCTime", Value: start.Format("15:04:05")},
		{Name: "WhiteType", Value: playerType(record.WhiteType)},
		{Name: "BlackType", Value: playerType(record.BlackType)},
		{Name: "PlyCount", Value: strconv.Itoa(len(san))},
	}
	if complete {
		// PGN's Termination values describe how play stopped; mate, draws,
		// and resignation are all "normal". The reason goes in the comment.
		value := "unterminated"
		if record.Result != "" {
			value = "normal"
		}
		tags = append(tags, chess.Tag{Name: "Termination", Value: value})
	}

	name := fmt.Sprintf("chess-%s-%s", start.Format("20060102"), record.GameID[:8])
	if !complete {
		name += fmt.Sprintf("-ply%d", ply)
	}
	return &PGN{
		Text: chess.PGN{
			Tags: tags, StartFEN: record.InitialFEN, SAN: san, Comment: comment, Result: result,
		}.String(),
		Filename: name + ".pgn",
	}, nil
}

// continuedAfter notes a resignation or agreed draw that an undo took back,
// such as "White resigned at ply 24; play continued."; empty when there was
// none or it is still the game's result.
func continuedAfter(record *storage.GameRecord, plies int) string {
	if record.ConcessionResult == "" {
		return ""
	}
	if record.ConcessionResult == record.Result &&
		record.ConcessionTermination == record.Termination && record.ConcessionPly == plies {
		return ""
	}
	var event string
	switch record.ConcessionResult {
	case "white_wins":
		event = "Black resigned"
	case "black_wins":
		event = "White resigned"
	default:
		event = "A draw was agreed"
	}
	return fmt.Sprintf("%s at ply %d; play continued.", event, record.ConcessionPly)
}

// Describe returns a sentence for a stored result and termination, such as
// "Black wins by resignation." or "Draw by threefold repetition."; empty for
// an unfinished game.
func Describe(result, termination string) string {
	winner := ""
	switch result {
	case "white_wins":
		winner = "White"
	case "black_wins":
		winner = "Black"
	case "":
		return ""
	}
	switch core.Termination(termination) {
	case core.TermCheckmate:
		return winner + " wins by checkmate."
	case core.TermResignation:
		return winner + " wins by resignation."
	case core.TermStalemate:
		return "Draw by stalemate."
	case core.TermInsufficientMaterial:
		return "Draw by insufficient material."
	case core.TermThreefoldRepetition:
		return "Draw by threefold repetition."
	case core.TermFiftyMoveRule:
		return "Draw by the fifty-move rule."
	case core.TermAgreement:
		return "Draw by agreement."
	}
	return ""
}

func playerName(playerType, level int, name string) string {
	switch {
	case core.PlayerType(playerType) == core.PlayerComputer:
		return "Stockfish level " + strconv.Itoa(level)
	case name != "":
		return name
	}
	return "Anonymous"
}

func playerType(t int) string {
	if core.PlayerType(t) == core.PlayerComputer {
		return "program"
	}
	return "human"
}
