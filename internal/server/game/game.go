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
	At            time.Time       `json:"at"`       // when PreviousMove was made; the start, for the first
}

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
	startTime    time.Time
	endTimeUTC   *time.Time
	lastActivity time.Time // last create, move, undo, reconfiguration, or state change
	drawOffer    core.Color
	// offerPly[c] is the ply count when color c last offered a draw, or -1.
	offerPly map[core.Color]int
	// concession is the first resignation or agreed draw; an undo keeps it.
	concession *Concession
}

// Concession is a result the players chose, the ply it was chosen at, and when.
type Concession struct {
	State       core.State
	Termination core.Termination
	Ply         int
	At          time.Time
}

// View is an immutable copy of the state needed by processors and transports.
// Service returns views instead of exposing mutable Game pointers outside its
// lock, preventing torn responses and concurrent move races.
type View struct {
	FEN           string
	InitialFEN    string
	NextTurnColor core.Color
	Moves         []string
	// FENs holds the position before the first move and after every move;
	// MoveTimes runs parallel to Moves.
	FENs         []string
	MoveTimes    []time.Time
	WhitePlayer  *core.Player
	BlackPlayer  *core.Player
	State        core.State
	Termination  core.Termination
	DrawOffer    core.Color // color whose offer awaits an answer; 0 when none
	OfferPly     map[core.Color]int
	Concession   *Concession
	LastResult   *MoveResult
	StartTimeUTC time.Time
	EndTimeUTC   *time.Time
}

func New(initialFEN string, whitePlayer, blackPlayer *core.Player, startingTurnColor core.Color, started time.Time) *Game {
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
				At:            started,
			},
		},
		players: map[core.Color]*core.Player{
			core.ColorWhite: &whiteCopy,
			core.ColorBlack: &blackCopy,
		},
		state:        core.StateOngoing,
		startTime:    started,
		lastActivity: started,
		offerPly:     map[core.Color]int{core.ColorWhite: -1, core.ColorBlack: -1},
	}
}

// Restore rebuilds a game from a view of it, as its stored rows give one. A
// draw offer, the offer allowance and the last engine result are not kept:
// they start fresh. Last activity is the latest of start, last move and end.
func Restore(v View) *Game {
	turn := v.NextTurnColor
	if len(v.Moves)%2 == 1 {
		turn = core.OppositeColor(turn)
	}
	g := New(v.InitialFEN, v.WhitePlayer, v.BlackPlayer, turn, v.StartTimeUTC)
	for i, move := range v.Moves {
		turn = core.OppositeColor(turn)
		g.AddSnapshot(v.FENs[i+1], move, turn, v.MoveTimes[i])
		g.Touch(v.MoveTimes[i])
	}
	g.state, g.termination = v.State, v.Termination
	if v.EndTimeUTC != nil {
		ended := *v.EndTimeUTC
		g.endTimeUTC = &ended
		g.Touch(ended)
	}
	if v.Concession != nil {
		concession := *v.Concession
		g.concession = &concession
	}
	return g
}

func (g *Game) View() View {
	view := View{
		FEN:           g.CurrentFEN(),
		InitialFEN:    g.InitialFEN(),
		NextTurnColor: g.NextTurnColor(),
		Moves:         g.Moves(),
		FENs:          make([]string, len(g.snapshots)),
		MoveTimes:     make([]time.Time, 0, len(g.snapshots)-1),
		State:         g.state,
		Termination:   g.termination,
		StartTimeUTC:  g.startTime,
		DrawOffer:     g.drawOffer,
		OfferPly: map[core.Color]int{
			core.ColorWhite: g.offerPly[core.ColorWhite],
			core.ColorBlack: g.offerPly[core.ColorBlack],
		},
	}
	for i, snapshot := range g.snapshots {
		view.FENs[i] = snapshot.FEN
		if i > 0 {
			view.MoveTimes = append(view.MoveTimes, snapshot.At)
		}
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

func (v View) BothHuman() bool {
	return bothHuman(v.WhitePlayer, v.BlackPlayer)
}

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

func (g *Game) CurrentSnapshot() Snapshot {
	return g.snapshots[len(g.snapshots)-1]
}

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

// AddSnapshot appends a move made at the given time. It answers a draw offer
// made by the other side: moving instead of accepting declines it. An offer
// from the mover stands.
func (g *Game) AddSnapshot(fen string, move string, nextTurnColor core.Color, at time.Time) {
	if g.drawOffer == nextTurnColor {
		g.drawOffer = 0
	}
	nextPlayer := g.players[nextTurnColor]
	g.snapshots = append(g.snapshots, Snapshot{
		FEN:           fen,
		PreviousMove:  move,
		NextTurnColor: nextTurnColor,
		PlayerType:    nextPlayer.Type,
		PlayerID:      nextPlayer.ID,
		At:            at,
	})
}

func (g *Game) UpdatePlayers(whitePlayer, blackPlayer *core.Player) {
	whiteCopy := *whitePlayer
	blackCopy := *blackPlayer
	g.players[core.ColorWhite] = &whiteCopy
	g.players[core.ColorBlack] = &blackCopy

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
	g.state = core.StateOngoing
	g.termination = core.TermNone
	g.lastResult = nil
	g.endTimeUTC = nil
	g.ClearDrawOffers()
	return nil
}

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
			g.concession = &Concession{State: s, Termination: termination, Ply: g.Plies(), At: at.UTC()}
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

// ClaimSlot gives an open human side to a signed-in user, with the name the
// user has now; repeating one's own claim changes nothing. The caller holds
// the service lock.
func (g *Game) ClaimSlot(color core.Color, userID, name string) error {
	player, err := g.openSide(color, core.Actor{UserID: userID})
	if err != nil || player.ClaimedBy == userID {
		return err
	}
	player.ClaimedBy, player.Name = userID, name
	return nil
}

// SeatGuest gives an open human side to the guest whose secret hashes to
// seat. The caller holds the service lock.
func (g *Game) SeatGuest(color core.Color, seat []byte) error {
	player, err := g.openSide(color, core.Actor{Seat: seat})
	if err != nil || player.Seated {
		return err
	}
	player.Seat, player.Seated = seat, true
	return nil
}

// openSide returns color's human side when a may take it or holds it already.
func (g *Game) openSide(color core.Color, a core.Actor) (*core.Player, error) {
	player := g.players[color]
	switch {
	case player == nil:
		return nil, fmt.Errorf("invalid color")
	case player.Type != core.PlayerHuman:
		return nil, fmt.Errorf("cannot take a computer's side")
	case !player.Admits(a):
		return nil, fmt.Errorf("side already held by another player")
	}
	return player, nil
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
