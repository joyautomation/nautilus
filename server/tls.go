package server

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/audit"
)

// CertSource is the tag API's TLS certificate (#708, server.tls): a
// certificate and key the operator supplies as files, re-read when either
// file changes (looked at no more than once a minute), so a renewal needs
// no restart. It remembers the leaf it replaced: a redundant pair renews
// one box a little before the other, and a standby still accepts the
// leader's certificate while it is either one (Pinned).
type CertSource struct {
	certPath, keyPath string
	now               func() time.Time

	mu        sync.Mutex
	cert      *tls.Certificate
	pin, prev [32]byte // SHA-256 of the current and the replaced leaf
	stamp     string
	checked   time.Time
	failed    string      // the last reload's error, "" when it loaded
	onReload  func(error) // told a failed reload once, and its recovery
}

// certRecheck is how often the files are looked at for a renewal.
const certRecheck = time.Minute

// NewCertSource loads cert and key (PEM). A key file anyone may read is
// refused (unix): it is what impersonates the controller.
func NewCertSource(certPath, keyPath string) (*CertSource, error) {
	c := &CertSource{certPath: certPath, keyPath: keyPath, now: time.Now}
	if err := c.load(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *CertSource) fileStamp() (string, error) {
	var parts []string
	for _, p := range []string{c.certPath, c.keyPath} {
		st, err := os.Stat(p)
		if err != nil {
			return "", err
		}
		parts = append(parts, fmt.Sprint(st.ModTime().UnixNano(), st.Size(), st.Mode()))
	}
	return strings.Join(parts, "|"), nil
}

func (c *CertSource) load() error {
	stamp, err := c.fileStamp()
	if err != nil {
		return fmt.Errorf("tls: %w", err)
	}
	if err := checkKeyMode(c.keyPath); err != nil {
		return err
	}
	pair, err := tls.LoadX509KeyPair(c.certPath, c.keyPath)
	if err != nil {
		return fmt.Errorf("tls: %w", err)
	}
	if len(pair.Certificate) == 0 {
		return errors.New("tls: the certificate file holds no certificate")
	}
	if pair.Leaf, err = x509.ParseCertificate(pair.Certificate[0]); err != nil {
		return fmt.Errorf("tls: %w", err)
	}
	pin := sha256.Sum256(pair.Certificate[0])
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cert != nil && pin != c.pin {
		c.prev = c.pin
	}
	c.cert, c.pin, c.stamp, c.checked = &pair, pin, stamp, c.now()
	return nil
}

// OnReload sets what is told when a renewal cannot be loaded (the error,
// once per failure) and when one loads again after that (nil).
func (c *CertSource) OnReload(fn func(error)) {
	c.mu.Lock()
	c.onReload = fn
	c.mu.Unlock()
}

// refresh re-reads the files when they changed, at most once a minute
// (now: at once). A renewal that cannot be loaded (half copied, a key
// that does not match) keeps the certificate already served, is told
// once (OnReload), and is tried again later.
func (c *CertSource) refresh(now bool) {
	c.mu.Lock()
	due := now || c.now().Sub(c.checked) >= certRecheck
	if due {
		c.checked = c.now()
	}
	stamp, failed := c.stamp, c.failed
	c.mu.Unlock()
	if !due {
		return
	}
	s, err := c.fileStamp()
	if err == nil && s == stamp && failed == "" {
		return
	}
	if err == nil {
		err = c.load()
	}
	c.mu.Lock()
	tell := c.onReload
	switch {
	case err != nil && err.Error() != c.failed:
		c.failed = err.Error()
	case err == nil && c.failed != "":
		c.failed = ""
	default:
		tell = nil // nothing new to say
	}
	c.mu.Unlock()
	if tell != nil {
		tell(err)
	}
}

// GetCertificate serves the current certificate (tls.Config).
func (c *CertSource) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.refresh(false)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cert, nil
}

// Config is the listener's TLS configuration: TLS 1.2 at least (1.3 when
// the client has it, Go's default preference), this certificate.
func (c *CertSource) Config() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: c.GetCertificate}
}

// Pin is the SHA-256 of the leaf served now: what a redundant pair's
// handshake compares (both boxes are meant to serve one certificate).
func (c *CertSource) Pin() [32]byte {
	c.refresh(false)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pin
}

// Pinned reports whether a peer's leaf (DER) is this controller's
// current certificate or the one it replaced. A leaf it does not know
// sends it to its own files at once, not in a minute: the leader may have
// loaded a renewal already copied here too.
func (c *CertSource) Pinned(der []byte) bool {
	sum := sha256.Sum256(der)
	known := func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return sum == c.pin || (c.prev != [32]byte{} && sum == c.prev)
	}
	c.refresh(false)
	if known() {
		return true
	}
	c.refresh(true)
	return known()
}

// errNotOurCert is the leader presenting a certificate this controller
// does not serve.
var errNotOurCert = errors.New("the leader's certificate is not this controller's (A and B must serve the same server.tls certificate)")

// leaderTransport is how a standby reaches the leader's tag API over TLS.
// The leader is addressed as peers: names it (often an IP) that the
// certificate's names need not cover, so instead of a name check the
// leader must present this controller's own leaf, current or previous
// (Pinned): a deliberate pin, not a skipped check.
func (c *CertSource) leaderTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		// The name check is replaced by VerifyConnection's pin, which
		// runs on every handshake; nothing is accepted unverified.
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 || !c.Pinned(cs.PeerCertificates[0].Raw) {
				return errNotOurCert
			}
			return nil
		},
	}
	return t
}

// ParseTrustedProxies reads server.trusted-proxies: addresses ("10.0.0.5")
// or prefixes ("10.0.0.0/24").
func ParseTrustedProxies(list []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range list {
		s = strings.TrimSpace(s)
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, fmt.Errorf("trusted-proxies: %q is not an address or a prefix", s)
		}
		out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
	}
	return out, nil
}

func (s *Server) trustedProxy(addr string) bool {
	a, err := netip.ParseAddr(hostOf(addr))
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, p := range s.proxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// secure reports whether r reached the controller over TLS: its own
// (server.tls), or a trusted proxy's that says so (X-Forwarded-Proto:
// https from an address in server.trusted-proxies). It decides Secure
// cookies and password sign-in (#708, 5b).
func (s *Server) secure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return s.trustedProxy(r.RemoteAddr) && strings.EqualFold(lastValue(r.Header.Values("X-Forwarded-Proto")), "https")
}

// lastValue is a header's right-most comma-separated value: what the
// proxy nearest the controller added (a client can put anything before
// it).
func lastValue(vs []string) string {
	all := strings.Split(strings.Join(vs, ","), ",")
	return strings.TrimSpace(all[len(all)-1])
}

// forwardHeaders sets what a standby tells the leader about a request it
// forwards: the X-Forwarded-* a client sent are dropped unless they came
// through a trusted proxy (the reverse proxy then appends this hop to
// X-Forwarded-For), and X-Forwarded-Proto says https only when the
// request reached this standby securely. The leader believes either only
// when it lists the standby in server.trusted-proxies.
func (s *Server) forwardHeaders(out *http.Request) {
	secure := s.secure(out)
	if !s.trustedProxy(out.RemoteAddr) {
		out.Header.Del("X-Forwarded-For")
		out.Header.Del("X-Forwarded-Host")
	}
	out.Header.Del("X-Forwarded-Proto")
	if secure {
		out.Header.Set("X-Forwarded-Proto", "https")
	}
}

// journalTLSReload records a renewal the controller cannot load (it keeps
// serving the certificate it has) and the first one it loads after that.
func (s *Server) journalTLSReload(err error) {
	e := audit.Entry{User: "system", Source: audit.SourceSystem, Action: "tls.reloaded", Result: audit.ResultOK}
	if err != nil {
		slog.Error("tls: a renewed certificate cannot be loaded; still serving the one loaded before", "err", err)
		e.Action, e.Result, e.Detail = "tls.reload-failed", audit.ResultError, map[string]any{"reason": err.Error()}
	}
	s.audit.record(e)
}

// clientAddr is who sent r, for the journal: the connection's address,
// or, from a trusted proxy, the right-most X-Forwarded-For address that is
// not itself a trusted proxy (a client can put anything on the left).
func (s *Server) clientAddr(r *http.Request) string {
	from := hostOf(r.RemoteAddr)
	if !s.trustedProxy(r.RemoteAddr) {
		return from
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		h := strings.TrimSpace(hops[i])
		if h == "" {
			continue
		}
		if _, err := netip.ParseAddr(h); err != nil {
			return from // a malformed hop: trust nothing past it
		}
		if !s.trustedProxy(h) {
			return h
		}
		from = h
	}
	return from
}

// journalPinMismatch records, once until a forward succeeds again, that a
// standby refused to forward to a leader serving another certificate.
func (s *Server) journalPinMismatch(addr string) {
	s.pinMu.Lock()
	already := s.pinRefuse
	s.pinRefuse = true
	s.pinMu.Unlock()
	if already {
		return
	}
	slog.Error("tls: not forwarding to the leader", "leader", addr, "err", errNotOurCert)
	s.audit.record(audit.Entry{
		User:   "system",
		Source: audit.SourceSystem,
		Action: "cluster.forward-refused",
		Target: addr,
		Detail: map[string]any{"reason": errNotOurCert.Error()},
		Result: audit.ResultError,
	})
}
