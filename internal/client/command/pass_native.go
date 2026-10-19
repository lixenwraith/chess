//go:build !js && !wasm

package command

import (
	"github.com/lixenwraith/chess/internal/client/display"

	"golang.org/x/term"
)

func readPassword(prompt string) (string, error) {
	display.Write(prompt)
	bytePassword, err := term.ReadPassword(0) // 0 is stdin
	display.Line()
	if err != nil {
		return "", err
	}
	return string(bytePassword), nil
}
