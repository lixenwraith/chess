//go:build js && wasm

package command

import "fmt"

// readImportFile has no file system in the browser; the web board imports
// a pasted PGN instead.
func readImportFile(path string) (string, error) {
	return "", fmt.Errorf("files cannot be read in the browser; paste the PGN into the web board's Import instead")
}
