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
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open JWT secret file: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat JWT secret file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("JWT secret file %s is not a regular file", path)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf("JWT secret file %s has mode %04o; it must not be accessible by group or others (chmod 600)",
			path, perm)
	}

	data, err := io.ReadAll(io.LimitReader(file, maxJWTSecretLen+1))
	if err != nil {
		return nil, fmt.Errorf("read JWT secret file: %w", err)
	}
	if len(data) > maxJWTSecretLen {
		return nil, fmt.Errorf("JWT secret file %s exceeds %d bytes", path, maxJWTSecretLen)
	}
	secret := bytes.TrimRight(data, "\r\n")
	if len(secret) < minJWTSecretLen {
		return nil, fmt.Errorf("JWT secret file %s must contain at least %d bytes", path, minJWTSecretLen)
	}
	return secret, nil
}
