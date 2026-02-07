package display

import (
	"fmt"
	"strings"
)

// RenderBoard renders an ASCII board with colored pieces
func RenderBoard(asciiBoard string) {
	lines := strings.Split(asciiBoard, "\n")

	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}

		isRankLine := (i == 0) || (i == 9)

		// Process each character
		for _, char := range line {
			switch {
			case char >= 'a' && char <= 'h' && isRankLine:
				// File letters - Cyan
				Print(Cyan, "%c", char)
			case char >= 'A' && char <= 'Z':
				// White pieces - Blue
				Print(Blue, "%c", char)
			case char >= 'a' && char <= 'z' && !isRankLine:
				// Black pieces - Red
				Print(Red, "%c", char)
			case char == '.':
				// Empty squares
				Print(White, ".")
			case char >= '1' && char <= '8':
				// Rank numbers - Cyan
				Print(Cyan, "%c", char)
			case char == ' ':
				Print(Reset, " ")
			default:
				Print(Reset, "%c", char)
			}
		}
		fmt.Println()
	}
}

// ColorForTurn returns colored turn indicator
func ColorForTurn(turn string) string {
	if turn == "w" {
		return Blue + "White" + Reset
	}
	return Red + "Black" + Reset
}