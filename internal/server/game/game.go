package game

import (
	"fmt"
	"time"

	"github.com/lixenwraith/chess/internal/server/chess"
	"github.com/lixenwraith/chess/internal/server/core"
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
	snapshots    []Snapshot
	players      map[core.Color]*core.Player
	state        core.State
	termination  core.Termination
	lastResult   *MoveResult
	endTimeUTC   *time.Time
	lastActivity time.Time // last create, move, undo, reconfiguration, or state change
	drawOffer    core.Color
	// offerPly[c] is the ply count when color c last offered a draw, or -1.
	offerPly map[core.Color]int
	// concession is the first resignation or agreed draw; an undo keeps it.
	concession *Concession
}

// Concession is a result the players chose, and the ply it was chosen at.
type Concession struct {
	State       core.State
	Termination core.Termination
	Ply         int
}

// View is an immutable copy of the state needed by processors and transports.
// Service returns views instead of exposing mutable Game pointers outside its
// lock, preventing torn responses and concurrent move races.
type View struct {
	FEN           string
	InitialFEN    string
	NextTurnColor core.Color
	Moves         []string
	// FENs holds the position before the first move and after every move.
	FENs        []string
	WhitePlayer *core.Player
	BlackPlayer *core.Player
	State       core.State
	Termination core.Termination
	DrawOffer   core.Color // color whose offer awaits an answer; 0 when none
	OfferPly    map[core.Color]int
	Concession  *Concession
	LastResult  *MoveResult
	EndTimeUTC  *time.Time
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
		state:    core.StateOngoing,
		offerPly: map[core.Color]int{core.ColorWhite: -1, core.ColorBlack: -1},
	}
}

func (g *Game) View() View {
	view := View{
		FEN:           g.CurrentFEN(),
		InitialFEN:    g.InitialFEN(),
		NextTurnColor: g.NextTurnColor(),
		Moves:         g.Moves(),
		FENs:          make([]string, len(g.snapshots)),
		State:         g.state,
		Termination:   g.termination,
		DrawOffer:     g.drawOffer,
		OfferPly: map[core.Color]int{
			core.ColorWhite: g.offerPly[core.ColorWhite],
			core.ColorBlack: g.offerPly[core.ColorBlack],
		},
	}
	for i, snapshot := range g.snapshots {
		view.FENs[i] = snapshot.FEN
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
	if g.concession != nil {
		copy := *g.concession
		view.Concession = &copy
	}
	return view
}

// BothHuman reports whether neither side is played by the computer.
func (v View) BothHuman() bool {
	return bothHuman(v.WhitePlayer, v.BlackPlayer)
}

// BothHuman reports whether neither side is played by the computer.
func (g *Game) BothHuman() bool {
	return bothHuman(g.players[core.ColorWhite], g.players[core.ColorBlack])
}

func bothHuman(white, black *core.Player) bool {
	return white != nil && white.Type == core.PlayerHuman &&
		black != nil && black.Type == core.PlayerHuman
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

// AddSnapshot appends a move. It answers a draw offer made by the other
// side: moving instead of accepting declines it. An offer from the mover
// stands.
func (g *Game) AddSnapshot(fen string, move string, nextTurnColor core.Color) {
	if g.drawOffer == nextTurnColor {
		g.drawOffer = 0
	}
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
	g.termination = core.TermNone
	g.lastResult = nil // Clear last result
	g.endTimeUTC = nil
	g.ClearDrawOffers()
	return nil
}

// Plies returns the number of moves played.
func (g *Game) Plies() int {
	return len(g.snapshots) - 1
}

// DrawOffer returns the color whose draw offer is pending, or 0.
func (g *Game) DrawOffer() core.Color {
	return g.drawOffer
}

// OfferDraw records a pending offer from color and the ply it was made at.
func (g *Game) OfferDraw(color core.Color) {
	g.drawOffer = color
	g.offerPly[color] = g.Plies()
}

// LastOfferPly returns the ply count at color's last draw offer, or -1.
func (g *Game) LastOfferPly(color core.Color) int {
	return g.offerPly[color]
}

// RecordDeclinedOffer counts an offer that was answered at once (by a
// computer) against its maker's one-offer-per-move allowance.
func (g *Game) RecordDeclinedOffer(color core.Color) {
	g.offerPly[color] = g.Plies()
}

// DeclineDraw withdraws a pending offer.
func (g *Game) DeclineDraw() {
	g.drawOffer = 0
}

// ClearDrawOffers forgets the pending offer and the per-move allowance, as
// after an undo or a player change.
func (g *Game) ClearDrawOffers() {
	g.drawOffer = 0
	g.offerPly[core.ColorWhite] = -1
	g.offerPly[core.ColorBlack] = -1
}

// Concession returns the game's first resignation or agreed draw, or nil.
func (g *Game) Concession() *Concession {
	return g.concession
}

// Termination returns how a finished game ended.
func (g *Game) Termination() core.Termination {
	return g.termination
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

// SetOutcome sets the state and how it was reached; a non-terminal state
// clears the termination. The first concession is remembered.
func (g *Game) SetOutcome(s core.State, termination core.Termination, at time.Time) {
	g.SetStateAt(s, at)
	if s.IsTerminal() {
		g.termination = termination
		g.drawOffer = 0
		if termination.IsConcession() && g.concession == nil {
			g.concession = &Concession{State: s, Termination: termination, Ply: g.Plies()}
		}
	} else {
		g.termination = core.TermNone
	}
}

func (g *Game) SetStateAt(s core.State, at time.Time) {
	g.Touch(at)
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

// Touch records activity at the given time; earlier times are ignored.
func (g *Game) Touch(at time.Time) {
	if at.After(g.lastActivity) {
		g.lastActivity = at
	}
}

// LastActivity returns the latest time passed to Touch.
func (g *Game) LastActivity() time.Time {
	return g.lastActivity
}

// IsClaimed reports whether a registered user claimed either slot.
func (g *Game) IsClaimed() bool {
	for _, player := range g.players {
		if player != nil && player.ClaimedBy != "" {
			return true
		}
	}
	return false
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
	return chess.StartFEN
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

// ReleaseUser clears userID's claims and name snapshots, leaving those slots
// anonymous, and reports whether any slot changed. Player IDs stay: once the
// account is gone they identify nobody. Caller must hold the lock.
func (g *Game) ReleaseUser(userID string) bool {
	changed := false
	for _, player := range g.players {
		if player != nil && player.ClaimedBy == userID {
			player.ClaimedBy, player.Name = "", ""
			changed = true
		}
	}
	return changed
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
