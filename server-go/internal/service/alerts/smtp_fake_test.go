package alerts

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A minimal SMTP server for the mailer tests: EHLO, STARTTLS (or implicit
// TLS), AUTH PLAIN, MAIL, RCPT, DATA, QUIT. It records what it received and
// whether the session was encrypted and authenticated.

type fakeMail struct {
	from   string
	to     []string
	data   string
	tls    bool
	authed bool
}

type fakeSMTP struct {
	ln            net.Listener
	implicitTLS   bool
	offerStartTLS bool
	cert          tls.Certificate
	pool          *x509.CertPool
	user, pass    string
	failMail      atomic.Int32

	mu    sync.Mutex
	mails []fakeMail
}

func testCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fake smtp"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true,
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

func newFakeSMTP(t *testing.T, implicitTLS, offerStartTLS bool) *fakeSMTP {
	t.Helper()
	cert, pool := testCert(t)
	f := &fakeSMTP{implicitTLS: implicitTLS, offerStartTLS: offerStartTLS, cert: cert, pool: pool, user: "alerts", pass: "s3cret"}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if implicitTLS {
		ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}})
	}
	f.ln = ln
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.handle(c)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

func (f *fakeSMTP) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeSMTP) received() []fakeMail {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeMail(nil), f.mails...)
}

func (f *fakeSMTP) handle(c net.Conn) {
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	isTLS := f.implicitTLS
	tp := textproto.NewConn(c)
	_ = tp.PrintfLine("220 fake ESMTP")
	authed := false
	var cur fakeMail
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch strings.ToUpper(fields[0]) {
		case "EHLO", "HELO":
			lines := []string{"fake"}
			if !isTLS && f.offerStartTLS {
				lines = append(lines, "STARTTLS")
			}
			if isTLS {
				lines = append(lines, "AUTH PLAIN")
			}
			lines = append(lines, "8BITMIME")
			for i, l := range lines {
				sep := "-"
				if i == len(lines)-1 {
					sep = " "
				}
				_ = tp.PrintfLine("250%s%s", sep, l)
			}
		case "STARTTLS":
			_ = tp.PrintfLine("220 go ahead")
			tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{f.cert}})
			if err := tc.Handshake(); err != nil {
				return
			}
			c, tp, isTLS = tc, textproto.NewConn(tc), true
		case "AUTH":
			if len(fields) < 2 || strings.ToUpper(fields[1]) != "PLAIN" {
				_ = tp.PrintfLine("504 unsupported")
				continue
			}
			resp := ""
			if len(fields) >= 3 {
				resp = fields[2]
			} else {
				// No initial response (curl's default): ask for it.
				_ = tp.PrintfLine("334 ")
				if resp, err = tp.ReadLine(); err != nil {
					return
				}
			}
			raw, _ := base64.StdEncoding.DecodeString(resp)
			parts := strings.Split(string(raw), "\x00")
			if len(parts) == 3 && parts[1] == f.user && parts[2] == f.pass {
				authed = true
				_ = tp.PrintfLine("235 2.7.0 accepted")
			} else {
				_ = tp.PrintfLine("535 5.7.8 bad credentials")
			}
		case "MAIL":
			if f.failMail.Load() > 0 {
				f.failMail.Add(-1)
				_ = tp.PrintfLine("451 4.3.0 try again later")
				continue
			}
			if !authed {
				_ = tp.PrintfLine("530 5.7.0 authentication required")
				continue
			}
			cur = fakeMail{from: line}
			_ = tp.PrintfLine("250 ok")
		case "RCPT":
			cur.to = append(cur.to, line)
			_ = tp.PrintfLine("250 ok")
		case "DATA":
			_ = tp.PrintfLine("354 send it")
			lines, err := tp.ReadDotLines()
			if err != nil {
				return
			}
			cur.data, cur.tls, cur.authed = strings.Join(lines, "\n"), isTLS, authed
			f.mu.Lock()
			f.mails = append(f.mails, cur)
			f.mu.Unlock()
			_ = tp.PrintfLine("250 queued")
		case "RSET", "NOOP":
			_ = tp.PrintfLine("250 ok")
		case "QUIT":
			_ = tp.PrintfLine("221 bye")
			return
		default:
			_ = tp.PrintfLine("502 unknown")
		}
	}
}
