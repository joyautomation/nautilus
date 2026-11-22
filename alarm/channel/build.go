package channel

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/joyautomation/nautilus/alarm/escalate"
)

// Options tunes every channel Build returns.
type Options struct {
	// Timeout bounds one attempt. Default 10s.
	Timeout time.Duration
	// Retries is how many times a transient failure is tried again. Zero
	// means the default, 2; negative means none.
	Retries int
	// Backoff is the first retry delay; it doubles. Default 500ms.
	Backoff time.Duration
}

func (o Options) norm() Options {
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	if o.Retries < 0 {
		o.Retries = 0
	} else if o.Retries == 0 {
		o.Retries = 2
	}
	if o.Backoff <= 0 {
		o.Backoff = 500 * time.Millisecond
	}
	return o
}

// Build makes one escalate.Channel per configured kind. It reads each
// ca-file now, so a missing bundle fails at start and not at the first
// page; addresses and secrets are read from dir on every send.
func Build(c *Config, dir Directory, o Options) (map[string]escalate.Channel, error) {
	out := map[string]escalate.Channel{}
	if c == nil {
		return out, nil
	}
	if dir == nil {
		return nil, errors.New("alarms.channels: no directory for addresses and secrets")
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	o = o.norm()
	pool := func(kind string, t TLS) (*x509.CertPool, error) {
		return loadPool("alarms.channels."+kind+".ca-file", t.CAFile)
	}
	if e := c.Email; e != nil {
		p, err := pool(KindEmail, e.TLSConf)
		if err != nil {
			return nil, err
		}
		host, _, _ := net.SplitHostPort(e.SMTP)
		em := &emailSender{cfg: *e, dir: dir, host: host, tls: tlsConfig(host, e.TLSConf, p), timeout: o.Timeout}
		out[KindEmail] = &retrying{send: em.attempt, o: o}
	}
	for _, pr := range []struct {
		kind string
		p    *Provider
		sec  string
	}{{KindSMS, c.SMS, SecretSMSToken}, {KindVoice, c.Voice, SecretVoiceToken}} {
		if pr.p == nil {
			continue
		}
		p, err := pool(pr.kind, pr.p.TLSConf)
		if err != nil {
			return nil, err
		}
		s := newProviderSender(pr.kind, *pr.p, pr.sec, dir, p)
		out[pr.kind] = &retrying{send: s.attempt, o: o}
	}
	if w := c.Webhook; w != nil {
		p, err := pool(KindWebhook, w.TLSConf)
		if err != nil {
			return nil, err
		}
		s := newWebhookSender(*w, dir, p)
		out[KindWebhook] = &retrying{send: s.attempt, o: o}
	}
	return out, nil
}

func loadPool(key, file string) (*x509.CertPool, error) {
	if file == "" {
		return nil, nil // system roots
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	p := x509.NewCertPool()
	if !p.AppendCertsFromPEM(b) {
		return nil, fmt.Errorf("%s: %s holds no PEM certificate", key, file)
	}
	return p, nil
}

// tlsConfig is every channel's TLS: verified against pool (or the system
// roots when nil) unless insecure, never below TLS 1.2.
func tlsConfig(host string, t TLS, pool *x509.CertPool) *tls.Config {
	return &tls.Config{
		ServerName:         host,
		RootCAs:            pool,
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: t.Insecure, //nolint:gosec // only when the manifest says insecure: true
	}
}

// retrying runs one attempt at a time under its own timeout and tries
// again on transient failures only. It always returns a *escalate.SendError.
type retrying struct {
	send func(ctx context.Context, m escalate.Message) error
	o    Options
}

func (r *retrying) Send(ctx context.Context, m escalate.Message) error {
	wait := r.o.Backoff
	for attempt := 0; ; attempt++ {
		actx, cancel := context.WithTimeout(ctx, r.o.Timeout)
		err := r.send(actx, m)
		cancel()
		if err == nil {
			return nil
		}
		se := toSendError(ctx, err)
		if attempt >= r.o.Retries || !transient(se.Class) || ctx.Err() != nil {
			return se
		}
		select {
		case <-ctx.Done():
			return se
		case <-time.After(wait):
		}
		wait *= 2
	}
}

func transient(class string) bool {
	switch class {
	case escalate.FailTimeout, escalate.FailUnreachable, escalate.FailProvider:
		return true
	}
	return false
}

// toSendError reduces any error to a SendError: its class from its type,
// never its text (a url.Error carries the URL, an SMTP error the address).
func toSendError(ctx context.Context, err error) *escalate.SendError {
	var se *escalate.SendError
	if errors.As(err, &se) {
		return &escalate.SendError{Class: se.Class, Code: se.Code, Proto: se.Proto}
	}
	return escalate.Failed(classify(ctx, err))
}

// classify names the class of a transport error from its type.
func classify(ctx context.Context, err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded)):
		return escalate.FailTimeout
	case errors.As(err, &ne) && ne.Timeout():
		return escalate.FailTimeout
	case isTLSError(err):
		return escalate.FailTLS
	case errors.Is(err, context.Canceled):
		return escalate.FailUnknown
	}
	var oe *net.OpError
	if errors.As(err, &oe) {
		return escalate.FailUnreachable
	}
	return escalate.FailUnknown
}

func isTLSError(err error) bool {
	var cv *tls.CertificateVerificationError
	var ua x509.UnknownAuthorityError
	var ci x509.CertificateInvalidError
	var he x509.HostnameError
	var rh tls.RecordHeaderError
	var ae tls.AlertError
	return errors.As(err, &cv) || errors.As(err, &ua) || errors.As(err, &ci) ||
		errors.As(err, &he) || errors.As(err, &rh) || errors.As(err, &ae)
}

// address looks up a principal's address for kind.
func address(dir Directory, to, kind string) (string, error) {
	a, err := dir.Address(to, kind)
	if err != nil {
		return "", escalate.Failed(escalate.FailConfig)
	}
	if a == "" {
		return "", escalate.Failed(escalate.FailNoContact)
	}
	return a, nil
}

// secret reads a required credential.
func secret(dir Directory, key string) (string, error) {
	s, err := dir.Secret(key)
	if err != nil || s == "" {
		return "", escalate.Failed(escalate.FailConfig)
	}
	return s, nil
}
