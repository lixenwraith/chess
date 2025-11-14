// FILE: lixenwraith/chess/cmd/chess-client-cli/exit_wasm.go
//go:build js && wasm

package main

import (
	"chess/internal/client/display"
)

func handleExit() (restart bool) {
	display.Println(display.Cyan, "Goodbye!")

	display.Println(display.Yellow, "\n━━━━━━━━━━━━━━━━━━━━━━━━")
	display.Println(display.Yellow, "Session ended.")
	display.Println(display.Yellow, "Restarting the client.")
	display.Println(display.Yellow, "━━━━━━━━━━━━━━━━━━━━━━━━\n")

	return true
}