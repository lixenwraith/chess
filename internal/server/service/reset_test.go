package service

import (
	"context"
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
	svc.mail.cooldown = 0 // set before the first request reaches the sender

	user, sessionID, err := svc.RegisterUser(t.Context(), "alice", "Alice@Example.test", "Password1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.RegisterUser(t.Context(), "bob", "", "Password1"); err != nil {
		t.Fatal(err)
	}

	// Unknown accounts and accounts without email get the same answer and no
	// mail: the sender works in order, so alice's mail is the first one.
	for _, id := range []string{"nobody", "nobody@example.test", "bob"} {
		if err := svc.RequestPasswordReset(id); err != nil {
			t.Errorf("recover %q: %v", id, err)
		}
	}

	// By email, case-insensitively; then by username, which replaces the link.
	if err := svc.RequestPasswordReset("ALICE@example.test"); err != nil {
		t.Fatal(err)
	}
	if mails := waitForMail(t, dir, 1); len(mails) != 1 {
		t.Fatalf("mail sent for unknown accounts: %d messages", len(mails))
	}
	first := resetToken(t, waitForMail(t, dir, 1)[0], "alice@example.test")
	if err := svc.RequestPasswordReset("alice"); err != nil {
		t.Fatal(err)
	}
	token := resetToken(t, waitForMail(t, dir, 2)[1], "alice@example.test")
	if token == first {
		t.Fatal("second request reused the token")
	}
	if _, _, err := svc.ResetPassword(t.Context(), first, "Password2"); !errors.Is(err, ErrResetInvalid) {
		t.Errorf("replaced token: %v", err)
	}
	for _, bad := range []string{"", "x", strings.Repeat("A", 43), token + "A"} {
		if _, _, err := svc.ResetPassword(t.Context(), bad, "Password2"); !errors.Is(err, ErrResetInvalid) {
			t.Errorf("token %q: %v", bad, err)
		}
	}

	reset, newSession, err := svc.ResetPassword(t.Context(), token, "Password2")
	if err != nil {
		t.Fatal(err)
	}
	if reset.UserID != user.UserID || reset.Username != "alice" || newSession == "" || newSession == sessionID {
		t.Fatalf("reset returned %+v, session %q", reset, newSession)
	}
	if _, _, err := svc.ResetPassword(t.Context(), token, "Password3"); !errors.Is(err, ErrResetInvalid) {
		t.Errorf("token reused: %v", err)
	}
	// The old session is gone, the new one and the new password work.
	if _, _, err := svc.ValidateToken(t.Context(), mustToken(t, svc, user.UserID, sessionID)); err == nil {
		t.Error("old session still valid")
	}
	if _, _, err := svc.ValidateToken(t.Context(), mustToken(t, svc, user.UserID, newSession)); err != nil {
		t.Errorf("new session invalid: %v", err)
	}
	if _, _, err := svc.AuthenticateUser(t.Context(), "alice", "Password1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("old password: %v", err)
	}
	if _, _, err := svc.AuthenticateUser(t.Context(), "alice", "Password2"); err != nil {
		t.Errorf("new password: %v", err)
	}

	// An expired link is refused and the cleanup removes it.
	expired, hash, err := NewResetToken()
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-time.Minute)
	if err := svc.store.CreatePasswordReset(t.Context(), storage.PasswordResetRecord{
		UserID: user.UserID, TokenHash: hash, CreatedAt: past.Add(-time.Hour), ExpiresAt: past,
	}, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.ResetPassword(t.Context(), expired, "Password4"); !errors.Is(err, ErrResetInvalid) {
		t.Errorf("expired token: %v", err)
	}
	if deleted, err := svc.store.DeleteExpiredPasswordResets(t.Context()); err != nil || deleted != 1 {
		t.Errorf("expired resets deleted = %d, %v", deleted, err)
	}
}

// TestPasswordResetRequestReturnsBeforeTheMailIsSent holds the relay: an
// answer that waited for it would take longer for an account with an address.
func TestPasswordResetRequestReturnsBeforeTheMailIsSent(t *testing.T) {
	svc := newPersistentTestService(t)
	if _, _, err := svc.RegisterUser(t.Context(), "alice", "alice@example.test", "Password1"); err != nil {
		t.Fatal(err)
	}
	sender := &heldSender{release: make(chan struct{}), sent: make(chan mail.Message, 1)}
	if err := svc.SetMailer(sender, "https://example.test/chess/web-client/"); err != nil {
		t.Fatal(err)
	}
	if err := svc.RequestPasswordReset("alice"); err != nil {
		t.Fatal(err)
	}
	close(sender.release)
	select {
	case m := <-sender.sent:
		if m.To != "alice@example.test" {
			t.Fatalf("mail to %q", m.To)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("queued reset email never sent")
	}
}

func TestPasswordResetMailsAnAccountOncePerCooldown(t *testing.T) {
	svc := newPersistentTestService(t)
	dir := filepath.Join(t.TempDir(), "mail")
	if err := svc.SetMailer(&mail.Dir{Path: dir, From: "chess@example.test"}, "https://example.test/chess/web-client/"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alice", "carol"} {
		if _, _, err := svc.RegisterUser(t.Context(), name, name+"@example.test", "Password1"); err != nil {
			t.Fatal(err)
		}
	}
	// Carol's request, queued last, shows when the sender has passed alice's
	// second one, which mails nothing and leaves the first link working.
	for _, id := range []string{"alice", "alice", "carol"} {
		if err := svc.RequestPasswordReset(id); err != nil {
			t.Fatal(err)
		}
	}
	mails := waitForMail(t, dir, 2)
	if len(mails) != 2 {
		t.Fatalf("%d messages, want one each", len(mails))
	}
	token := resetToken(t, mails[0], "alice@example.test")
	resetToken(t, mails[1], "carol@example.test")
	if _, _, err := svc.ResetPassword(t.Context(), token, "Password2"); err != nil {
		t.Fatalf("first link after a request within the cooldown: %v", err)
	}
}

// heldSender delivers each message once release is closed.
type heldSender struct {
	release chan struct{}
	sent    chan mail.Message
}

func (h *heldSender) Send(ctx context.Context, m mail.Message) error {
	select {
	case <-h.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	h.sent <- m
	return nil
}

func (h *heldSender) Describe() string { return "held" }

// waitForMail waits until dir holds at least n complete messages and returns
// them all in the order written.
func waitForMail(t *testing.T, dir string, n int) []string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		entries, _ := os.ReadDir(dir)
		var mails []string
		for _, entry := range entries {
			if data, err := os.ReadFile(filepath.Join(dir, entry.Name())); err == nil && linkPattern.Match(data) {
				mails = append(mails, string(data))
			}
		}
		if len(mails) >= n && len(mails) == len(entries) {
			return mails
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d messages in %s", len(mails), n, dir)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// resetToken returns the token of the reset link in a message to address.
func resetToken(t *testing.T, text, address string) string {
	t.Helper()
	if !strings.Contains(text, "To: "+address+"\r\n") || !strings.Contains(text, "Subject: Reset your chess password\r\n") {
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
