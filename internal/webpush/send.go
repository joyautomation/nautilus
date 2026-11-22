package webpush

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// SendError is a push service's refusal, or the failure to reach it. It
// never carries the endpoint: a *url.Error would, so its text is dropped.
type SendError struct {
	Status int  // the HTTP status; 0 when the service was not reached
	Gone   bool // 404 or 410: the subscription is over, prune it
	Why    string
}

func (e *SendError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("push service answered %d (%s)", e.Status, e.Why)
	}
	return "push service not reached (" + e.Why + ")"
}

// Options are a message's delivery terms (RFC 8030).
type Options struct {
	TTL     time.Duration // how long the service may hold it; default 1 h
	Urgency string        // very-low, low, normal, high; default high
	Topic   string        // replaces a queued message of the same topic (an alarm id)
}

// Sender POSTs encrypted messages, identified by Key, to endpoints Policy
// allows.
type Sender struct {
	Key     *Key
	Subject string // mailto: or https: URL the site owns; never defaulted
	Policy  Policy
	Client  *http.Client // nil: one that follows no redirect and dials only what Policy allows
	TLS     *tls.Config  // for the default client (a test's CA); nil: the system roots
	Now     func() time.Time

	once sync.Once
	cli  *http.Client
}

func (s *Sender) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	s.once.Do(func() { s.cli = newClient(s.Policy, s.TLS) })
	return s.cli
}

// publicIP is an address a push service can have: not loopback, private,
// link-local, multicast, unspecified or carrier-grade NAT.
func publicIP(ip net.IP) bool {
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && ip4[1]&0xc0 == 64 {
		return false // 100.64.0.0/10
	}
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsMulticast() && !ip.IsUnspecified()
}

func newClient(p Policy, tc *tls.Config) *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second}
	return &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:   tc,
			Proxy:             nil, // no ambient proxy: the endpoint is dialled as policy checked it
			DisableKeepAlives: true,
			// DialContext checks what the name resolved to, at the moment
			// of the connection: a host that answered a public address
			// when it subscribed cannot be turned on the controller's LAN.
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, errors.New("bad address")
				}
				ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
				if err != nil {
					return nil, errors.New("name not resolved")
				}
				listed := p.allows(host)
				tried := false
				var last error
				for _, ia := range ips {
					if !listed && !publicIP(ia.IP) {
						continue
					}
					tried = true
					c, err := d.DialContext(ctx, network, net.JoinHostPort(ia.IP.String(), port))
					if err == nil {
						return c, nil
					}
					last = err
				}
				if !tried {
					return nil, errors.New("refused: the host resolves only to addresses a push service cannot have (name it in push.allow-hosts to allow it)")
				}
				return nil, last
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Send encrypts payload to sub and POSTs it. nil is a 201 from the push
// service; anything else is a *SendError.
func (s *Sender) Send(ctx context.Context, sub Subscription, payload []byte, o Options) error {
	if _, err := s.Policy.CheckEndpoint(sub.Endpoint); err != nil {
		return &SendError{Why: "endpoint refused by policy"}
	}
	pk, err := b64.DecodeString(sub.P256dh)
	if err != nil {
		return &SendError{Why: "bad keys"}
	}
	au, err := b64.DecodeString(sub.Auth)
	if err != nil {
		return &SendError{Why: "bad keys"}
	}
	body, err := Encrypt(payload, pk, au)
	if err != nil {
		return &SendError{Why: "not encrypted"}
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	ttl := o.TTL
	if ttl <= 0 {
		ttl = time.Hour
	}
	auth, err := s.Key.Authorization(sub.Endpoint, s.Subject, now().Add(12*time.Hour))
	if err != nil {
		return &SendError{Why: "not signed"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return &SendError{Why: "bad endpoint"}
	}
	h := req.Header
	h.Set("Authorization", auth)
	h.Set("Content-Encoding", "aes128gcm")
	h.Set("Content-Type", "application/octet-stream")
	h.Set("TTL", strconv.Itoa(int(ttl/time.Second)))
	urg := o.Urgency
	if urg == "" {
		urg = "high"
	}
	h.Set("Urgency", urg)
	if o.Topic != "" {
		h.Set("Topic", safeTopic(o.Topic))
	}
	resp, err := s.client().Do(req)
	if err != nil {
		var ue *url.Error
		why := "connection failed"
		if errors.As(err, &ue) && errors.Is(ue.Err, context.DeadlineExceeded) {
			why = "timed out"
		} else if ctx.Err() != nil {
			why = "cancelled"
		}
		return &SendError{Why: why}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return &SendError{Status: resp.StatusCode, Gone: resp.StatusCode == 404 || resp.StatusCode == 410, Why: http.StatusText(resp.StatusCode)}
}

// safeTopic is a Topic header value: at most 32 characters of the URL-safe
// base64 alphabet (RFC 8030 §5.4); anything else is hashed into it.
func safeTopic(t string) string {
	ok := len(t) <= 32 && len(t) > 0
	for _, c := range t {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			ok = false
		}
	}
	if ok {
		return t
	}
	return rowID(t) + "00"
}
