// FILE: lixenwraith/chess/internal/client/command/pass_native.go
//go:build !js && !wasm

package command

import (
	"fmt"

	"golang.org/x/term"
)

func readPassword(prompt string) (string, error) {
	fmt.Print(prompt)
	bytePassword, err := term.ReadPassword(0) // 0 is stdin
	fmt.Println()
	if err != nil {
		return "", err
	}
	return string(bytePassword), nil
}