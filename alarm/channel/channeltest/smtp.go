package channeltest

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// SMTPOptions shapes the fake server.
type SMTPOptions struct {
	Implicit   bool   // TLS from the first byte (port 465 style)
	NoSTARTTLS bool   // do not offer STARTTLS (AUTH is still offered, to tempt a client)
	User, Pass string // require AUTH PLAIN with these when User is set
	RcptCode   int    // reply to RCPT TO with this code and text echoing the address
	Hang       bool   // accept, then never answer
}

// Mail is one accepted message.
type Mail struct {
	From   string
	To     []string
	Data   string
	TLS    bool
	Authed bool
}

// SMTP is a running fake server.
type SMTP struct {
	Addr   string // 127.0.0.1:port
	CAFile string // PEM of the server's certificate

	o    SMTPOptions
	ln   net.Listener
	cert tls.Certificate

	mu        sync.Mutex
	mails     []Mail
	authTried bool
	conns     []net.Conn
	closed    chan struct{}
}

// Mails returns what the server accepted.
func (s *SMTP) Mails() []Mail {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Mail(nil), s.mails...)
}

// AuthTried reports whether any client sent an AUTH command.
func (s *SMTP) AuthTried() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authTried
}

// selfSigned makes a certificate for 127.0.0.1 and localhost and writes
// its PEM to dir.
func selfSigned(t testing.TB, dir string) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, file
}

// NewSMTP starts the server on 127.0.0.1; t closes it.
func NewSMTP(t testing.TB, o SMTPOptions) *SMTP {
	t.Helper()
	s := &SMTP{o: o, closed: make(chan struct{})}
	var caFile string
	s.cert, caFile = selfSigned(t, t.TempDir())
	s.CAFile = caFile
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if o.Implicit {
		ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{s.cert}})
	}
	s.ln = ln
	s.Addr = ln.Addr().String()
	go s.accept()
	t.Cleanup(func() {
		close(s.closed)
		_ = ln.Close()
		s.mu.Lock()
		for _, c := range s.conns {
			_ = c.Close()
		}
		s.mu.Unlock()
	})
	return s
}

func (s *SMTP) accept() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns = append(s.conns, c)
		s.mu.Unlock()
		go s.serve(c)
	}
}

func (s *SMTP) serve(c net.Conn) {
	defer c.Close()
	if s.o.Hang {
		<-s.closed
		return
	}
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	isTLS := s.o.Implicit
	r := bufio.NewReader(c)
	say := func(f string, a ...any) { fmt.Fprintf(c, f+"\r\n", a...) }
	say("220 localhost ESMTP")
	var from string
	var to []string
	authed := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(verb, "EHLO"), strings.HasPrefix(verb, "HELO"):
			say("250-localhost")
			if !s.o.NoSTARTTLS && !isTLS {
				say("250-STARTTLS")
			}
			if s.o.User != "" {
				say("250-AUTH PLAIN")
			}
			say("250 8BITMIME")
		case verb == "STARTTLS":
			if s.o.NoSTARTTLS || isTLS {
				say("502 not offered")
				continue
			}
			say("220 ready")
			tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{s.cert}})
			if tc.Handshake() != nil {
				return
			}
			c, isTLS = tc, true
			r = bufio.NewReader(tc)
			from, to = "", nil
		case strings.HasPrefix(verb, "AUTH"):
			s.mu.Lock()
			s.authTried = true
			s.mu.Unlock()
			f := strings.Fields(line)
			var resp string
			if len(f) >= 3 {
				resp = f[2]
			} else {
				say("334 ")
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				resp = strings.TrimSpace(l)
			}
			dec, _ := base64.StdEncoding.DecodeString(resp)
			p := strings.Split(string(dec), "\x00")
			if len(p) == 3 && p[1] == s.o.User && p[2] == s.o.Pass && s.o.User != "" {
				authed = true
				say("235 ok")
			} else {
				say("535 5.7.8 bad credentials")
			}
		case strings.HasPrefix(verb, "MAIL FROM:"):
			if s.o.User != "" && !authed {
				say("530 5.7.0 auth required")
				continue
			}
			from = angle(line[len("MAIL FROM:"):])
			to = nil
			say("250 ok")
		case strings.HasPrefix(verb, "RCPT TO:"):
			addr := angle(line[len("RCPT TO:"):])
			if s.o.RcptCode != 0 {
				say("%d mailbox %s unavailable", s.o.RcptCode, addr)
				continue
			}
			to = append(to, addr)
			say("250 ok")
		case verb == "DATA":
			say("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(strings.TrimPrefix(l, "."))
			}
			s.mu.Lock()
			s.mails = append(s.mails, Mail{From: from, To: to, Data: b.String(), TLS: isTLS, Authed: authed})
			s.mu.Unlock()
			say("250 queued")
		case verb == "RSET", verb == "NOOP":
			say("250 ok")
		case verb == "QUIT":
			say("221 bye")
			return
		default:
			say("502 unknown")
		}
	}
}

// angle returns the address inside <...>, dropping ESMTP parameters.
func angle(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, ">"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimPrefix(s, "<")
}
