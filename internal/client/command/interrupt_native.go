//go:build !js && !wasm

package command

import (
	"context"
	"os"
	"os/signal"
)

// interruptContext is cancelled by Ctrl-C until stop, which gives the signal
// its default back: at the prompt it ends the client.
func interruptContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}
