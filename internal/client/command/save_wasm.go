//go:build js && wasm

package command

import "fmt"

// savePGN has no file system in the browser: the PGN is printed instead,
// to be copied from the terminal.
func savePGN(path, text string) error {
	fmt.Print(text)
	return fmt.Errorf("files cannot be saved from the browser; the PGN is printed above")
}
