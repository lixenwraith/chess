// FILE: lixenwraith/chess/internal/client/display/board.go
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
				fmt.Printf("%s%c%s", Cyan, char, Reset)
			case char >= 'A' && char <= 'Z':
				// White pieces - Blue
				fmt.Printf("%s%c%s", Blue, char, Reset)
			case char >= 'a' && char <= 'z' && !isRankLine:
				// Black pieces - Red
				fmt.Printf("%s%c%s", Red, char, Reset)
			case char == '.':
				// Empty squares
				fmt.Printf(".")
			case char >= '1' && char <= '8':
				// Rank numbers - Cyan
				fmt.Printf("%s%c%s", Cyan, char, Reset)
			case char == ' ':
				fmt.Printf(" ")
			default:
				fmt.Printf("%c", char)
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