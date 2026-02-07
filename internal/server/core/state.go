package core

type State int

const (
	StateOngoing State = iota
	StatePending       // Computer is calculating a move
	StateStuck         // Computer is calculating a move
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