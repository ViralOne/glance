// Package mailer sends the digest and alert emails over SMTP.
//
// It speaks SMTP directly with the standard library rather than taking a
// dependency: Glance sends a handful of short messages a week to one server
// the operator configured, which is the case net/smtp handles well. Nothing
// is queued — a send either works now or is logged and dropped, because a
// retry queue for "your weekly traffic summary" is more machinery than the
// message is worth.
package mailer

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// ErrNotConfigured is returned when no SMTP host is set.
var ErrNotConfigured = errors.New("email is not configured: set GLANCE_SMTP_HOST and GLANCE_SMTP_FROM")

// Config is the SMTP connection.
type Config struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string
	// TLS is "starttls", "tls" (implicit) or "none".
	TLS string
}

// Mailer sends messages.
type Mailer struct {
	cfg Config
	log *slog.Logger
	now func() time.Time
}

// New returns a Mailer. A zero Host makes every send return ErrNotConfigured,
// so callers can hold a non-nil Mailer without checking configuration first.
func New(cfg Config, log *slog.Logger) *Mailer {
	if cfg.Port == 0 {
		cfg.Port = 587
	}
	if cfg.TLS == "" {
		cfg.TLS = "starttls"
	}
	return &Mailer{cfg: cfg, log: log, now: time.Now}
}

// Configured reports whether mail can be sent.
func (m *Mailer) Configured() bool {
	return m != nil && m.cfg.Host != "" && m.cfg.From != ""
}

// Message is one email.
type Message struct {
	To      string
	Subject string
	// Text is the plain-text body; HTML is optional and sent as the richer
	// alternative when present.
	Text string
	HTML string
}

// ValidAddress reports whether addr is a usable email address.
func ValidAddress(addr string) bool {
	a, err := mail.ParseAddress(strings.TrimSpace(addr))
	return err == nil && a.Address != ""
}

// Send delivers one message.
func (m *Mailer) Send(msg Message) error {
	if !m.Configured() {
		return ErrNotConfigured
	}
	if !ValidAddress(msg.To) {
		return fmt.Errorf("%q is not a valid email address", msg.To)
	}
	addr := net.JoinHostPort(m.cfg.Host, strconv.Itoa(m.cfg.Port))
	body := m.compose(msg)

	client, err := m.dial(addr)
	if err != nil {
		return err
	}
	defer client.Close()
	if m.cfg.User != "" {
		if err := client.Auth(smtp.PlainAuth("", m.cfg.User, m.cfg.Password, m.cfg.Host)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	from := m.cfg.From
	if a, err := mail.ParseAddress(from); err == nil {
		from = a.Address
	}
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("smtp from: %w", err)
	}
	to := strings.TrimSpace(msg.To)
	if a, err := mail.ParseAddress(to); err == nil {
		to = a.Address
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("smtp to: %w", err)
	}
	wc, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := wc.Write(body); err != nil {
		wc.Close()
		return err
	}
	if err := wc.Close(); err != nil {
		return err
	}
	return client.Quit()
}

func (m *Mailer) dial(addr string) (*smtp.Client, error) {
	if m.cfg.TLS == "tls" {
		conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: m.cfg.Host, MinVersion: tls.VersionTLS12})
		if err != nil {
			return nil, fmt.Errorf("smtp dial (implicit tls): %w", err)
		}
		return smtp.NewClient(conn, m.cfg.Host)
	}
	client, err := smtp.Dial(addr)
	if err != nil {
		return nil, fmt.Errorf("smtp dial: %w", err)
	}
	if m.cfg.TLS == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			client.Close()
			return nil, errors.New("smtp server does not offer STARTTLS; set GLANCE_SMTP_TLS=none to send in the clear")
		}
		if err := client.StartTLS(&tls.Config{ServerName: m.cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
			client.Close()
			return nil, fmt.Errorf("starttls: %w", err)
		}
	}
	return client, nil
}

// compose builds the RFC 5322 message. A multipart/alternative is only used
// when there is HTML to offer, so a plain digest stays a plain message.
func (m *Mailer) compose(msg Message) []byte {
	var b strings.Builder
	write := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	write("From", m.cfg.From)
	write("To", strings.TrimSpace(msg.To))
	write("Subject", mime.QEncoding.Encode("utf-8", msg.Subject))
	write("Date", m.now().Format(time.RFC1123Z))
	write("MIME-Version", "1.0")
	// Digests and alerts are transactional, not marketing, but auto-replies
	// to them are still noise.
	write("Auto-Submitted", "auto-generated")
	if msg.HTML == "" {
		write("Content-Type", `text/plain; charset="utf-8"`)
		b.WriteString("\r\n")
		b.WriteString(crlf(msg.Text))
		return []byte(b.String())
	}
	boundary := "glance-" + strconv.FormatInt(m.now().UnixNano(), 36)
	write("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	b.WriteString("\r\n")
	for _, part := range []struct{ ctype, body string }{
		{`text/plain; charset="utf-8"`, msg.Text},
		{`text/html; charset="utf-8"`, msg.HTML},
	} {
		b.WriteString("--" + boundary + "\r\n")
		b.WriteString("Content-Type: " + part.ctype + "\r\n\r\n")
		b.WriteString(crlf(part.body))
		b.WriteString("\r\n")
	}
	b.WriteString("--" + boundary + "--\r\n")
	return []byte(b.String())
}

// crlf normalises line endings to CRLF.
//
// It deliberately does not dot-stuff: smtp.Client.Data returns a
// textproto.DotWriter, which does that already. Doing it here as well sent
// every line starting with a dot with two of them.
func crlf(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}
