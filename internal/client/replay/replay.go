// Package replay steps through a stored game's history in the terminal
// client. The state is separate from the live session: it holds the
// history the server returned and a ply, and renders positions from the
// stored FENs; it never computes a move.
package replay

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/lixenwraith/chess/internal/client/api"
)

// State is a stored game and the ply shown: 0 is the initial position,
// Len() the position after the last move.
type State struct {
	History *api.GameHistoryResponse
	Ply     int
}

// New starts a replay at the initial position.
func New(history *api.GameHistoryResponse) *State {
	return &State{History: history}
}

// Len is the number of stored plies.
func (s *State) Len() int { return len(s.History.Moves) }

// Goto shows the position after ply plies, clamped to the stored line, and
// returns the ply shown.
func (s *State) Goto(ply int) int {
	if ply < 0 {
		ply = 0
	}
	if ply > s.Len() {
		ply = s.Len()
	}
	s.Ply = ply
	return s.Ply
}

func (s *State) Next() int  { return s.Goto(s.Ply + 1) }
func (s *State) Prev() int  { return s.Goto(s.Ply - 1) }
func (s *State) First() int { return s.Goto(0) }
func (s *State) Last() int  { return s.Goto(s.Len()) }

// AtEnd reports whether the last stored position is shown.
func (s *State) AtEnd() bool { return s.Ply >= s.Len() }

// FEN is the stored position at the current ply.
func (s *State) FEN() string {
	if s.Ply == 0 {
		return s.History.InitialFEN
	}
	return s.History.Moves[s.Ply-1].FENAfterMove
}

// Move is the ply-th move (from 1), or nil for the initial position.
func (s *State) Move(ply int) *api.HistoryMove {
	if ply < 1 || ply > s.Len() {
		return nil
	}
	return &s.History.Moves[ply-1]
}

// startsWithBlack and firstMoveNumber come from the initial FEN, so a game
// from a custom position numbers its moves as its PGN does.
func (s *State) start() (number int, black bool) {
	parts := strings.Fields(s.History.InitialFEN)
	number = 1
	if len(parts) >= 6 {
		if n, err := strconv.Atoi(parts[5]); err == nil && n > 0 {
			number = n
		}
	}
	black = len(parts) >= 2 && parts[1] == "b"
	return number, black
}

// Label names a ply in PGN style: "2. Nf3", "2... Nf6", or "start".
func (s *State) Label(ply int) string {
	move := s.Move(ply)
	if move == nil {
		return "start"
	}
	first, black := s.start()
	offset := ply - 1
	if black {
		offset++
	}
	number := first + offset/2
	text := move.SAN
	if text == "" {
		text = move.MoveUCI
	}
	if move.PlayerColor == "b" {
		return fmt.Sprintf("%d... %s", number, text)
	}
	return fmt.Sprintf("%d. %s", number, text)
}

// Movetext lists the moves with numbers, the current ply in brackets,
// wrapped at width columns (80 when width is not positive).
func (s *State) Movetext(width int) string {
	if width <= 0 {
		width = 80
	}
	first, black := s.start()
	var tokens []string
	number := first
	for i, move := range s.History.Moves {
		text := move.SAN
		if text == "" {
			text = move.MoveUCI
		}
		if i+1 == s.Ply {
			text = "[" + text + "]"
		}
		switch {
		case i == 0 && black:
			tokens = append(tokens, fmt.Sprintf("%d...", number), text)
			number++
		case move.PlayerColor == "w":
			tokens = append(tokens, fmt.Sprintf("%d.", number), text)
		default:
			tokens = append(tokens, text)
			number++
		}
	}
	if len(tokens) == 0 {
		return "(no moves)"
	}
	var b strings.Builder
	col := 0
	for _, token := range tokens {
		if col > 0 && col+1+len(token) > width {
			b.WriteByte('\n')
			col = 0
		} else if col > 0 {
			b.WriteByte(' ')
			col++
		}
		b.WriteString(token)
		col += len(token)
	}
	return b.String()
}

// Players names the sides: the stored name, the engine and level, or
// "Anonymous".
func (s *State) Players() (white, black string) {
	return playerLabel(s.History.Players.White), playerLabel(s.History.Players.Black)
}

func playerLabel(p api.PlayerInfo) string {
	switch {
	case p.Type == 2:
		return fmt.Sprintf("Stockfish L%d", p.Level)
	case p.Name != "":
		return p.Name
	}
	return "Anonymous"
}

// Result describes the outcome: "1-0 (checkmate)" or "ongoing".
func (s *State) Result() string {
	if s.History.Result == "" {
		return "ongoing"
	}
	if s.History.Termination != "" {
		return fmt.Sprintf("%s (%s)", s.History.PGNResult, strings.ReplaceAll(s.History.Termination, "_", " "))
	}
	return s.History.PGNResult
}
