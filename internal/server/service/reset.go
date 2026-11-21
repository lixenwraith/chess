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
	"sync"
	"time"

	"github.com/lixenwraith/chess/internal/server/mail"
	"github.com/lixenwraith/chess/internal/server/storage"

	"github.com/google/uuid"
)

const (
	// PasswordResetTTL is how long a reset link works.
	PasswordResetTTL = time.Hour
	// PasswordResetCooldown: while an account's link is this new, a request
	// mails nothing and that link keeps working, so requests from many
	// addresses cannot flood its inbox.
	PasswordResetCooldown = 5 * time.Minute
	resetTokenBytes       = 32
	resetQueueCapacity    = 64
	mailSendTimeout       = 20 * time.Second
)

var (
	// ErrMailDisabled reports that no mail transport is configured, so
	// password recovery is off.
	ErrMailDisabled = errors.New("password recovery is not configured")
	// ErrMailBusy reports a full queue of recovery requests.
	ErrMailBusy = errors.New("too many password recovery requests; try again shortly")
	// ErrResetInvalid reports an unknown, used, or expired reset token.
	ErrResetInvalid = errors.New("invalid or expired reset link")
)

// mailer is the configured transport, the client URL reset links open, and
// the queue of recovery requests one sender works through, so that a request
// is answered before its account is looked up and its timing reveals nothing.
type mailer struct {
	sender    mail.Sender
	publicURL *url.URL
	cooldown  time.Duration

	mu       sync.RWMutex // guards closing requests against a send
	closed   bool
	requests chan string
	ctx      context.Context // cancelled when shutdown stops waiting
	cancel   context.CancelFunc
	done     chan struct{}
}

// SetMailer configures password recovery: sender delivers the email and
// publicURL is the web client's address, which the link opens with
// ?reset=<token>. A nil sender turns recovery off. Call it before serving.
func (s *Service) SetMailer(sender mail.Sender, publicURL string) error {
	var next *mailer
	if sender != nil {
		u, err := url.Parse(publicURL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("public URL %q must be absolute (https://host/chess/web-client/)", publicURL)
		}
		ctx, cancel := context.WithCancel(context.Background())
		next = &mailer{
			sender: sender, publicURL: u, cooldown: PasswordResetCooldown,
			requests: make(chan string, resetQueueCapacity),
			ctx:      ctx, cancel: cancel, done: make(chan struct{}),
		}
		go s.sendPasswordResets(next)
	}
	if s.mail != nil {
		s.mail.close(mailSendTimeout)
	}
	s.mail = next
	return nil
}

// close stops taking requests and waits up to timeout for the queued ones.
// Past it the rest are dropped and the send under way is cut short; close
// still waits for that link's revocation, which needs the store.
func (m *mailer) close(timeout time.Duration) {
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		close(m.requests)
	}
	m.mu.Unlock()
	defer m.cancel()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-m.done:
	case <-timer.C:
		slog.Warn("password recovery stopped before every queued email was sent")
		m.cancel()
		<-m.done
	}
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

// NewSeat returns a guest's seat secret and the hash a game keeps for it,
// made as a reset token is; SeatHash is the hash of a secret presented later.
func NewSeat() (secret string, hash []byte, err error) { return NewResetToken() }

func SeatHash(secret string) []byte { return hashResetToken(secret) }

// ResetLink is the web client URL that opens the password form for token.
func ResetLink(publicURL *url.URL, token string) string {
	u := *publicURL
	q := u.Query()
	q.Set("reset", token)
	u.RawQuery = q.Encode()
	u.Fragment = ""
	return u.String()
}

// RequestPasswordReset queues a reset email for the account named by
// identifier (username or email) and returns before looking it up, so the
// answer and its timing are the same for every identifier.
func (s *Service) RequestPasswordReset(identifier string) error {
	if s.store == nil {
		return ErrStorageDisabled
	}
	m := s.mail
	if m == nil {
		return ErrMailDisabled
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return ErrMailDisabled
	}
	select {
	case m.requests <- identifier:
		return nil
	default:
		return ErrMailBusy
	}
}

// sendPasswordResets mails the queued requests in order until the queue
// closes; those left when shutdown stops waiting are dropped.
func (s *Service) sendPasswordResets(m *mailer) {
	defer close(m.done)
	for identifier := range m.requests {
		if m.ctx.Err() != nil {
			continue
		}
		s.sendPasswordReset(m, identifier)
	}
}

// sendPasswordReset mails a reset link to the account named by identifier
// when it has an email address and no pending link within the cooldown. A link
// that cannot be sent is revoked, so a retry is not held back.
func (s *Service) sendPasswordReset(m *mailer, identifier string) {
	// Shutdown cuts only the send short: the store calls, each bounded, finish,
	// so a link is never left stored unsent.
	persist := context.WithoutCancel(m.ctx)
	identifier = strings.ToLower(strings.TrimSpace(identifier))
	var record *storage.UserRecord
	var err error
	if strings.Contains(identifier, "@") {
		record, err = s.store.GetUserByEmail(persist, identifier)
	} else {
		record, err = s.store.GetUserByUsername(persist, identifier)
	}
	switch {
	case errors.Is(err, sql.ErrNoRows):
		slog.Info("password reset requested for an unknown account")
		return
	case err != nil:
		slog.Error("password reset lookup failed", "error", err)
		return
	case record.Email == "":
		slog.Info("password reset requested for an account without email", "user_id", record.UserID)
		return
	}

	token, hash, err := NewResetToken()
	if err != nil {
		slog.Error("password reset token failed", "error", err)
		return
	}
	now := time.Now().UTC()
	err = s.store.CreatePasswordReset(persist, storage.PasswordResetRecord{
		UserID: record.UserID, TokenHash: hash, CreatedAt: now, ExpiresAt: now.Add(PasswordResetTTL),
	}, m.cooldown)
	switch {
	case errors.Is(err, storage.ErrResetPending):
		slog.Info("password reset not mailed: an earlier link is within the cooldown", "user_id", record.UserID)
		return
	case err != nil:
		slog.Error("password reset not stored", "user_id", record.UserID, "error", err)
		return
	}

	link := ResetLink(m.publicURL, token)
	ctx, cancel := context.WithTimeout(m.ctx, mailSendTimeout)
	defer cancel()
	err = m.sender.Send(ctx, mail.Message{
		To:      record.Email,
		Subject: "Reset your chess password",
		Body: fmt.Sprintf(`Hello %s,

Someone asked to reset the password of your chess account at %s.
If it was you, open this link within the next hour:

  %s

If you did not ask for this, ignore this message: your password stays as
it is and nobody else can use the link without this email.
`, record.Username, m.publicURL.Host, link),
	})
	if err != nil {
		slog.Error("password reset email failed", "user_id", record.UserID, "error", err)
		// Revoked, or the cooldown would hold back a retry for a link never sent.
		if _, err := s.store.ConsumePasswordReset(persist, hash, now); err != nil && !errors.Is(err, sql.ErrNoRows) {
			slog.Error("unsent password reset not revoked", "user_id", record.UserID, "error", err)
		}
		return
	}
	slog.Info("password reset email sent", "user_id", record.UserID)
}

// ResetPassword sets a new password for the account whose pending reset
// token this is, ends its other sessions, and signs the user in. The token
// works once and only until its expiry.
func (s *Service) ResetPassword(ctx context.Context, token, password string) (*User, string, error) {
	if s.store == nil {
		return nil, "", ErrStorageDisabled
	}
	if raw, err := base64.RawURLEncoding.DecodeString(token); err != nil || len(raw) != resetTokenBytes {
		return nil, "", ErrResetInvalid
	}
	now := time.Now().UTC()
	userID, err := s.store.ConsumePasswordReset(ctx, hashResetToken(token), now)
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
	if err := s.store.ResetUserPassword(ctx, userID, passwordHash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", ErrResetInvalid
		}
		return nil, "", fmt.Errorf("%w: reset password: %v", ErrStorageUnavailable, err)
	}
	record, err := s.store.GetUserByID(ctx, userID)
	if err != nil {
		return nil, "", fmt.Errorf("%w: look up user: %v", ErrStorageUnavailable, err)
	}
	sessionID := uuid.NewString()
	if err := s.store.CreateSession(ctx, storage.SessionRecord{
		SessionID: sessionID, UserID: userID, CreatedAt: now, ExpiresAt: now.Add(SessionTTL),
	}); err != nil {
		return nil, "", fmt.Errorf("%w: create session: %v", ErrStorageUnavailable, err)
	}
	slog.Info("password reset completed", "user_id", userID)
	return &User{
		UserID: record.UserID, Username: record.Username, Email: record.Email, CreatedAt: record.CreatedAt,
	}, sessionID, nil
}
