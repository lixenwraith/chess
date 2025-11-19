// FILE: lixenwraith/chess/cmd/chess-client-cli/exit_wasm.go
//go:build js && wasm

package main

import (
	"chess/internal/client/display"
)

func handleExit() (restart bool) {
	display.Println(display.Cyan, "Goodbye!")

	display.Println(display.Yellow, "\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	display.Println(display.Yellow, "Session ended.")
	display.Println(display.Yellow, "Restarting the client.\n")
	display.Println(display.Yellow, "For a complete restart:")
	display.Println(display.White, "• Refresh the page (F5 or Ctrl+R)")
	display.Println(display.White, "• Or close and reopen this tab")
	display.Println(display.Yellow, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

	return true
}