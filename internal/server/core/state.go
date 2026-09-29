package core

type State int

const (
	StateOngoing State = iota
	StatePending       // Computer is calculating a move
	StateStuck         // Engine work failed and requires recovery or undo
	StateWhiteWins
	StateBlackWins
	StateDraw
	StateStalemate
)

func (s State) String() string {
	switch s {
	case StatePending:
		return "pending"
	case StateStuck:
		return "stuck"
	case StateWhiteWins:
		return "white wins"
	case StateBlackWins:
		return "black wins"
	case StateDraw:
		return "draw"
	case StateStalemate:
		return "stalemate"
	case StateOngoing:
		return "ongoing"
	default:
		return "unknown"
	}
}

// IsTerminal reports whether the game has a durable result. Pending and stuck
// are operational states and must not be archived as completed games.
func (s State) IsTerminal() bool {
	switch s {
	case StateWhiteWins, StateBlackWins, StateDraw, StateStalemate:
		return true
	default:
		return false
	}
}

// Result returns the stable persistence/API value for a terminal state.
func (s State) Result() (string, bool) {
	switch s {
	case StateWhiteWins:
		return "white_wins", true
	case StateBlackWins:
		return "black_wins", true
	case StateDraw:
		return "draw", true
	case StateStalemate:
		return "stalemate", true
	default:
		return "", false
	}
}

// Termination names how a game reached its result.
type Termination string

const (
	TermNone                 Termination = ""
	TermCheckmate            Termination = "checkmate"
	TermStalemate            Termination = "stalemate"
	TermInsufficientMaterial Termination = "insufficient_material"
	TermThreefoldRepetition  Termination = "threefold_repetition"
	TermFiftyMoveRule        Termination = "fifty_move_rule"
	TermAgreement            Termination = "agreement"
	TermResignation          Termination = "resignation"
)

// ValidFor reports whether a termination can produce the terminal state:
// wins come from checkmate or resignation, stalemate from stalemate, and
// draws from the draw rules or agreement.
func (t Termination) ValidFor(s State) bool {
	switch s {
	case StateWhiteWins, StateBlackWins:
		return t == TermCheckmate || t == TermResignation
	case StateStalemate:
		return t == TermStalemate
	case StateDraw:
		return t == TermInsufficientMaterial || t == TermThreefoldRepetition ||
			t == TermFiftyMoveRule || t == TermAgreement
	default:
		return t == TermNone
	}
}

// IsConcession reports whether the players chose the result: a resignation or
// a draw by agreement. A concession is final when both sides are human; against
// the computer play may continue, and the first concession stays on record.
func (t Termination) IsConcession() bool {
	return t == TermResignation || t == TermAgreement
}

// ParseColor accepts "w", "b", "white", or "black"; anything else is zero.
func ParseColor(s string) Color {
	switch s {
	case "w", "white":
		return ColorWhite
	case "b", "black":
		return ColorBlack
	}
	return 0
}
