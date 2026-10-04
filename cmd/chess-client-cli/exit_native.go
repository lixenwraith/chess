//go:build !js && !wasm

package main

import (
	"github.com/lixenwraith/chess/internal/client/display"
)

func handleExit() (restart bool) {
	display.Println(display.Cyan, "Goodbye!")
	return false
}
