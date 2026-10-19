package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/lixenwraith/chess/internal/server/mail"
	"github.com/lixenwraith/chess/internal/server/storage"

	"github.com/google/uuid"
)

const (
	// PasswordResetTTL is how long a reset link works.
	PasswordResetTTL = time.Hour
	resetTokenBytes  = 32
	mailSendTimeout  = 20 * time.Second
)

var (
	// ErrMailDisabled reports that no mail transport is configured, so
	// password recovery is off.
	ErrMailDisabled = errors.New("password recovery is not configured")
	// ErrMailFailed reports a delivery failure to the relay.
	ErrMailFailed = errors.New("could not send the email")
	// ErrResetInvalid reports an unknown, used, or expired reset token.
	ErrResetInvalid = errors.New("invalid or expired reset link")
)

// mailer is the configured transport and the client URL reset links open.
type mailer struct {
	sender    mail.Sender
	publicURL *url.URL
}

// SetMailer configures password recovery: sender delivers the email and
// publicURL is the web client's address, which the link opens with
// ?reset=<token>. A nil sender turns recovery off.
func (s *Service) SetMailer(sender mail.Sender, publicURL string) error {
	if sender == nil {
		s.mail = nil
		return nil
	}
	u, err := url.Parse(publicURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("public URL %q must be absolute (https://host/chess/web-client/)", publicURL)
	}
	s.mail = &mailer{sender: sender, publicURL: u}
	return nil
}

// MailHealth reports "ok" when password recovery can send mail, otherwise
// "disabled".
func (s *Service) MailHealth() string {
	if s.mail == nil || s.store == nil {
		return "disabled"
	}
	return "ok"
}

// NewResetToken returns a fresh reset token and the hash stored for it.
func NewResetToken() (token string, hash []byte, err error) {
	raw := make([]byte, resetTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, hashResetToken(token), nil
}

func hashResetToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// ResetLink is the web client URL that opens the password form for token.
func ResetLink(publicURL *url.URL, token string) string {
	u := *publicURL
	q := u.Query()
	q.Set("reset", token)
	u.RawQuery = q.Encode()
	u.Fragment = ""
	return u.String()
}

// RequestPasswordReset emails a reset link to the account named by
// identifier (username or email) when it has an email address. It reports
// success for unknown accounts and accounts without an email, so the request
// reveals nothing about who is registered; the caller sees the same answer.
func (s *Service) RequestPasswordReset(identifier string) error {
	if s.store == nil {
		return ErrStorageDisabled
	}
	if s.mail == nil {
		return ErrMailDisabled
	}
	identifier = strings.ToLower(strings.TrimSpace(identifier))
	var record *storage.UserRecord
	var err error
	if strings.Contains(identifier, "@") {
		record, err = s.store.GetUserByEmail(identifier)
	} else {
		record, err = s.store.GetUserByUsername(identifier)
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			slog.Info("password reset requested for an unknown account")
			return nil
		}
		return fmt.Errorf("%w: look up user: %v", ErrStorageUnavailable, err)
	}
	if record.Email == "" {
		slog.Info("password reset requested for an account without email", "user_id", record.UserID)
		return nil
	}

	token, hash, err := NewResetToken()
	if err != nil {
		return fmt.Errorf("generate reset token: %w", err)
	}
	now := time.Now().UTC()
	if err := s.store.CreatePasswordReset(storage.PasswordResetRecord{
		UserID: record.UserID, TokenHash: hash, CreatedAt: now, ExpiresAt: now.Add(PasswordResetTTL),
	}); err != nil {
		return fmt.Errorf("%w: store reset: %v", ErrStorageUnavailable, err)
	}

	link := ResetLink(s.mail.publicURL, token)
	ctx, cancel := context.WithTimeout(context.Background(), mailSendTimeout)
	defer cancel()
	err = s.mail.sender.Send(ctx, mail.Message{
		To:      record.Email,
		Subject: "Reset your chess password",
		Body: fmt.Sprintf(`Hello %s,

Someone asked to reset the password of your chess account at %s.
If it was you, open this link within the next hour:

  %s

If you did not ask for this, ignore this message: your password stays as
it is and nobody else can use the link without this email.
`, record.Username, s.mail.publicURL.Host, link),
	})
	if err != nil {
		slog.Error("password reset email failed", "user_id", record.UserID, "error", err)
		return ErrMailFailed
	}
	slog.Info("password reset email sent", "user_id", record.UserID)
	return nil
}

// ResetPassword sets a new password for the account whose pending reset
// token this is, ends its other sessions, and signs the user in. The token
// works once and only until its expiry.
func (s *Service) ResetPassword(token, password string) (*User, string, error) {
	if s.store == nil {
		return nil, "", ErrStorageDisabled
	}
	if raw, err := base64.RawURLEncoding.DecodeString(token); err != nil || len(raw) != resetTokenBytes {
		return nil, "", ErrResetInvalid
	}
	now := time.Now().UTC()
	userID, err := s.store.ConsumePasswordReset(hashResetToken(token), now)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", ErrResetInvalid
		}
		return nil, "", fmt.Errorf("%w: consume reset: %v", ErrStorageUnavailable, err)
	}

	passwordHash, err := s.hashPassword(password)
	if err != nil {
		if errors.Is(err, ErrAuthBusy) {
			return nil, "", err
		}
		return nil, "", fmt.Errorf("failed to hash password: %w", err)
	}
	if err := s.store.ResetUserPassword(userID, passwordHash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", ErrResetInvalid
		}
		return nil, "", fmt.Errorf("%w: reset password: %v", ErrStorageUnavailable, err)
	}
	record, err := s.store.GetUserByID(userID)
	if err != nil {
		return nil, "", fmt.Errorf("%w: look up user: %v", ErrStorageUnavailable, err)
	}
	sessionID := uuid.NewString()
	if err := s.store.CreateSession(storage.SessionRecord{
		SessionID: sessionID, UserID: userID, CreatedAt: now, ExpiresAt: now.Add(SessionTTL),
	}); err != nil {
		return nil, "", fmt.Errorf("%w: create session: %v", ErrStorageUnavailable, err)
	}
	slog.Info("password reset completed", "user_id", userID)
	return &User{
		UserID: record.UserID, Username: record.Username, Email: record.Email, CreatedAt: record.CreatedAt,
	}, sessionID, nil
}
