package game

import (
	"fmt"
	"time"

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
	snapshots  []Snapshot
	players    map[core.Color]*core.Player
	state      core.State
	lastResult *MoveResult
	endTimeUTC *time.Time
}

// View is an immutable copy of the state needed by processors and transports.
// Service returns views instead of exposing mutable Game pointers outside its
// lock, preventing torn responses and concurrent move races.
type View struct {
	FEN           string
	InitialFEN    string
	NextTurnColor core.Color
	Moves         []string
	WhitePlayer   *core.Player
	BlackPlayer   *core.Player
	State         core.State
	LastResult    *MoveResult
	EndTimeUTC    *time.Time
}

func New(initialFEN string, whitePlayer, blackPlayer *core.Player, startingTurnColor core.Color) *Game {
	// Determine which player's turn it is initially
	var initialPlayerID string
	var initialPlayerType core.PlayerType
	if startingTurnColor == core.ColorWhite {
		initialPlayerID = whitePlayer.ID
		initialPlayerType = whitePlayer.Type
	} else {
		initialPlayerID = blackPlayer.ID
		initialPlayerType = blackPlayer.Type
	}
	whiteCopy := *whitePlayer
	blackCopy := *blackPlayer

	return &Game{
		snapshots: []Snapshot{
			{
				FEN:           initialFEN,
				PreviousMove:  "",
				NextTurnColor: startingTurnColor,
				PlayerType:    initialPlayerType,
				PlayerID:      initialPlayerID,
			},
		},
		players: map[core.Color]*core.Player{
			core.ColorWhite: &whiteCopy,
			core.ColorBlack: &blackCopy,
		},
		state: core.StateOngoing,
	}
}

func (g *Game) View() View {
	view := View{
		FEN:           g.CurrentFEN(),
		InitialFEN:    g.InitialFEN(),
		NextTurnColor: g.NextTurnColor(),
		Moves:         g.Moves(),
		State:         g.state,
	}
	if player := g.players[core.ColorWhite]; player != nil {
		copy := *player
		view.WhitePlayer = &copy
	}
	if player := g.players[core.ColorBlack]; player != nil {
		copy := *player
		view.BlackPlayer = &copy
	}
	if g.lastResult != nil {
		copy := *g.lastResult
		view.LastResult = &copy
	}
	if g.endTimeUTC != nil {
		copy := *g.endTimeUTC
		view.EndTimeUTC = &copy
	}
	return view
}

func (v View) NextPlayer() *core.Player {
	if v.NextTurnColor == core.ColorWhite {
		return v.WhitePlayer
	}
	return v.BlackPlayer
}

func (v View) Player(color core.Color) *core.Player {
	if color == core.ColorWhite {
		return v.WhitePlayer
	}
	if color == core.ColorBlack {
		return v.BlackPlayer
	}
	return nil
}

func (g *Game) SetLastResult(result *MoveResult) {
	if result == nil {
		g.lastResult = nil
		return
	}
	copy := *result
	g.lastResult = &copy
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
		PlayerType:    nextPlayer.Type,
		PlayerID:      nextPlayer.ID,
	})
}

func (g *Game) UpdatePlayers(whitePlayer, blackPlayer *core.Player) {
	whiteCopy := *whitePlayer
	blackCopy := *blackPlayer
	g.players[core.ColorWhite] = &whiteCopy
	g.players[core.ColorBlack] = &blackCopy

	// Update current snapshot's PlayerID to reflect new player
	if len(g.snapshots) > 0 {
		currentSnap := &g.snapshots[len(g.snapshots)-1]
		currentPlayer := g.players[currentSnap.NextTurnColor]
		currentSnap.PlayerID = currentPlayer.ID
		currentSnap.PlayerType = currentPlayer.Type
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
	g.endTimeUTC = nil
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

func (g *Game) SetStateAt(s core.State, at time.Time) {
	if s.IsTerminal() {
		if !g.state.IsTerminal() || g.endTimeUTC == nil {
			ended := at.UTC()
			g.endTimeUTC = &ended
		}
	} else if g.state.IsTerminal() {
		g.endTimeUTC = nil
	}
	g.state = s
}

func (g *Game) EndTimeUTC() *time.Time {
	if g.endTimeUTC == nil {
		return nil
	}
	copy := *g.endTimeUTC
	return &copy
}

func (g *Game) InitialFEN() string {
	if len(g.snapshots) > 0 {
		return g.snapshots[0].FEN
	}
	return board.StartingFEN
}

// ClaimSlot claims a player slot for a user
// Caller must hold the lock
func (g *Game) ClaimSlot(color core.Color, userID string) error {
	player := g.players[color]
	if player == nil {
		return fmt.Errorf("invalid color")
	}

	if player.Type != core.PlayerHuman {
		return fmt.Errorf("cannot claim computer slot")
	}

	if player.ClaimedBy != "" && player.ClaimedBy != userID {
		return fmt.Errorf("slot already claimed by another user")
	}

	player.ClaimedBy = userID
	return nil
}

// GetSlotOwner returns the userID that claimed the slot, empty if unclaimed
// Caller must hold the lock
func (g *Game) GetSlotOwner(color core.Color) string {
	player := g.players[color]
	if player == nil {
		return ""
	}
	return player.ClaimedBy
}

// HasComputerPlayer returns true if at least one player is computer
func (g *Game) HasComputerPlayer() bool {
	white := g.players[core.ColorWhite]
	black := g.players[core.ColorBlack]
	return (white != nil && white.Type == core.PlayerComputer) ||
		(black != nil && black.Type == core.PlayerComputer)
}
