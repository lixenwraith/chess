//go:build !js && !wasm

package command

import "os"

// savePGN writes a PGN export to path.
func savePGN(path, text string) error {
	return os.WriteFile(path, []byte(text), 0o644)
}
