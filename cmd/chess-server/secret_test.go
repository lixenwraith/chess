package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadJWTSecret(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string, mode os.FileMode) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil { // umask-independent
			t.Fatal(err)
		}
		return path
	}
	key := strings.Repeat("k", 48)

	secret, err := loadJWTSecret(write("ok", key+"\n", 0o600))
	if err != nil || string(secret) != key {
		t.Fatalf("valid secret = %q, %v", secret, err)
	}
	for name, test := range map[string]struct {
		content string
		mode    os.FileMode
		want    string
	}{
		"group readable": {key, 0o640, "chmod 600"},
		"short":          {strings.Repeat("k", 31) + "\n", 0o600, "at least 32 bytes"},
		"oversized":      {strings.Repeat("k", maxJWTSecretLen+1), 0o600, "exceeds"},
	} {
		if _, err := loadJWTSecret(write(name, test.content, test.mode)); err == nil ||
			!strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: error = %v, want %q", name, err, test.want)
		}
	}
	if _, err := loadJWTSecret(dir); err == nil {
		t.Error("directory accepted as secret file")
	}
	if _, err := loadJWTSecret(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing file accepted")
	}
}
