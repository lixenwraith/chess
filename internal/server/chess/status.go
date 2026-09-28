package chess

import "strconv"

// InsufficientMaterial reports a dead position by material alone: bare
// kings, a single minor piece, or only bishops, all on squares of one color.
// Two knights are not included: mate is possible with the loser's help.
func (p *Position) InsufficientMaterial() bool {
	minors := 0
	bishopColors := [2]bool{}
	knights := 0
	for s := Square(0); s < 64; s++ {
		switch p.board[s].Kind() {
		case Pawn, Rook, Queen:
			return false
		case Knight:
			minors++
			knights++
		case Bishop:
			minors++
			bishopColors[(s.File()+s.Rank())&1] = true
		}
	}
	if minors <= 1 {
		return true
	}
	return knights == 0 && !(bishopColors[0] && bishopColors[1])
}

// Repetitions counts how often the last of positions occurs in the line,
// itself included. positions[i] is the position after i plies of one game.
// Only positions since the last capture or pawn move can repeat, and only
// every other ply has the same side to move, so at most halfmove/2 earlier
// positions are compared.
func Repetitions(positions []*Position) int {
	last := len(positions) - 1
	if last < 0 {
		return 0
	}
	key := positions[last].Key()
	count := 1
	for i := last - 2; i >= 0 && last-i <= positions[last].halfmove; i -= 2 {
		if positions[i].Key() == key {
			count++
		}
	}
	return count
}

// Line is a replayed game: the positions before and after every ply.
type Line struct {
	Start     *Position
	Moves     []Move
	SAN       []string
	positions []*Position // positions[i] is the position after i plies
}

// Replay applies UCI moves from a starting FEN. On an illegal move it
// returns the line up to that ply together with the error.
func Replay(startFEN string, uci []string) (*Line, error) {
	start, err := ParseFEN(startFEN)
	if err != nil {
		return nil, err
	}
	line := &Line{
		Start:     start,
		Moves:     make([]Move, 0, len(uci)),
		SAN:       make([]string, 0, len(uci)),
		positions: append(make([]*Position, 0, len(uci)+1), start),
	}
	pos := start
	for i, text := range uci {
		legal := pos.LegalMoves()
		m, err := pos.parseUCI(text, legal)
		if err != nil {
			return line, &PlyError{Ply: i + 1, Err: err}
		}
		line.SAN = append(line.SAN, pos.san(m, legal))
		line.Moves = append(line.Moves, m)
		pos = pos.Play(m)
		line.positions = append(line.positions, pos)
	}
	return line, nil
}

// PlyError locates a replay failure; Ply counts from 1.
type PlyError struct {
	Ply int
	Err error
}

func (e *PlyError) Error() string { return "ply " + strconv.Itoa(e.Ply) + ": " + e.Err.Error() }
func (e *PlyError) Unwrap() error { return e.Err }

// Position returns the position after ply plies (0 is the start).
func (l *Line) Position(ply int) *Position { return l.positions[ply] }

// Final returns the position after the last replayed ply.
func (l *Line) Final() *Position { return l.positions[len(l.positions)-1] }

// Repetitions counts occurrences of the position after ply, itself included.
func (l *Line) Repetitions(ply int) int { return Repetitions(l.positions[:ply+1]) }
