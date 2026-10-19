// Package mail sends the server's few transactional messages, today the
// password-reset link. A Sender is an SMTP relay (production), a directory
// of .eml files (development and tests), or nothing, in which case the
// features that need mail are off.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Message is one plain-text email.
type Message struct {
	To      string
	Subject string
	Body    string
}

// Sender delivers messages.
type Sender interface {
	Send(ctx context.Context, m Message) error
	// Describe names the transport for logs, without secrets.
	Describe() string
}

// SMTP delivers through a relay. Username and Password are optional: a relay
// inside the same network usually accepts mail from this host without them.
// STARTTLS is used when the relay offers it; credentials are only ever sent
// over TLS (or to localhost), which net/smtp enforces.
type SMTP struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string // "Chess <chess@example.com>" or a bare address
	// Timeout bounds the whole exchange; 30 seconds when zero.
	Timeout time.Duration
}

// Describe names the relay.
func (s *SMTP) Describe() string {
	auth := "no authentication"
	if s.Username != "" {
		auth = "as " + s.Username
	}
	return fmt.Sprintf("smtp %s:%d (%s), from %s", s.Host, s.Port, auth, s.From)
}

// Send delivers one message through the relay.
func (s *SMTP) Send(ctx context.Context, m Message) error {
	from, err := mail.ParseAddress(s.From)
	if err != nil {
		return fmt.Errorf("mail from %q: %w", s.From, err)
	}
	to, err := mail.ParseAddress(m.To)
	if err != nil {
		return fmt.Errorf("mail to %q: %w", m.To, err)
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if deadline, ok := ctx.Deadline(); ok {
		if until := time.Until(deadline); until < timeout {
			timeout = until
		}
	}
	addr := net.JoinHostPort(s.Host, fmt.Sprint(s.Port))
	conn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", addr, err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}

	client, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		return fmt.Errorf("smtp handshake with %s: %w", addr, err)
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("starttls with %s: %w", addr, err)
		}
	}
	if s.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			return fmt.Errorf("smtp authentication with %s: %w", addr, err)
		}
	}
	if err := client.Mail(from.Address); err != nil {
		return fmt.Errorf("smtp from: %w", err)
	}
	if err := client.Rcpt(to.Address); err != nil {
		return fmt.Errorf("smtp to: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(Format(s.From, m)); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp send: %w", err)
	}
	return client.Quit()
}

// Dir writes each message to a file in a directory instead of sending it,
// for development and tests. The file holds exactly what SMTP would carry.
type Dir struct {
	Path string
	From string
}

// Describe names the directory.
func (d *Dir) Describe() string {
	return fmt.Sprintf("directory %s, from %s", d.Path, d.From)
}

// Send writes the message as <unix-nanos>-<random>.eml.
func (d *Dir) Send(_ context.Context, m Message) error {
	if _, err := mail.ParseAddress(m.To); err != nil {
		return fmt.Errorf("mail to %q: %w", m.To, err)
	}
	if err := os.MkdirAll(d.Path, 0o700); err != nil {
		return err
	}
	var nonce [4]byte
	rand.Read(nonce[:])
	name := fmt.Sprintf("%d-%s.eml", time.Now().UnixNano(), hex.EncodeToString(nonce[:]))
	return os.WriteFile(filepath.Join(d.Path, name), Format(d.From, m), 0o600)
}

// Format renders the message as RFC 5322 text with CRLF line ends: headers,
// a blank line, the UTF-8 body. Header values are kept to one line.
func Format(from string, m Message) []byte {
	var nonce [8]byte
	rand.Read(nonce[:])
	domain := "localhost"
	if addr, err := mail.ParseAddress(from); err == nil {
		if at := strings.LastIndex(addr.Address, "@"); at >= 0 {
			domain = addr.Address[at+1:]
		}
	}
	var b bytes.Buffer
	header := func(name, value string) {
		value = strings.NewReplacer("\r", " ", "\n", " ").Replace(value)
		fmt.Fprintf(&b, "%s: %s\r\n", name, value)
	}
	header("From", from)
	header("To", m.To)
	header("Subject", m.Subject)
	header("Date", time.Now().UTC().Format(time.RFC1123Z))
	header("Message-ID", fmt.Sprintf("<%d.%s@%s>", time.Now().UnixNano(), hex.EncodeToString(nonce[:]), domain))
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=utf-8")
	header("Content-Transfer-Encoding", "8bit")
	header("Auto-Submitted", "auto-generated")
	b.WriteString("\r\n")
	body := strings.ReplaceAll(strings.ReplaceAll(m.Body, "\r\n", "\n"), "\n", "\r\n")
	b.WriteString(body)
	if !strings.HasSuffix(body, "\r\n") {
		b.WriteString("\r\n")
	}
	return b.Bytes()
}

// ErrNoSender is returned by Validate for an empty configuration.
var ErrNoSender = errors.New("no mail transport configured")

// Config is the server's mail configuration, from its flags. SMTPHost or Dir
// selects the transport; both empty means no mail.
type Config struct {
	SMTPHost         string
	SMTPPort         int
	SMTPUser         string
	SMTPPasswordFile string
	Dir              string
	From             string
}

// Build returns the configured Sender, nil (with ErrNoSender) when nothing is
// configured, or an error for an inconsistent configuration.
func (c Config) Build() (Sender, error) {
	switch {
	case c.SMTPHost != "" && c.Dir != "":
		return nil, errors.New("-smtp-host and -mail-dir are exclusive")
	case c.SMTPHost == "" && c.Dir == "":
		return nil, ErrNoSender
	}
	if c.From == "" {
		return nil, errors.New("-mail-from is required with a mail transport")
	}
	if _, err := mail.ParseAddress(c.From); err != nil {
		return nil, fmt.Errorf("-mail-from %q: %w", c.From, err)
	}
	if c.Dir != "" {
		if err := os.MkdirAll(c.Dir, 0o700); err != nil {
			return nil, fmt.Errorf("-mail-dir: %w", err)
		}
		return &Dir{Path: c.Dir, From: c.From}, nil
	}
	port := c.SMTPPort
	if port <= 0 {
		port = 25
	}
	s := &SMTP{Host: c.SMTPHost, Port: port, Username: c.SMTPUser, From: c.From}
	if c.SMTPPasswordFile != "" {
		secret, err := readSecretFile(c.SMTPPasswordFile)
		if err != nil {
			return nil, fmt.Errorf("-smtp-password-file: %w", err)
		}
		s.Password = secret
	}
	if s.Password != "" && s.Username == "" {
		return nil, errors.New("-smtp-password-file needs -smtp-user")
	}
	return s, nil
}

// readSecretFile reads a one-line secret from a file that only its owner may
// read, as the JWT key file is.
func readSecretFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s is readable by group or others (mode %04o); use mode 0600", path, info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	secret := strings.TrimRight(string(data), "\r\n")
	if secret == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return secret, nil
}
