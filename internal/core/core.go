// FILE: internal/core/core.go
package core

type State int

const (
	StateOngoing State = iota
	StateWhiteWins
	StateBlackWins
	StateDraw
	StateStalemate
)

func (s State) String() string {
	switch s {
	case StateWhiteWins:
		return "White wins"
	case StateBlackWins:
		return "Black wins"
	case StateDraw:
		return "Draw"
	case StateStalemate:
		return "Stalemate"
	default:
		return "Ongoing"
	}
}

type PlayerType int

const (
	PlayerHuman PlayerType = iota
	PlayerComputer
)

type Player struct {
	ID   string
	Type PlayerType
}

type Color byte

const (
	ColorWhite Color = 'w'
	ColorBlack Color = 'b'
)

func OppositeColor(c Color) Color {
	if c == ColorWhite {
		return ColorBlack
	}
	return ColorWhite
}
