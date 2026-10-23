package alerts

import (
	"context"
	"crypto/tls"
	"mime"
	"strings"
	"testing"
	"time"

	"binarytrading/internal/config"
)

// The SMTP client against the fake server: STARTTLS on a submission port,
// implicit TLS on 465, never clear text, credentials only after TLS.

func smtpSettings(port int) config.AlertSettings {
	s := config.DefaultAlertSettings()
	s.EmailEnabled, s.SMTPHost, s.SMTPPort = true, "127.0.0.1", port
	s.SMTPUsername, s.SMTPPassword = "alerts", "s3cret"
	s.From, s.To = "alerts@wintratrade.test", []string{"ops@wintratrade.test", "risk@wintratrade.test"}
	s.SMTPTimeoutSeconds = 5
	return s
}

func sendVia(t *testing.T, f *fakeSMTP, s config.AlertSettings, msg Message) error {
	t.Helper()
	sender := &smtpSender{s: s, tlsConfig: &tls.Config{RootCAs: f.pool, MinVersion: tls.VersionTLS12}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return sender.Send(ctx, msg)
}

func TestSMTPStartTLSAuthDelivers(t *testing.T) {
	f := newFakeSMTP(t, false, true)
	err := sendVia(t, f, smtpSettings(f.port()), Message{Subject: "Exposure book not READY – LIVE refused", Body: "line one\nline two"})
	if err != nil {
		t.Fatal(err)
	}
	got := f.received()
	if len(got) != 1 {
		t.Fatalf("mails: %d", len(got))
	}
	m := got[0]
	if !m.tls || !m.authed {
		t.Fatalf("delivered without TLS (%v) or auth (%v)", m.tls, m.authed)
	}
	if len(m.to) != 2 || !strings.Contains(m.from, "alerts@wintratrade.test") {
		t.Fatalf("envelope: %q %q", m.from, m.to)
	}
	dec := new(mime.WordDecoder)
	var subject string
	for _, line := range strings.Split(m.data, "\n") {
		if strings.HasPrefix(line, "Subject: ") {
			subject, _ = dec.DecodeHeader(strings.TrimPrefix(line, "Subject: "))
		}
	}
	if subject != "Exposure book not READY – LIVE refused" {
		t.Fatalf("subject %q", subject)
	}
	if !strings.Contains(m.data, "line one") || !strings.Contains(m.data, "Content-Transfer-Encoding: quoted-printable") {
		t.Fatalf("body: %q", m.data)
	}
}

func TestSMTPImplicitTLSDelivers(t *testing.T) {
	f := newFakeSMTP(t, true, false)
	// The fake's random port plays the part of 465.
	saved := implicitTLSPort
	implicitTLSPort = f.port()
	t.Cleanup(func() { implicitTLSPort = saved })
	if err := sendVia(t, f, smtpSettings(f.port()), Message{Subject: "implicit", Body: "hello"}); err != nil {
		t.Fatal(err)
	}
	got := f.received()
	if len(got) != 1 || !got[0].tls || !got[0].authed {
		t.Fatalf("implicit TLS delivery: %+v", got)
	}
}

func TestSMTPRefusesServerWithoutStartTLS(t *testing.T) {
	f := newFakeSMTP(t, false, false)
	err := sendVia(t, f, smtpSettings(f.port()), Message{Subject: "x", Body: "y"})
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("clear-text server accepted: %v", err)
	}
	if len(f.received()) != 0 {
		t.Fatal("a message was delivered in clear text")
	}
}

func TestSMTPWrongPasswordFails(t *testing.T) {
	f := newFakeSMTP(t, false, true)
	s := smtpSettings(f.port())
	s.SMTPPassword = "wrong-pass-123"
	err := sendVia(t, f, s, Message{Subject: "x", Body: "y"})
	if err == nil || !strings.Contains(err.Error(), "AUTH") {
		t.Fatalf("bad credentials: %v", err)
	}
	if strings.Contains(err.Error(), "wrong-pass-123") {
		t.Fatal("the password appears in the error")
	}
	if len(f.received()) != 0 {
		t.Fatal("delivered without authentication")
	}
}

func TestMessageHeadersCannotBeInjected(t *testing.T) {
	s := smtpSettings(587)
	raw := string(buildMessage(s, Message{Subject: "hello\r\nBcc: victim@example.com", Body: "x"}, time.Now()))
	headers := raw[:strings.Index(raw, "\r\n\r\n")]
	for _, line := range strings.Split(headers, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "bcc:") {
			t.Fatalf("header injected: %q", headers)
		}
	}
	if !strings.Contains(headers, "Auto-Submitted: auto-generated") || !strings.Contains(headers, "To: ops@wintratrade.test, risk@wintratrade.test") {
		t.Fatalf("headers: %q", headers)
	}
}
