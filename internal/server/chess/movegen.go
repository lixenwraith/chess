package chess

import (
	"errors"
	"fmt"
)

const (
	sqA1 Square = 0
	sqC1 Square = 2
	sqD1 Square = 3
	sqE1 Square = 4
	sqF1 Square = 5
	sqG1 Square = 6
	sqH1 Square = 7
	sqA8 Square = 56
	sqC8 Square = 58
	sqD8 Square = 59
	sqE8 Square = 60
	sqF8 Square = 61
	sqG8 Square = 62
	sqH8 Square = 63
)

// Move is a from-to pair plus the promotion kind, if any. Castling is the
// king's two-square move, as in UCI.
type Move struct {
	From, To  Square
	Promotion Kind
}

// UCI returns long algebraic notation, e.g. "e2e4" or "e7e8q".
func (m Move) UCI() string {
	s := m.From.String() + m.To.String()
	if m.Promotion != NoKind {
		s += string(kindLetters[m.Promotion] + 'a' - 'A')
	}
	return s
}

// Direction tables: the first four are orthogonal, the last four diagonal.
var (
	knightTargets [64][]Square
	kingTargets   [64][]Square
	rays          [64][8][]Square
	// castleMask[s] holds the rights that survive a move from or to s.
	castleMask [64]Castling
)

func init() {
	knightDeltas := [8][2]int{{1, 2}, {2, 1}, {2, -1}, {1, -2}, {-1, -2}, {-2, -1}, {-2, 1}, {-1, 2}}
	kingDeltas := [8][2]int{{1, 0}, {1, 1}, {0, 1}, {-1, 1}, {-1, 0}, {-1, -1}, {0, -1}, {1, -1}}
	dirs := [8][2]int{{0, 1}, {0, -1}, {1, 0}, {-1, 0}, {1, 1}, {1, -1}, {-1, 1}, {-1, -1}}
	for s := Square(0); s < 64; s++ {
		f, r := s.File(), s.Rank()
		for _, d := range knightDeltas {
			if onBoard(f+d[0], r+d[1]) {
				knightTargets[s] = append(knightTargets[s], MakeSquare(f+d[0], r+d[1]))
			}
		}
		for _, d := range kingDeltas {
			if onBoard(f+d[0], r+d[1]) {
				kingTargets[s] = append(kingTargets[s], MakeSquare(f+d[0], r+d[1]))
			}
		}
		for i, d := range dirs {
			for nf, nr := f+d[0], r+d[1]; onBoard(nf, nr); nf, nr = nf+d[0], nr+d[1] {
				rays[s][i] = append(rays[s][i], MakeSquare(nf, nr))
			}
		}
		castleMask[s] = WhiteKingside | WhiteQueenside | BlackKingside | BlackQueenside
	}
	castleMask[sqE1] &^= WhiteKingside | WhiteQueenside
	castleMask[sqH1] &^= WhiteKingside
	castleMask[sqA1] &^= WhiteQueenside
	castleMask[sqE8] &^= BlackKingside | BlackQueenside
	castleMask[sqH8] &^= BlackKingside
	castleMask[sqA8] &^= BlackQueenside
}

func onBoard(file, rank int) bool { return file >= 0 && file < 8 && rank >= 0 && rank < 8 }

func (p *Position) kingSquare(c Color) Square {
	king := MakePiece(c, King)
	for s := Square(0); s < 64; s++ {
		if p.board[s] == king {
			return s
		}
	}
	return NoSquare
}

// attacked reports whether side `by` attacks square s.
func (p *Position) attacked(s Square, by Color) bool {
	if s == NoSquare {
		return false
	}
	f, r := s.File(), s.Rank()
	pawn := MakePiece(by, Pawn)
	pr := r - 1 // a white pawn attacks upward, so it stands one rank below
	if by == Black {
		pr = r + 1
	}
	if pr >= 0 && pr < 8 {
		if f > 0 && p.board[MakeSquare(f-1, pr)] == pawn {
			return true
		}
		if f < 7 && p.board[MakeSquare(f+1, pr)] == pawn {
			return true
		}
	}
	knight := MakePiece(by, Knight)
	for _, t := range knightTargets[s] {
		if p.board[t] == knight {
			return true
		}
	}
	king := MakePiece(by, King)
	for _, t := range kingTargets[s] {
		if p.board[t] == king {
			return true
		}
	}
	queen := MakePiece(by, Queen)
	for i := 0; i < 8; i++ {
		slider := MakePiece(by, Rook)
		if i >= 4 {
			slider = MakePiece(by, Bishop)
		}
		for _, t := range rays[s][i] {
			if piece := p.board[t]; piece != NoPiece {
				if piece == slider || piece == queen {
					return true
				}
				break
			}
		}
	}
	return false
}

// InCheck reports whether the side to move is in check.
func (p *Position) InCheck() bool {
	return p.attacked(p.kingSquare(p.turn), p.turn.Other())
}

var promotionKinds = [4]Kind{Queen, Rook, Bishop, Knight}

// pseudoLegal appends moves that obey piece movement but may leave the
// mover's king in check.
func (p *Position) pseudoLegal(moves []Move) []Move {
	us := p.turn
	them := us.Other()
	forward, startRank, lastRank := 8, 1, 7
	if us == Black {
		forward, startRank, lastRank = -8, 6, 0
	}
	addPawn := func(from, to Square) {
		if to.Rank() == lastRank {
			for _, k := range promotionKinds {
				moves = append(moves, Move{From: from, To: to, Promotion: k})
			}
			return
		}
		moves = append(moves, Move{From: from, To: to})
	}

	for s := Square(0); s < 64; s++ {
		piece := p.board[s]
		if piece == NoPiece || piece.Color() != us {
			continue
		}
		switch piece.Kind() {
		case Pawn:
			one := s + Square(forward)
			if p.board[one] == NoPiece {
				addPawn(s, one)
				two := one + Square(forward)
				if s.Rank() == startRank && p.board[two] == NoPiece {
					moves = append(moves, Move{From: s, To: two})
				}
			}
			for _, df := range [2]int{-1, 1} {
				f := s.File() + df
				if f < 0 || f > 7 {
					continue
				}
				to := one + Square(df)
				if target := p.board[to]; target != NoPiece && target.Color() == them {
					addPawn(s, to)
				} else if to == p.ep && target == NoPiece {
					moves = append(moves, Move{From: s, To: to})
				}
			}
		case Knight:
			for _, t := range knightTargets[s] {
				if target := p.board[t]; target == NoPiece || target.Color() == them {
					moves = append(moves, Move{From: s, To: t})
				}
			}
		case King:
			for _, t := range kingTargets[s] {
				if target := p.board[t]; target == NoPiece || target.Color() == them {
					moves = append(moves, Move{From: s, To: t})
				}
			}
		default:
			first, last := 0, 8
			switch piece.Kind() {
			case Rook:
				last = 4
			case Bishop:
				first = 4
			}
			for i := first; i < last; i++ {
				for _, t := range rays[s][i] {
					target := p.board[t]
					if target == NoPiece {
						moves = append(moves, Move{From: s, To: t})
						continue
					}
					if target.Color() == them {
						moves = append(moves, Move{From: s, To: t})
					}
					break
				}
			}
		}
	}
	return p.castlingMoves(moves)
}

func (p *Position) castlingMoves(moves []Move) []Move {
	us, them := p.turn, p.turn.Other()
	kingside, queenside := WhiteKingside, WhiteQueenside
	e, f, g, d, c, b := sqE1, sqF1, sqG1, sqD1, sqC1, Square(1)
	if us == Black {
		kingside, queenside = BlackKingside, BlackQueenside
		e, f, g, d, c, b = sqE8, sqF8, sqG8, sqD8, sqC8, Square(57)
	}
	if p.castling&(kingside|queenside) == 0 || p.attacked(e, them) {
		return moves
	}
	if p.castling&kingside != 0 && p.board[f] == NoPiece && p.board[g] == NoPiece &&
		!p.attacked(f, them) && !p.attacked(g, them) {
		moves = append(moves, Move{From: e, To: g})
	}
	if p.castling&queenside != 0 && p.board[d] == NoPiece && p.board[c] == NoPiece && p.board[b] == NoPiece &&
		!p.attacked(d, them) && !p.attacked(c, them) {
		moves = append(moves, Move{From: e, To: c})
	}
	return moves
}

// LegalMoves returns every legal move in the position.
func (p *Position) LegalMoves() []Move {
	candidates := p.pseudoLegal(make([]Move, 0, 48))
	legal := candidates[:0]
	for _, m := range candidates {
		next := p.play(m)
		if !next.attacked(next.kingSquare(p.turn), p.turn.Other()) {
			legal = append(legal, m)
		}
	}
	return legal
}

// HasLegalMoves reports whether the side to move can move; it stops at the
// first legal move.
func (p *Position) HasLegalMoves() bool {
	for _, m := range p.pseudoLegal(make([]Move, 0, 48)) {
		next := p.play(m)
		if !next.attacked(next.kingSquare(p.turn), p.turn.Other()) {
			return true
		}
	}
	return false
}

// play applies a pseudo-legal move and returns the resulting position.
func (p *Position) play(m Move) Position {
	next := *p
	piece := next.board[m.From]
	captured := next.board[m.To]
	next.board[m.From] = NoPiece
	next.board[m.To] = piece

	switch piece.Kind() {
	case Pawn:
		if m.To == p.ep && captured == NoPiece && m.From.File() != m.To.File() {
			next.board[MakeSquare(m.To.File(), m.From.Rank())] = NoPiece
		}
		if m.Promotion != NoKind {
			next.board[m.To] = MakePiece(p.turn, m.Promotion)
		}
	case King:
		switch {
		case m.From == sqE1 && m.To == sqG1:
			next.board[sqH1], next.board[sqF1] = NoPiece, next.board[sqH1]
		case m.From == sqE1 && m.To == sqC1:
			next.board[sqA1], next.board[sqD1] = NoPiece, next.board[sqA1]
		case m.From == sqE8 && m.To == sqG8:
			next.board[sqH8], next.board[sqF8] = NoPiece, next.board[sqH8]
		case m.From == sqE8 && m.To == sqC8:
			next.board[sqA8], next.board[sqD8] = NoPiece, next.board[sqA8]
		}
	}

	next.castling &= castleMask[m.From] & castleMask[m.To]
	next.ep = NoSquare
	if piece.Kind() == Pawn && (m.To-m.From == 16 || m.From-m.To == 16) {
		next.ep = (m.From + m.To) / 2
	}
	if piece.Kind() == Pawn || captured != NoPiece {
		next.halfmove = 0
	} else {
		next.halfmove++
	}
	if p.turn == Black {
		next.fullmove++
	}
	next.turn = p.turn.Other()
	return next
}

// Play returns the position after a legal move. It does not re-check
// legality; use ParseUCI or ParseSAN to obtain a legal Move.
func (p *Position) Play(m Move) *Position {
	next := p.play(m)
	return &next
}

// legalEPSquare returns the en-passant target only when a capture on it is
// legal for the side to move.
func (p *Position) legalEPSquare() Square {
	if p.ep == NoSquare {
		return NoSquare
	}
	pawn := MakePiece(p.turn, Pawn)
	from := p.ep.Rank() - 1
	if p.turn == Black {
		from = p.ep.Rank() + 1
	}
	for _, df := range [2]int{-1, 1} {
		f := p.ep.File() + df
		if f < 0 || f > 7 {
			continue
		}
		s := MakeSquare(f, from)
		if p.board[s] != pawn {
			continue
		}
		next := p.play(Move{From: s, To: p.ep})
		if !next.attacked(next.kingSquare(p.turn), p.turn.Other()) {
			return p.ep
		}
	}
	return NoSquare
}

// ErrIllegalMove reports a move that is malformed or not legal here.
var ErrIllegalMove = errors.New("illegal move")

// ErrPromotionRequired reports a pawn move to the last rank without a
// promotion piece.
var ErrPromotionRequired = errors.New("promotion piece required")

// ParseUCI resolves a UCI move string against the legal moves.
func (p *Position) ParseUCI(s string) (Move, error) {
	return p.parseUCI(s, p.LegalMoves())
}

func (p *Position) parseUCI(s string, legal []Move) (Move, error) {
	if len(s) != 4 && len(s) != 5 {
		return Move{}, fmt.Errorf("%w: %q", ErrIllegalMove, s)
	}
	from, err1 := ParseSquare(s[0:2])
	to, err2 := ParseSquare(s[2:4])
	if err1 != nil || err2 != nil {
		return Move{}, fmt.Errorf("%w: %q", ErrIllegalMove, s)
	}
	m := Move{From: from, To: to}
	if len(s) == 5 {
		switch s[4] {
		case 'q', 'Q':
			m.Promotion = Queen
		case 'r', 'R':
			m.Promotion = Rook
		case 'b', 'B':
			m.Promotion = Bishop
		case 'n', 'N':
			m.Promotion = Knight
		default:
			return Move{}, fmt.Errorf("%w: %q", ErrIllegalMove, s)
		}
	}
	promotable := false
	for _, candidate := range legal {
		if candidate == m {
			return m, nil
		}
		if candidate.From == from && candidate.To == to && candidate.Promotion != NoKind {
			promotable = true
		}
	}
	if promotable && m.Promotion == NoKind {
		return Move{}, fmt.Errorf("%w: %q", ErrPromotionRequired, s)
	}
	return Move{}, fmt.Errorf("%w: %q", ErrIllegalMove, s)
}

// Perft counts leaf nodes of the legal move tree to the given depth.
func (p *Position) Perft(depth int) uint64 {
	if depth <= 0 {
		return 1
	}
	moves := p.LegalMoves()
	if depth == 1 {
		return uint64(len(moves))
	}
	var n uint64
	for _, m := range moves {
		next := p.play(m)
		n += next.Perft(depth - 1)
	}
	return n
}
