// FILE: lixenwraith/chess/internal/server/game/game.go
package game

import (
	"fmt"

	"chess/internal/server/board"
	"chess/internal/server/core"
)

type Snapshot struct {
	FEN           string          `json:"fen"`
	PreviousMove  string          `json:"previousMove"`
	NextTurnColor core.Color      `json:"nextTurnColor"`
	PlayerType    core.PlayerType `json:"playerType"`
	PlayerID      string          `json:"playerId"` // ID of the player whose turn it is
}

// MoveResult tracks the outcome of a move
type MoveResult struct {
	Move        string     `json:"move"`
	PlayerColor core.Color `json:"playerColor"`
	GameState   core.State `json:"gameState"`
	Score       int        `json:"score"`
	Depth       int        `json:"depth"`
}

type Game struct {
	snapshots  []Snapshot                  `json:"snapshots"`
	players    map[core.Color]*core.Player `json:"players"`
	state      core.State                  `json:"state"`
	lastResult *MoveResult                 `json:"lastResult,omitempty"`
}

func New(initialFEN string, whitePlayer, blackPlayer *core.Player, startingTurnColor core.Color) *Game {
	// Determine which player's turn it is initially
	var initialPlayerID string
	if startingTurnColor == core.ColorWhite {
		initialPlayerID = whitePlayer.ID
	} else {
		initialPlayerID = blackPlayer.ID
	}

	return &Game{
		snapshots: []Snapshot{
			{
				FEN:           initialFEN,
				PreviousMove:  "",
				NextTurnColor: startingTurnColor,
				PlayerID:      initialPlayerID,
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

// CurrentSnapshot returns the latest game snapshot
func (g *Game) CurrentSnapshot() Snapshot {
	return g.snapshots[len(g.snapshots)-1]
}

// CurrentFEN returns the current position in FEN notation
func (g *Game) CurrentFEN() string {
	return g.CurrentSnapshot().FEN
}

func (g *Game) NextTurnColor() core.Color {
	return g.CurrentSnapshot().NextTurnColor
}

func (g *Game) NextPlayer() *core.Player {
	return g.players[g.NextTurnColor()]
}

func (g *Game) GetPlayer(color core.Color) *core.Player {
	return g.players[color]
}

func (g *Game) AddSnapshot(fen string, move string, nextTurnColor core.Color) {
	// Get the player ID for the next turn
	nextPlayer := g.players[nextTurnColor]
	g.snapshots = append(g.snapshots, Snapshot{
		FEN:           fen,
		PreviousMove:  move,
		NextTurnColor: nextTurnColor,
		PlayerID:      nextPlayer.ID,
	})
}

func (g *Game) UpdatePlayers(whitePlayer, blackPlayer *core.Player) {
	g.players[core.ColorWhite] = whitePlayer
	g.players[core.ColorBlack] = blackPlayer

	// Update current snapshot's PlayerID to reflect new player
	if len(g.snapshots) > 0 {
		currentSnap := &g.snapshots[len(g.snapshots)-1]
		currentSnap.PlayerID = g.players[currentSnap.NextTurnColor].ID
	}
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