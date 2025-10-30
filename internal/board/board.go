// FILE: internal/board/board.go
package board

import (
	"fmt"
	"strings"

	"chess/internal/core"
)

const (
	StartingFEN = "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1"
)

type Board struct {
	squares   [8][8]byte
	turn      core.Color
	castling  string
	enPassant string
	halfmove  int
	fullmove  int
}

func ParseFEN(fen string) (*Board, error) {
	parts := strings.Fields(fen)
	if len(parts) != 6 {
		return nil, fmt.Errorf("invalid FEN: expected 6 parts, got %d", len(parts))
	}

	b := &Board{}

	// Parse board
	ranks := strings.Split(parts[0], "/")
	if len(ranks) != 8 {
		return nil, fmt.Errorf("invalid FEN: expected 8 ranks")
	}

	for r := 0; r < 8; r++ {
		file := 0
		for _, ch := range ranks[r] {
			if ch >= '1' && ch <= '8' {
				file += int(ch - '0')
			} else {
				if file >= 8 {
					return nil, fmt.Errorf("invalid FEN: too many pieces in rank %d", r+1)
				}
				b.squares[r][file] = byte(ch)
				file++
			}
		}
		if file != 8 {
			return nil, fmt.Errorf("invalid FEN: rank %d has %d files", r+1, file)
		}
	}

	// Parse game state with validation
	if len(parts[1]) != 1 {
		return nil, fmt.Errorf("invalid FEN: turn must be 'w' or 'b'")
	}
	switch parts[1] {
	case "w":
		b.turn = core.ColorWhite
	case "b":
		b.turn = core.ColorBlack
	default:
		return nil, fmt.Errorf("invalid FEN: turn must be 'w' or 'b'")
	}
	b.castling = parts[2]
	b.enPassant = parts[3]

	if _, err := fmt.Sscanf(parts[4], "%d", &b.halfmove); err != nil {
		return nil, fmt.Errorf("invalid FEN: halfmove counter")
	}
	if _, err := fmt.Sscanf(parts[5], "%d", &b.fullmove); err != nil {
		return nil, fmt.Errorf("invalid FEN: fullmove counter")
	}

	return b, nil
}

// ToASCII creates an ASCII representation of the board
func (b *Board) ToASCII() string {
	var sb strings.Builder
	sb.WriteString("  a b c d e f g h\n")

	for r := 0; r < 8; r++ {
		sb.WriteString(fmt.Sprintf("%d ", 8-r))
		for f := 0; f < 8; f++ {
			square := fmt.Sprintf("%c%c", 'a'+f, '8'-r)
			piece := b.GetPieceAt(square)

			if piece == 0 {
				sb.WriteString(". ")
			} else {
				sb.WriteString(fmt.Sprintf("%c ", piece))
			}
		}
		sb.WriteString(fmt.Sprintf(" %d\n", 8-r))
	}
	sb.WriteString("  a b c d e f g h")

	return sb.String()
}

func (b *Board) Turn() core.Color {
	return b.turn
}

func (b *Board) GetPieceAt(square string) byte {
	if len(square) != 2 {
		return 0
	}
	if square[0] < 'a' || square[0] > 'h' || square[1] < '1' || square[1] > '8' {
		return 0
	}
	file := square[0] - 'a'
	rank := '8' - square[1]
	return b.squares[rank][file]
}