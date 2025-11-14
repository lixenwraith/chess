// FILE: lixenwraith/chess/cmd/chess-client-cli/exit_native.go
//go:build !js && !wasm

package main

import (
	"chess/internal/client/display"
)

func handleExit() (restart bool) {
	display.Println(display.Cyan, "Goodbye!")
	return false
}