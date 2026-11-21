//go:build js && wasm

package command

import (
	"context"
	"sync/atomic"
	"syscall/js"
)

// running cancels the command Ctrl-C would interrupt; nil at a prompt.
var running atomic.Pointer[context.CancelFunc]

// terminal.js calls chessInterrupt on Ctrl-C outside a read; it answers
// whether a command took the interrupt.
func init() {
	js.Global().Set("chessInterrupt", js.FuncOf(func(js.Value, []js.Value) any {
		cancel := running.Load()
		if cancel == nil {
			return false
		}
		(*cancel)()
		return true
	}))
}

func interruptContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	running.Store(&cancel)
	return ctx, func() {
		running.Store(nil)
		cancel()
	}
}
