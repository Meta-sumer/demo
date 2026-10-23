package alerts

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"binarytrading/internal/config"
)

// Email over SMTP with the standard library. Port 465 is implicit TLS; any
// other port (587, 25, 2525) must offer STARTTLS, which is always used: the
// alert text and the SMTP credentials never travel in clear text. The server
// certificate is verified against the system roots.

// Message is one email.
type Message struct {
	Subject string
	Body    string
}

// Sender sends one message to the configured recipients.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

type smtpSender struct {
	s config.AlertSettings
	// tlsConfig overrides the TLS settings (tests trust their own CA).
	tlsConfig *tls.Config
}

// implicitTLSPort is the port that means TLS from the first byte (a
// variable so the tests can point it at their server).
var implicitTLSPort = 465

// newSender builds the sender the worker uses; tests replace it.
var newSender = func(s config.AlertSettings) Sender { return &smtpSender{s: s} }

func (m *smtpSender) tls() *tls.Config {
	c := &tls.Config{MinVersion: tls.VersionTLS12}
	if m.tlsConfig != nil {
		c = m.tlsConfig.Clone()
	}
	if c.ServerName == "" {
		c.ServerName = m.s.SMTPHost
	}
	return c
}

func (m *smtpSender) Send(ctx context.Context, msg Message) error {
	s := m.s
	timeout := time.Duration(s.SMTPTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	addr := net.JoinHostPort(s.SMTPHost, strconv.Itoa(s.SMTPPort))
	dialer := &net.Dialer{Deadline: deadline}

	var conn net.Conn
	var err error
	implicit := s.SMTPPort == implicitTLSPort
	if implicit {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, m.tls())
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("smtp connect %s: %w", addr, err)
	}
	_ = conn.SetDeadline(deadline)
	c, err := smtp.NewClient(conn, s.SMTPHost)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp greeting: %w", err)
	}
	defer c.Close()
	if err := c.Hello(localName()); err != nil {
		return fmt.Errorf("smtp EHLO: %w", err)
	}
	if !implicit {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("smtp server does not offer STARTTLS; refusing to send in clear text")
		}
		if err := c.StartTLS(m.tls()); err != nil {
			return fmt.Errorf("smtp STARTTLS: %w", err)
		}
	}
	if s.SMTPUsername != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return errors.New("smtp server does not offer AUTH")
		}
		if err := c.Auth(smtp.PlainAuth("", s.SMTPUsername, s.SMTPPassword, s.SMTPHost)); err != nil {
			return fmt.Errorf("smtp AUTH: %w", err)
		}
	}
	if err := c.Mail(s.From); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	for _, to := range s.To {
		if err := c.Rcpt(to); err != nil {
			return fmt.Errorf("smtp RCPT TO %s: %w", to, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if _, err := w.Write(buildMessage(s, msg, time.Now())); err != nil {
		_ = w.Close()
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp end of data: %w", err)
	}
	return c.Quit()
}

func localName() string {
	h, err := os.Hostname()
	if err != nil || h == "" || strings.ContainsAny(h, " \r\n") {
		return "localhost"
	}
	return h
}

// headerSafe drops line breaks so no value can inject a header.
func headerSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' {
			return ' '
		}
		return r
	}, s)
}

// buildMessage renders RFC 5322 text: UTF-8, quoted-printable, CRLF lines.
func buildMessage(s config.AlertSettings, msg Message, now time.Time) []byte {
	domain := "localhost"
	if i := strings.LastIndex(s.From, "@"); i >= 0 {
		domain = headerSafe(s.From[i+1:])
	}
	var b bytes.Buffer
	h := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	h("From", headerSafe(s.From))
	h("To", headerSafe(strings.Join(s.To, ", ")))
	h("Subject", mime.QEncoding.Encode("utf-8", headerSafe(msg.Subject)))
	h("Date", now.Format(time.RFC1123Z))
	h("Message-ID", "<"+uuid.NewString()+"@"+domain+">")
	h("MIME-Version", "1.0")
	h("Content-Type", "text/plain; charset=UTF-8")
	h("Content-Transfer-Encoding", "quoted-printable")
	h("Auto-Submitted", "auto-generated")
	b.WriteString("\r\n")
	body := strings.ReplaceAll(strings.ReplaceAll(msg.Body, "\r\n", "\n"), "\n", "\r\n")
	qp := quotedprintable.NewWriter(&b)
	_, _ = qp.Write([]byte(body))
	_ = qp.Close()
	b.WriteString("\r\n")
	return b.Bytes()
}
