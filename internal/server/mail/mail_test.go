package mail

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFormatIsRFC5322WithCRLF(t *testing.T) {
	text := string(Format("Chess <chess@example.com>", Message{
		To: "alice@example.com", Subject: "Reset your\npassword", Body: "line one\nline two",
	}))
	head, body, ok := strings.Cut(text, "\r\n\r\n")
	if !ok {
		t.Fatalf("no header/body separator in %q", text)
	}
	for _, want := range []string{
		"From: Chess <chess@example.com>\r\n", "To: alice@example.com\r\n",
		"Subject: Reset your password\r\n", "Content-Type: text/plain; charset=utf-8\r\n", "@example.com>\r\n",
	} {
		if !strings.Contains(head+"\r\n", want) {
			t.Errorf("header %q missing in %q", want, head)
		}
	}
	if body != "line one\r\nline two\r\n" {
		t.Errorf("body = %q", body)
	}
	if strings.Contains(text, "\n\n") || strings.Contains(strings.ReplaceAll(text, "\r\n", ""), "\n") {
		t.Error("bare LF in message")
	}
}

func TestDirWritesOneFilePerMessage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mail")
	d := &Dir{Path: dir, From: "chess@example.com"}
	if err := d.Send(context.Background(), Message{To: "bob@example.com", Subject: "s", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Send(context.Background(), Message{To: "not an address", Subject: "s", Body: "b"}); err == nil {
		t.Error("invalid recipient accepted")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".eml") {
		t.Fatalf("entries = %v, %v", entries, err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if !strings.Contains(string(data), "To: bob@example.com\r\n") {
		t.Errorf("file = %q", data)
	}
}

func TestConfigBuild(t *testing.T) {
	if _, err := (Config{}).Build(); !errors.Is(err, ErrNoSender) {
		t.Errorf("empty config: %v", err)
	}
	for name, c := range map[string]Config{
		"both transports": {SMTPHost: "h", Dir: "d", From: "a@b.c"},
		"no from":         {SMTPHost: "h"},
		"bad from":        {SMTPHost: "h", From: "nope"},
		"password no user": {SMTPHost: "h", From: "a@b.c", SMTPPasswordFile: func() string {
			p := filepath.Join(t.TempDir(), "pw")
			os.WriteFile(p, []byte("secret\n"), 0o600)
			return p
		}()},
	} {
		if _, err := c.Build(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	pw := filepath.Join(t.TempDir(), "pw")
	os.WriteFile(pw, []byte("secret\n"), 0o644)
	if _, err := (Config{SMTPHost: "h", From: "a@b.c", SMTPUser: "u", SMTPPasswordFile: pw}).Build(); err == nil {
		t.Error("world-readable password file accepted")
	}
	os.Chmod(pw, 0o600)
	sender, err := (Config{SMTPHost: "mail.internal", From: "Chess <a@b.c>", SMTPUser: "u", SMTPPasswordFile: pw}).Build()
	if err != nil {
		t.Fatal(err)
	}
	s, ok := sender.(*SMTP)
	if !ok || s.Port != 25 || s.Password != "secret" || !strings.Contains(s.Describe(), "as u") ||
		strings.Contains(s.Describe(), "secret") {
		t.Errorf("smtp = %+v, %q", s, sender.Describe())
	}
	if d, err := (Config{Dir: "x", From: "a@b.c"}).Build(); err != nil || d.(*Dir).Path != "x" {
		t.Errorf("dir = %v, %v", d, err)
	}
}
