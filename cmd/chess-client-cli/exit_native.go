//go:build !js && !wasm

package main

import (
	"chess/internal/client/display"
)

func handleExit() (restart bool) {
	display.Println(display.Cyan, "Goodbye!")
	return false
}