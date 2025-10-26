// FILE: internal/game/game.go
package game

import (
	"fmt"

	"chess/internal/board"
	"chess/internal/core"
)

type Snapshot struct {
	FEN          string     // Board state at this point
	PreviousMove string     // Move that created this position (empty for initial)
	NextTurn     core.Color // Whose turn it is at this position
}

// MoveResult tracks the outcome of a move
type MoveResult struct {
	Move      string
	Player    core.Color
	GameState core.State
	Score     int
	Depth     int
}

type Game struct {
	snapshots  []Snapshot
	players    map[core.Color]*core.Player
	state      core.State
	lastResult *MoveResult
}

func New(initialFEN string, whitePlayer, blackPlayer *core.Player, startingTurn core.Color) *Game {
	return &Game{
		snapshots: []Snapshot{
			{
				FEN:          initialFEN,
				PreviousMove: "", // No move led to initial position
				NextTurn:     startingTurn,
			},
		},
		players: map[core.Color]*core.Player{
			core.ColorWhite: whitePlayer,
			core.ColorBlack: blackPlayer,
		},
		state: core.StateOngoing,
	}
}

func (g *Game) SetLastResult(result *MoveResult) {
	g.lastResult = result
}

func (g *Game) LastResult() *MoveResult {
	return g.lastResult
}

func (g *Game) CurrentSnapshot() Snapshot {
	return g.snapshots[len(g.snapshots)-1]
}

func (g *Game) CurrentFEN() string {
	return g.CurrentSnapshot().FEN
}

func (g *Game) NextTurn() core.Color {
	return g.CurrentSnapshot().NextTurn
}

func (g *Game) NextPlayer() *core.Player {
	return g.players[g.NextTurn()]
}

func (g *Game) AddSnapshot(fen string, move string, nextTurn core.Color) {
	g.snapshots = append(g.snapshots, Snapshot{
		FEN:          fen,
		PreviousMove: move,
		NextTurn:     nextTurn,
	})
}

func (g *Game) UndoMoves(count int) error {
	if count < 1 {
		return fmt.Errorf("invalid undo count: %d", count)
	}

	availableMoves := len(g.snapshots) - 1
	if availableMoves < count {
		return fmt.Errorf("cannot undo %d moves: only %d moves available", count, availableMoves)
	}

	g.snapshots = g.snapshots[:len(g.snapshots)-count]
	g.state = core.StateOngoing // Reset game state when undoing
	g.lastResult = nil          // Clear last result
	return nil
}

func (g *Game) Moves() []string {
	moves := []string{}
	for i := 1; i < len(g.snapshots); i++ {
		if g.snapshots[i].PreviousMove != "" {
			moves = append(moves, g.snapshots[i].PreviousMove)
		}
	}
	return moves
}

func (g *Game) State() core.State {
	return g.state
}

func (g *Game) SetState(s core.State) {
	g.state = s
}

func (g *Game) InitialFEN() string {
	if len(g.snapshots) > 0 {
		return g.snapshots[0].FEN
	}
	return board.StartingFEN
}