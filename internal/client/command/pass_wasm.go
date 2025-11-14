// FILE: lixenwraith/chess/internal/client/command/pass_wasm.go
//go:build js && wasm

package command

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"chess/internal/client/display"
)

func readPassword(prompt string) (string, error) {
	display.Println(display.Red, "(warning: password visible in browser)")
	display.Print(display.Yellow, prompt)

	// In WASM/browser, password masking must be handled by JavaScript/xterm.js
	// This is fallback with visible input
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		return strings.TrimSpace(scanner.Text()), nil
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("no input received")
}