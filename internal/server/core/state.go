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
