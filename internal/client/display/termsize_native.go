//go:build !js && !wasm

package display

import (
	"os"

	"golang.org/x/term"
)

func init() {
	TermSize = func() (rows, cols int) {
		fd := int(os.Stdout.Fd())
		if !term.IsTerminal(fd) {
			return 0, 0
		}
		cols, rows, err := term.GetSize(fd)
		if err != nil {
			return 0, 0
		}
		return rows, cols
	}
}
