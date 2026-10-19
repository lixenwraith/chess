package service

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/lixenwraith/chess/internal/server/mail"
	"github.com/lixenwraith/chess/internal/server/storage"
)

var linkPattern = regexp.MustCompile(`https://example\.test/chess/web-client/\?reset=([A-Za-z0-9_-]{43})`)

func TestPasswordResetFlow(t *testing.T) {
	svc := newPersistentTestService(t)
	if svc.MailHealth() != "disabled" {
		t.Fatal("mail reported before configuration")
	}
	if err := svc.RequestPasswordReset("alice"); !errors.Is(err, ErrMailDisabled) {
		t.Fatalf("recovery without mail: %v", err)
	}
	dir := filepath.Join(t.TempDir(), "mail")
	if err := svc.SetMailer(&mail.Dir{Path: dir, From: "chess@example.test"}, "/chess/"); err == nil {
		t.Fatal("relative public URL accepted")
	}
	if err := svc.SetMailer(&mail.Dir{Path: dir, From: "chess@example.test"}, "https://example.test/chess/web-client/"); err != nil {
		t.Fatal(err)
	}
	if svc.MailHealth() != "ok" {
		t.Fatal("mail not reported ok")
	}

	user, sessionID, err := svc.RegisterUser("alice", "Alice@Example.test", "Password1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.RegisterUser("bob", "", "Password1"); err != nil {
		t.Fatal(err)
	}

	// Unknown accounts and accounts without email get the same answer and no mail.
	for _, id := range []string{"nobody", "nobody@example.test", "bob"} {
		if err := svc.RequestPasswordReset(id); err != nil {
			t.Errorf("recover %q: %v", id, err)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("mail sent for unknown accounts: %d files", len(entries))
	}

	// By email, case-insensitively; then by username, which replaces the link.
	if err := svc.RequestPasswordReset("ALICE@example.test"); err != nil {
		t.Fatal(err)
	}
	first := lastMailToken(t, dir)
	if err := svc.RequestPasswordReset("alice"); err != nil {
		t.Fatal(err)
	}
	token := lastMailToken(t, dir)
	if token == first {
		t.Fatal("second request reused the token")
	}
	if _, _, err := svc.ResetPassword(first, "Password2"); !errors.Is(err, ErrResetInvalid) {
		t.Errorf("replaced token: %v", err)
	}
	for _, bad := range []string{"", "x", strings.Repeat("A", 43), token + "A"} {
		if _, _, err := svc.ResetPassword(bad, "Password2"); !errors.Is(err, ErrResetInvalid) {
			t.Errorf("token %q: %v", bad, err)
		}
	}

	reset, newSession, err := svc.ResetPassword(token, "Password2")
	if err != nil {
		t.Fatal(err)
	}
	if reset.UserID != user.UserID || reset.Username != "alice" || newSession == "" || newSession == sessionID {
		t.Fatalf("reset returned %+v, session %q", reset, newSession)
	}
	if _, _, err := svc.ResetPassword(token, "Password3"); !errors.Is(err, ErrResetInvalid) {
		t.Errorf("token reused: %v", err)
	}
	// The old session is gone, the new one and the new password work.
	if _, _, err := svc.ValidateToken(mustToken(t, svc, user.UserID, sessionID)); err == nil {
		t.Error("old session still valid")
	}
	if _, _, err := svc.ValidateToken(mustToken(t, svc, user.UserID, newSession)); err != nil {
		t.Errorf("new session invalid: %v", err)
	}
	if _, _, err := svc.AuthenticateUser("alice", "Password1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("old password: %v", err)
	}
	if _, _, err := svc.AuthenticateUser("alice", "Password2"); err != nil {
		t.Errorf("new password: %v", err)
	}

	// An expired link is refused and the cleanup removes it.
	expired, hash, err := NewResetToken()
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-time.Minute)
	if err := svc.store.CreatePasswordReset(storage.PasswordResetRecord{
		UserID: user.UserID, TokenHash: hash, CreatedAt: past.Add(-time.Hour), ExpiresAt: past,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.ResetPassword(expired, "Password4"); !errors.Is(err, ErrResetInvalid) {
		t.Errorf("expired token: %v", err)
	}
	if deleted, err := svc.store.DeleteExpiredPasswordResets(); err != nil || deleted != 1 {
		t.Errorf("expired resets deleted = %d, %v", deleted, err)
	}
}

func lastMailToken(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no mail in %s: %v", dir, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, entries[len(entries)-1].Name()))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "To: alice@example.test\r\n") || !strings.Contains(text, "Subject: Reset your chess password\r\n") {
		t.Fatalf("mail = %q", text)
	}
	m := linkPattern.FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("no reset link in %q", text)
	}
	return m[1]
}

func mustToken(t *testing.T, svc *Service, userID, sessionID string) string {
	t.Helper()
	token, err := svc.GenerateUserToken(userID, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	return token
}
