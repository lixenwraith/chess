package chess

import (
	"fmt"
	"strings"
)

// maxSANLength bounds parser input; the longest SAN is "Qa1xb2+" plus
// annotation glyphs.
const maxSANLength = 16

// SAN returns Standard Algebraic Notation for a legal move, with "+" or "#".
func (p *Position) SAN(m Move) string {
	return p.san(m, p.LegalMoves())
}

func (p *Position) san(m Move, legal []Move) string {
	var b strings.Builder
	piece := p.board[m.From]
	switch {
	case piece.Kind() == King && m.To-m.From == 2:
		b.WriteString("O-O")
	case piece.Kind() == King && m.From-m.To == 2:
		b.WriteString("O-O-O")
	case piece.Kind() == Pawn:
		if m.From.File() != m.To.File() {
			b.WriteByte(byte('a' + m.From.File()))
			b.WriteByte('x')
		}
		b.WriteString(m.To.String())
		if m.Promotion != NoKind {
			b.WriteByte('=')
			b.WriteByte(kindLetters[m.Promotion])
		}
	default:
		b.WriteByte(kindLetters[piece.Kind()])
		b.WriteString(p.disambiguation(m, legal))
		if p.board[m.To] != NoPiece {
			b.WriteByte('x')
		}
		b.WriteString(m.To.String())
	}
	next := p.play(m)
	if next.InCheck() {
		if next.HasLegalMoves() {
			b.WriteByte('+')
		} else {
			b.WriteByte('#')
		}
	}
	return b.String()
}

// disambiguation returns the file, rank, or square needed to tell m apart
// from other legal moves of the same piece kind to the same square.
func (p *Position) disambiguation(m Move, legal []Move) string {
	kind := p.board[m.From].Kind()
	ambiguous, sameFile, sameRank := false, false, false
	for _, other := range legal {
		if other.To != m.To || other.From == m.From || p.board[other.From].Kind() != kind {
			continue
		}
		ambiguous = true
		if other.From.File() == m.From.File() {
			sameFile = true
		}
		if other.From.Rank() == m.From.Rank() {
			sameRank = true
		}
	}
	switch {
	case !ambiguous:
		return ""
	case !sameFile:
		return string(byte('a' + m.From.File()))
	case !sameRank:
		return string(byte('1' + m.From.Rank()))
	default:
		return m.From.String()
	}
}

// ParseSAN resolves a SAN move against the legal moves. It accepts check and
// annotation suffixes, "0-0" castling, promotion with or without "=", and
// redundant disambiguation.
func (p *Position) ParseSAN(s string) (Move, error) {
	if len(s) == 0 || len(s) > maxSANLength {
		return Move{}, fmt.Errorf("%w: %q", ErrIllegalMove, s)
	}
	text := strings.TrimRight(s, "+#!?")
	legal := p.LegalMoves()

	switch text {
	case "O-O", "0-0", "O-O-O", "0-0-0":
		kingFrom, delta := sqE1, Square(2)
		if p.turn == Black {
			kingFrom = sqE8
		}
		if len(text) == 5 {
			delta = -2
		}
		for _, m := range legal {
			if m.From == kingFrom && m.To == kingFrom+delta && p.board[m.From].Kind() == King {
				return m, nil
			}
		}
		return Move{}, fmt.Errorf("%w: %q", ErrIllegalMove, s)
	}

	kind := Pawn
	if len(text) > 0 && strings.IndexByte("NBRQK", text[0]) >= 0 {
		kind = kindOfLetter(text[0])
		text = text[1:]
	}

	promotion := NoKind
	if n := len(text); n >= 2 && strings.IndexByte("NBRQ", text[n-1]) >= 0 {
		promotion = kindOfLetter(text[n-1])
		text = strings.TrimSuffix(text[:n-1], "=")
	}

	if len(text) < 2 {
		return Move{}, fmt.Errorf("%w: %q", ErrIllegalMove, s)
	}
	to, err := ParseSquare(text[len(text)-2:])
	if err != nil {
		return Move{}, fmt.Errorf("%w: %q", ErrIllegalMove, s)
	}
	text = strings.TrimSuffix(text[:len(text)-2], "x")

	fromFile, fromRank := -1, -1
	for i := 0; i < len(text); i++ {
		switch c := text[i]; {
		case c >= 'a' && c <= 'h' && fromFile < 0 && fromRank < 0:
			fromFile = int(c - 'a')
		case c >= '1' && c <= '8' && fromRank < 0:
			fromRank = int(c - '1')
		default:
			return Move{}, fmt.Errorf("%w: %q", ErrIllegalMove, s)
		}
	}

	var found Move
	matches := 0
	for _, m := range legal {
		if m.To != to || m.Promotion != promotion || p.board[m.From].Kind() != kind {
			continue
		}
		if fromFile >= 0 && m.From.File() != fromFile || fromRank >= 0 && m.From.Rank() != fromRank {
			continue
		}
		found = m
		matches++
	}
	switch matches {
	case 1:
		return found, nil
	case 0:
		return Move{}, fmt.Errorf("%w: %q", ErrIllegalMove, s)
	default:
		return Move{}, fmt.Errorf("%w: ambiguous %q", ErrIllegalMove, s)
	}
}

func kindOfLetter(c byte) Kind {
	switch c {
	case 'N':
		return Knight
	case 'B':
		return Bishop
	case 'R':
		return Rook
	case 'Q':
		return Queen
	case 'K':
		return King
	}
	return NoKind
}
