package replay

import (
	"fmt"

	"github.com/lixenwraith/chess/internal/server/chess"
	"github.com/lixenwraith/chess/internal/server/core"
	"github.com/lixenwraith/chess/internal/server/storage"
)

// Verify checks a stored game against the rules and returns one line per
// problem, empty when the game is consistent:
//
//   - the initial position parses, and moves are numbered 1..n with no gap
//     (a gap means rows were deleted);
//   - each move is made by the side to move, is legal from the stored
//     position before it, and produces the stored position after it;
//   - the result and its termination belong together, and a termination
//     decided by the position (mate, stalemate, dead material, repetition,
//     fifty moves) holds in the final position;
//   - a mated or stalemated final position has its result recorded.
//
// Draw rules that became automatic later are not required of older games:
// an unfinished game in a drawn position is not a problem.
func Verify(record *storage.GameRecord, moves []storage.MoveRecord) []string {
	var problems []string
	pos, err := chess.ParseFEN(record.InitialFEN)
	if err != nil {
		return []string{fmt.Sprintf("initial position: %v", err)}
	}
	positions := []*chess.Position{pos}
	for i, move := range moves {
		if move.MoveNumber != i+1 {
			return append(problems, fmt.Sprintf("ply %d missing (next stored ply is %d)", i+1, move.MoveNumber))
		}
		if move.PlayerColor != pos.Turn().String() {
			problems = append(problems, fmt.Sprintf("ply %d %s: stored as played by %q, but %q is to move",
				move.MoveNumber, move.MoveUCI, move.PlayerColor, pos.Turn().String()))
		}
		m, err := pos.ParseUCI(move.MoveUCI)
		if err != nil {
			problems = append(problems, fmt.Sprintf("ply %d %s: %v", move.MoveNumber, move.MoveUCI, err))
		} else if want := pos.Play(m).FEN(); !sameFEN(want, move.FENAfterMove) {
			problems = append(problems, fmt.Sprintf("ply %d %s: stored %q, rules give %q",
				move.MoveNumber, move.MoveUCI, move.FENAfterMove, want))
		}
		// Continue from the stored position so one fault is reported once.
		if pos, err = chess.ParseFEN(move.FENAfterMove); err != nil {
			return append(problems, fmt.Sprintf("ply %d stored position: %v", move.MoveNumber, err))
		}
		positions = append(positions, pos)
	}
	return append(problems, verifyOutcome(record.Result, record.Termination, positions)...)
}

func verifyOutcome(result, termination string, positions []*chess.Position) []string {
	final := positions[len(positions)-1]
	hasMoves := final.HasLegalMoves()
	mated := !hasMoves && final.InCheck()
	stalemated := !hasMoves && !final.InCheck()

	if result == "" {
		switch {
		case termination != "":
			return []string{fmt.Sprintf("termination %q without a result", termination)}
		case mated:
			return []string{"final position is checkmate but no result is stored"}
		case stalemated:
			return []string{"final position is stalemate but no result is stored"}
		}
		return nil
	}

	state, ok := stateOf(result)
	if !ok || !core.Termination(termination).ValidFor(state) {
		return []string{fmt.Sprintf("result %q cannot come from termination %q", result, termination)}
	}
	var holds bool
	switch core.Termination(termination) {
	case core.TermCheckmate:
		// The side to move is the one mated.
		holds = mated && (final.Turn() == chess.White) == (result == "black_wins")
	case core.TermStalemate:
		holds = stalemated
	case core.TermInsufficientMaterial:
		holds = final.InsufficientMaterial()
	case core.TermThreefoldRepetition:
		holds = chess.Repetitions(positions) >= 3
	case core.TermFiftyMoveRule:
		holds = final.Halfmove() >= 100
	case core.TermResignation, core.TermAgreement:
		holds = true // decided by the players, not the position
	}
	if !holds {
		return []string{fmt.Sprintf("stored %s by %s does not hold in the final position", result, termination)}
	}
	return nil
}

func stateOf(result string) (core.State, bool) {
	switch result {
	case "white_wins":
		return core.StateWhiteWins, true
	case "black_wins":
		return core.StateBlackWins, true
	case "stalemate":
		return core.StateStalemate, true
	case "draw":
		return core.StateDraw, true
	}
	return 0, false
}

func sameFEN(a, b string) bool {
	na, errA := chess.NormalizeFEN(a)
	nb, errB := chess.NormalizeFEN(b)
	return errA == nil && errB == nil && na == nb
}
