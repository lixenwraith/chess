package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
)

const (
	minJWTSecretLen = 32
	maxJWTSecretLen = 4096
)

// loadJWTSecret reads a signing key file. The file must be a regular file
// readable only by its owner, like an SSH private key; trailing line endings
// are ignored so `openssl rand -base64 48 > file` works as-is.
func loadJWTSecret(path string) ([]byte, error) {
	return loadSecret("JWT secret", path, minJWTSecretLen)
}

// loadSecret reads the file that holds a secret of at least minimum bytes.
func loadSecret(what, path string, minimum int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s file: %w", what, err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %s file: %w", what, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s file %s is not a regular file", what, path)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf("%s file %s has mode %04o; it must not be accessible by group or others (chmod 600)",
			what, path, perm)
	}

	data, err := io.ReadAll(io.LimitReader(file, maxJWTSecretLen+1))
	if err != nil {
		return nil, fmt.Errorf("read %s file: %w", what, err)
	}
	if len(data) > maxJWTSecretLen {
		return nil, fmt.Errorf("%s file %s exceeds %d bytes", what, path, maxJWTSecretLen)
	}
	secret := bytes.TrimRight(data, "\r\n")
	if len(secret) < minimum {
		return nil, fmt.Errorf("%s file %s must contain at least %d bytes", what, path, minimum)
	}
	return secret, nil
}
