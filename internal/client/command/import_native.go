//go:build !js && !wasm

package command

import (
	"fmt"
	"os"
)

const maxImportFile = 64 << 10

// readImportFile reads a PGN file for import.
func readImportFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() > maxImportFile {
		return "", fmt.Errorf("%s is larger than %d bytes", path, maxImportFile)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
