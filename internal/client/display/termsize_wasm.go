//go:build js && wasm

package display

import "syscall/js"

// The page's terminal wrapper (terminal.js) publishes xterm's size as
// globalThis.chessTerminal.rows and .cols.
func init() {
	TermSize = func() (rows, cols int) {
		t := js.Global().Get("chessTerminal")
		if t.IsUndefined() || t.IsNull() {
			return 24, 80
		}
		rows, cols = t.Get("rows").Int(), t.Get("cols").Int()
		if rows <= 0 || cols <= 0 {
			return 24, 80
		}
		return rows, cols
	}
}
