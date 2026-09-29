// Package chess is a dependency-free rules core for replay and notation: FEN,
// legal move generation, SAN, PGN, and draw-rule detection. Stockfish remains
// the move validator for live play; this package derives notation from stored
// games and cross-checks the engine.
package chess

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// StartFEN is the standard initial position.
const StartFEN = "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1"

// maxFENLength bounds parser input; a legal FEN is far shorter.
const maxFENLength = 128

// Color is the side to move or a piece's owner.
type Color uint8

const (
	White Color = iota
	Black
)

// Other returns the opposing color.
func (c Color) Other() Color { return c ^ 1 }

func (c Color) String() string {
	if c == White {
		return "w"
	}
	return "b"
}

// Kind is a piece type without color.
type Kind uint8

const (
	NoKind Kind = iota
	Pawn
	Knight
	Bishop
	Rook
	Queen
	King
)

// Piece is a Kind with a Color; the zero value is an empty square.
type Piece uint8

const NoPiece Piece = 0

// MakePiece combines a color and kind.
func MakePiece(c Color, k Kind) Piece { return Piece(k) | Piece(c)<<3 }

func (p Piece) Kind() Kind   { return Kind(p & 7) }
func (p Piece) Color() Color { return Color(p >> 3) }

// Square indexes the board from a1 = 0 to h8 = 63.
type Square int8

const NoSquare Square = -1

// MakeSquare returns the square at file 0-7 (a-h) and rank 0-7 (1-8).
func MakeSquare(file, rank int) Square { return Square(rank*8 + file) }

func (s Square) File() int { return int(s) & 7 }
func (s Square) Rank() int { return int(s) >> 3 }

func (s Square) String() string {
	if s < 0 || s > 63 {
		return "-"
	}
	return string([]byte{byte('a' + s.File()), byte('1' + s.Rank())})
}

// ParseSquare parses algebraic coordinates such as "e4".
func ParseSquare(s string) (Square, error) {
	if len(s) != 2 || s[0] < 'a' || s[0] > 'h' || s[1] < '1' || s[1] > '8' {
		return NoSquare, fmt.Errorf("invalid square %q", s)
	}
	return MakeSquare(int(s[0]-'a'), int(s[1]-'1')), nil
}

// Castling is a set of castling rights.
type Castling uint8

const (
	WhiteKingside Castling = 1 << iota
	WhiteQueenside
	BlackKingside
	BlackQueenside
)

// Position is an immutable-by-convention board state; Play returns a copy.
type Position struct {
	board    [64]Piece
	turn     Color
	castling Castling
	// ep is the en-passant target after any double pawn step, whether or not
	// a capture is possible; FEN output prints it only when one is legal.
	ep       Square
	halfmove int
	fullmove int
}

var (
	pieceLetters = map[byte]Piece{
		'P': MakePiece(White, Pawn), 'N': MakePiece(White, Knight), 'B': MakePiece(White, Bishop),
		'R': MakePiece(White, Rook), 'Q': MakePiece(White, Queen), 'K': MakePiece(White, King),
		'p': MakePiece(Black, Pawn), 'n': MakePiece(Black, Knight), 'b': MakePiece(Black, Bishop),
		'r': MakePiece(Black, Rook), 'q': MakePiece(Black, Queen), 'k': MakePiece(Black, King),
	}
	kindLetters = [...]byte{NoKind: ' ', Pawn: 'P', Knight: 'N', Bishop: 'B', Rook: 'R', Queen: 'Q', King: 'K'}
)

// Letter returns the FEN letter of a piece (uppercase for White).
func (p Piece) Letter() byte {
	if p == NoPiece {
		return ' '
	}
	l := kindLetters[p.Kind()]
	if p.Color() == Black {
		l += 'a' - 'A'
	}
	return l
}

// ErrInvalidFEN wraps every FEN parse failure.
var ErrInvalidFEN = errors.New("invalid FEN")

func fenError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidFEN, fmt.Sprintf(format, args...))
}

// ParseFEN parses and validates a six-field FEN. It rejects positions the
// rules cannot arise from in ways that matter to move generation: a king
// count other than one per side, pawns on the first or last rank, or the side
// not to move being in check. Castling rights whose king or rook is not on
// its original square are dropped, and an en-passant square is kept only
// when a double step could have produced it.
func ParseFEN(fen string) (*Position, error) {
	if len(fen) > maxFENLength {
		return nil, fenError("longer than %d bytes", maxFENLength)
	}
	fields := strings.Fields(fen)
	if len(fields) != 6 {
		return nil, fenError("expected 6 fields, got %d", len(fields))
	}
	p := &Position{ep: NoSquare}

	ranks := strings.Split(fields[0], "/")
	if len(ranks) != 8 {
		return nil, fenError("expected 8 ranks")
	}
	kings := [2]int{}
	for i, row := range ranks {
		rank := 7 - i
		file := 0
		for j := 0; j < len(row); j++ {
			ch := row[j]
			if ch >= '1' && ch <= '8' {
				file += int(ch - '0')
				if file > 8 {
					return nil, fenError("rank %d overflows", rank+1)
				}
				continue
			}
			piece, ok := pieceLetters[ch]
			if !ok {
				return nil, fenError("unknown piece %q", ch)
			}
			if file >= 8 {
				return nil, fenError("rank %d overflows", rank+1)
			}
			if piece.Kind() == Pawn && (rank == 0 || rank == 7) {
				return nil, fenError("pawn on rank %d", rank+1)
			}
			if piece.Kind() == King {
				kings[piece.Color()]++
			}
			p.board[MakeSquare(file, rank)] = piece
			file++
		}
		if file != 8 {
			return nil, fenError("rank %d has %d files", rank+1, file)
		}
	}
	if kings[White] != 1 || kings[Black] != 1 {
		return nil, fenError("each side needs exactly one king")
	}

	switch fields[1] {
	case "w":
		p.turn = White
	case "b":
		p.turn = Black
	default:
		return nil, fenError("side to move must be w or b")
	}

	if fields[2] != "-" {
		if len(fields[2]) > 4 {
			return nil, fenError("castling field too long")
		}
		for i := 0; i < len(fields[2]); i++ {
			var right Castling
			switch fields[2][i] {
			case 'K':
				right = WhiteKingside
			case 'Q':
				right = WhiteQueenside
			case 'k':
				right = BlackKingside
			case 'q':
				right = BlackQueenside
			default:
				return nil, fenError("invalid castling right %q", fields[2][i])
			}
			if p.castling&right != 0 {
				return nil, fenError("duplicate castling right %q", fields[2][i])
			}
			p.castling |= right
		}
		p.castling &= p.castlingSupported()
	}

	if fields[3] != "-" {
		sq, err := ParseSquare(fields[3])
		if err != nil {
			return nil, fenError("en passant: %v", err)
		}
		if p.epPlausible(sq) {
			p.ep = sq
		}
	}

	var err error
	if p.halfmove, err = parseCounter(fields[4], 0); err != nil {
		return nil, fenError("halfmove clock: %v", err)
	}
	if p.fullmove, err = parseCounter(fields[5], 1); err != nil {
		return nil, fenError("fullmove number: %v", err)
	}

	if p.attacked(p.kingSquare(p.turn.Other()), p.turn) {
		return nil, fenError("side not to move is in check")
	}
	return p, nil
}

func parseCounter(s string, minimum int) (int, error) {
	if len(s) > 5 {
		return 0, errors.New("out of range")
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < minimum {
		return 0, errors.New("out of range")
	}
	return n, nil
}

// castlingSupported returns the rights whose king and rook stand on their
// original squares.
func (p *Position) castlingSupported() Castling {
	var c Castling
	wk, bk := MakePiece(White, King), MakePiece(Black, King)
	wr, br := MakePiece(White, Rook), MakePiece(Black, Rook)
	if p.board[sqE1] == wk {
		if p.board[sqH1] == wr {
			c |= WhiteKingside
		}
		if p.board[sqA1] == wr {
			c |= WhiteQueenside
		}
	}
	if p.board[sqE8] == bk {
		if p.board[sqH8] == br {
			c |= BlackKingside
		}
		if p.board[sqA8] == br {
			c |= BlackQueenside
		}
	}
	return c
}

// epPlausible reports whether the opponent's last move could have been a
// double pawn step past sq.
func (p *Position) epPlausible(sq Square) bool {
	them := p.turn.Other()
	var pawnRank, targetRank, originRank int
	if them == White {
		targetRank, pawnRank, originRank = 2, 3, 1
	} else {
		targetRank, pawnRank, originRank = 5, 4, 6
	}
	if sq.Rank() != targetRank {
		return false
	}
	pawnSq := MakeSquare(sq.File(), pawnRank)
	origin := MakeSquare(sq.File(), originRank)
	return p.board[pawnSq] == MakePiece(them, Pawn) && p.board[sq] == NoPiece && p.board[origin] == NoPiece
}

// FEN serializes the position. The en-passant square is printed only when
// an en-passant capture is legal, so equal positions have equal FENs
// regardless of which convention produced the input.
func (p *Position) FEN() string {
	var b strings.Builder
	b.Grow(90)
	for rank := 7; rank >= 0; rank-- {
		empty := 0
		for file := 0; file < 8; file++ {
			piece := p.board[MakeSquare(file, rank)]
			if piece == NoPiece {
				empty++
				continue
			}
			if empty > 0 {
				b.WriteByte(byte('0' + empty))
				empty = 0
			}
			b.WriteByte(piece.Letter())
		}
		if empty > 0 {
			b.WriteByte(byte('0' + empty))
		}
		if rank > 0 {
			b.WriteByte('/')
		}
	}
	b.WriteByte(' ')
	b.WriteString(p.turn.String())
	b.WriteByte(' ')
	b.WriteString(p.castlingString())
	b.WriteByte(' ')
	b.WriteString(p.legalEPSquare().String())
	b.WriteByte(' ')
	b.WriteString(strconv.Itoa(p.halfmove))
	b.WriteByte(' ')
	b.WriteString(strconv.Itoa(p.fullmove))
	return b.String()
}

// Key identifies a position for repetition: placement, side to move,
// castling rights, and a legal en-passant square.
func (p *Position) Key() string {
	fen := p.FEN()
	// Drop the two counters.
	for i, n := len(fen)-1, 0; i >= 0; i-- {
		if fen[i] == ' ' {
			n++
			if n == 2 {
				return fen[:i]
			}
		}
	}
	return fen
}

func (p *Position) castlingString() string {
	if p.castling == 0 {
		return "-"
	}
	var b []byte
	for _, r := range []struct {
		right  Castling
		letter byte
	}{{WhiteKingside, 'K'}, {WhiteQueenside, 'Q'}, {BlackKingside, 'k'}, {BlackQueenside, 'q'}} {
		if p.castling&r.right != 0 {
			b = append(b, r.letter)
		}
	}
	return string(b)
}

// Turn returns the side to move.
func (p *Position) Turn() Color { return p.turn }

// Fullmove returns the FEN fullmove number.
func (p *Position) Fullmove() int { return p.fullmove }

// Halfmove returns the halfmove clock for the fifty-move rule.
func (p *Position) Halfmove() int { return p.halfmove }

// PieceAt returns the piece on a square.
func (p *Position) PieceAt(s Square) Piece {
	if s < 0 || s > 63 {
		return NoPiece
	}
	return p.board[s]
}

// NormalizeFEN parses and re-serializes a FEN, so FENs that differ only in
// en-passant convention or castling-right order compare equal.
func NormalizeFEN(fen string) (string, error) {
	p, err := ParseFEN(fen)
	if err != nil {
		return "", err
	}
	return p.FEN(), nil
}

// ASCII renders the board from White's side: files on the first and last
// lines, ranks at both ends of each row, "." for an empty square.
func (p *Position) ASCII() string {
	var b strings.Builder
	b.Grow(200)
	b.WriteString("  a b c d e f g h\n")
	for rank := 7; rank >= 0; rank-- {
		b.WriteByte(byte('1' + rank))
		b.WriteByte(' ')
		for file := 0; file < 8; file++ {
			if piece := p.board[MakeSquare(file, rank)]; piece != NoPiece {
				b.WriteByte(piece.Letter())
			} else {
				b.WriteByte('.')
			}
			b.WriteByte(' ')
		}
		b.WriteByte(byte('1' + rank))
		b.WriteByte('\n')
	}
	b.WriteString("  a b c d e f g h")
	return b.String()
}
