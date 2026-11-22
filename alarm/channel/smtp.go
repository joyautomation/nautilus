package channel

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/alarm/escalate"
)

type emailSender struct {
	cfg     Email
	dir     Directory
	host    string
	tls     *tls.Config
	timeout time.Duration
}

func (e *emailSender) attempt(ctx context.Context, m escalate.Message) error {
	to, err := address(e.dir, m.To, KindEmail)
	if err != nil {
		return err
	}
	if ValidEmail(to) != nil {
		return escalate.Failed(escalate.FailNoContact)
	}
	var pass string
	if e.cfg.Username != "" {
		if pass, err = secret(e.dir, SecretEmailPassword); err != nil {
			return err
		}
	}
	msg, err := e.compose(to, m)
	if err != nil {
		return err
	}
	return e.deliver(ctx, to, pass, msg)
}

// headerValue removes every control character from a header value.
func headerValue(s string) string { return clean(s) }

func (e *emailSender) compose(to string, m escalate.Message) ([]byte, error) {
	from := e.cfg.From
	domain := "localhost"
	if i := strings.LastIndex(from, "@"); i >= 0 && i+1 < len(from) {
		domain = headerValue(from[i+1:])
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, escalate.Failed(escalate.FailUnknown)
	}
	subj := headerValue(subject(m))
	subj = mime.QEncoding.Encode("utf-8", subj)
	var b bytes.Buffer
	h := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	h("From", headerValue(from))
	h("To", headerValue(to))
	h("Subject", subj)
	h("Date", time.Now().UTC().Format(time.RFC1123Z))
	h("Message-ID", "<"+hex.EncodeToString(id[:])+"@"+domain+">")
	h("MIME-Version", "1.0")
	h("Content-Type", "text/plain; charset=utf-8")
	h("Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")
	w := quotedprintable.NewWriter(&b)
	if _, err := w.Write([]byte(body(m))); err != nil {
		return nil, escalate.Failed(escalate.FailUnknown)
	}
	if err := w.Close(); err != nil {
		return nil, escalate.Failed(escalate.FailUnknown)
	}
	return b.Bytes(), nil
}

func (e *emailSender) deliver(ctx context.Context, to, pass string, msg []byte) error {
	d := net.Dialer{Timeout: e.timeout}
	conn, err := d.DialContext(ctx, "tcp", e.cfg.SMTP)
	if err != nil {
		if c := classify(ctx, err); c == escalate.FailTimeout {
			return escalate.Failed(c)
		}
		return escalate.Failed(escalate.FailUnreachable)
	}
	dl, ok := ctx.Deadline()
	if !ok {
		dl = time.Now().Add(e.timeout)
	}
	_ = conn.SetDeadline(dl)
	// A server that stops answering must not hold Send past its context.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	defer conn.Close()

	implicit := e.cfg.TLS == SMTPImplicit
	var nc net.Conn = conn
	if implicit {
		tc := tls.Client(conn, e.tls)
		if err := tc.HandshakeContext(ctx); err != nil {
			return tlsFailure(ctx, err)
		}
		nc = tc
	}
	c, err := smtp.NewClient(nc, e.host)
	if err != nil {
		return smtpFailure(ctx, err)
	}
	defer c.Close()
	if err := c.Hello("localhost"); err != nil {
		return smtpFailure(ctx, err)
	}
	if !implicit {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(e.tls); err != nil {
				return tlsFailure(ctx, err)
			}
		} else if !(e.tls.InsecureSkipVerify && e.cfg.Username == "") {
			// No TLS offered: nothing more is sent, least of all AUTH.
			return escalate.Failed(escalate.FailTLS)
		}
	}
	if e.cfg.Username != "" {
		if _, ok := c.TLSConnectionState(); !ok {
			return escalate.Failed(escalate.FailTLS)
		}
		if ok, _ := c.Extension("AUTH"); !ok {
			return escalate.Failed(escalate.FailAuth)
		}
		if err := c.Auth(smtp.PlainAuth("", e.cfg.Username, pass, e.host)); err != nil {
			return smtpFailure(ctx, err)
		}
	}
	if err := c.Mail(e.cfg.From); err != nil {
		return smtpFailure(ctx, err)
	}
	if err := c.Rcpt(to); err != nil {
		return smtpFailure(ctx, err)
	}
	w, err := c.Data()
	if err != nil {
		return smtpFailure(ctx, err)
	}
	if _, err := w.Write(msg); err != nil {
		return smtpFailure(ctx, err)
	}
	if err := w.Close(); err != nil {
		return smtpFailure(ctx, err)
	}
	_ = c.Quit()
	return nil
}

// tlsFailure is a failed handshake: a timeout stays a timeout, anything
// else is the transport's security refusing.
func tlsFailure(ctx context.Context, err error) error {
	if c := classify(ctx, err); c == escalate.FailTimeout {
		return escalate.Failed(c)
	}
	return escalate.Failed(escalate.FailTLS)
}

// smtpFailure maps an SMTP error to a class and the reply code; the
// server's text (it may name the mailbox) is dropped.
func smtpFailure(ctx context.Context, err error) error {
	var te *textproto.Error
	if errors.As(err, &te) {
		se := &escalate.SendError{Code: te.Code, Proto: "smtp"}
		switch {
		case te.Code == 530 || te.Code == 534 || te.Code == 535:
			se.Class = escalate.FailAuth
		case te.Code >= 500 && te.Code < 600:
			se.Class = escalate.FailRejected
		case te.Code >= 400 && te.Code < 500:
			se.Class = escalate.FailProvider
		default:
			se.Class = escalate.FailUnknown
		}
		return se
	}
	return escalate.Failed(classify(ctx, err))
}
