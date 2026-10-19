package display

import (
	"fmt"
	"strings"
)

// BoardFromFEN renders a FEN's piece placement in the server's ASCII board
// format (files on the first and last lines, ranks at both ends of a row,
// "." for an empty square), so RenderBoard colors it the same way.
func BoardFromFEN(fen string) (string, error) {
	fields := strings.Fields(fen)
	if len(fields) == 0 {
		return "", fmt.Errorf("empty FEN")
	}
	rows := strings.Split(fields[0], "/")
	if len(rows) != 8 {
		return "", fmt.Errorf("FEN has %d ranks, want 8", len(rows))
	}
	var b strings.Builder
	b.WriteString("  a b c d e f g h\n")
	for i, row := range rows {
		rank := byte('8' - i)
		b.WriteByte(rank)
		b.WriteByte(' ')
		files := 0
		for _, c := range row {
			switch {
			case c >= '1' && c <= '8':
				for n := 0; n < int(c-'0'); n++ {
					b.WriteString(". ")
				}
				files += int(c - '0')
			case strings.ContainsRune("pnbrqkPNBRQK", c):
				b.WriteRune(c)
				b.WriteByte(' ')
				files++
			default:
				return "", fmt.Errorf("invalid FEN character %q", c)
			}
		}
		if files != 8 {
			return "", fmt.Errorf("rank %c has %d files, want 8", rank, files)
		}
		b.WriteByte(rank)
		b.WriteByte('\n')
	}
	b.WriteString("  a b c d e f g h")
	return b.String(), nil
}
