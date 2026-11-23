//go:build !js

package camera

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// MaxSnapshotBytes bounds a still: a camera that sends more is refused, not
// buffered.
const MaxSnapshotBytes = 4 << 20

// Fetcher gets a camera's still and relays its stream. It reaches only the
// URLs of the rows it was built with (a request names a camera id, never a
// URL), and it holds the camera's credentials, which no request or response
// carries.
type Fetcher struct {
	// Secret returns a named secret of the secrets file ("user:password").
	Secret func(name string) (string, bool)
	// Client is the HTTP client; nil is one with a short connect timeout, a
	// bound on waiting for the camera's headers, and no redirect to another
	// host or scheme.
	Client *http.Client

	learned sync.Map // camera id and host:port -> "basic" or "digest": what that service asked for
	onvif   sync.Map // camera id -> *onvifEntry: what an ONVIF device said
	once    sync.Once
	made    *http.Client
}

// Bounds on a camera that does not answer or stops sending: a place is never
// held by one.
const (
	dialTimeout   = 5 * time.Second
	headerTimeout = 10 * time.Second
	_streamIdle   = 15 * time.Second
)

// StreamIdle is how long a relayed stream may send nothing before it ends (a
// variable so a test need not wait for it).
var StreamIdle = _streamIdle

// client is the HTTP client, made once: a transport per request would strand a
// keep-alive connection (and its goroutines) at the camera for every still.
func (f *Fetcher) client() *http.Client {
	if f.Client != nil {
		return f.Client
	}
	f.once.Do(func() { f.made = f.newClient() })
	return f.made
}

func (f *Fetcher) newClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: dialTimeout}).DialContext,
			ResponseHeaderTimeout: headerTimeout,
			MaxIdleConnsPerHost:   2,
			IdleConnTimeout:       30 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			// the credentials ride the redirect, so it stays on the same host, port
			// and scheme (https to http would send them in the clear)
			if req.URL.Host != via[0].URL.Host || req.URL.Scheme != via[0].URL.Scheme {
				return errors.New("a camera's URL redirected to another host")
			}
			return nil
		},
	}
}

// ErrNoSnapshot says the camera has no still the controller can get.
var ErrNoSnapshot = errors.New("this camera has no snapshot source")

// Snapshot returns the camera's current JPEG.
func (f *Fetcher) Snapshot(ctx context.Context, c Camera) ([]byte, error) {
	if c.Kind == KindONVIF {
		var err error
		if c, err = f.Resolve(ctx, c); err != nil {
			return nil, err
		}
	}
	switch c.Kind {
	case KindVirtual:
		return VirtualFrame(c.Label, nil, time.Now()), nil
	}
	u := c.SnapshotURL
	if u == "" && c.Kind == KindSnapshot {
		u = c.URL
	}
	if u == "" {
		if c.Kind == KindMJPEG {
			return f.frameFromStream(ctx, c)
		}
		return nil, ErrNoSnapshot
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := f.get(ctx, c, u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxSnapshotBytes+1))
	if err != nil {
		return nil, readErr(err)
	}
	if len(b) > MaxSnapshotBytes {
		return nil, fmt.Errorf("the camera's snapshot is larger than %d bytes", MaxSnapshotBytes)
	}
	if !isJPEG(b) {
		return nil, errors.New("the camera's snapshot URL did not return a JPEG")
	}
	return b, nil
}

func isJPEG(b []byte) bool { return len(b) > 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF }

// get fetches u with the camera's credentials: basic, or digest if the camera
// asks for it. The credentials are never put in the URL or in an error.
func (f *Fetcher) get(ctx context.Context, c Camera, u string) (*http.Response, error) {
	return f.request(ctx, c, http.MethodGet, u, nil, "")
}

// request is get for any method: a body (resent whole if the camera asks for
// credentials first) and its content type ride along.
func (f *Fetcher) request(ctx context.Context, c Camera, method, u string, body []byte, ctype string) (*http.Response, error) {
	user, pass, err := f.credentials(c)
	if err != nil {
		return nil, err
	}
	do := func(authz string) (*http.Response, error) {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, rd)
		if err != nil {
			return nil, scrub(err, u)
		}
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		resp, err := f.client().Do(req)
		if err != nil {
			return nil, scrub(err, u)
		}
		return resp, nil
	}
	// No credential goes out before the camera asks for one, and then only in
	// the scheme it asked for: a digest camera is never sent the password in
	// basic. A camera that asked for basic once is sent it straight away after.
	first := ""
	if user != "" {
		if v, _ := f.learned.Load(learnKey(c.ID, u)); v == "basic" {
			first = basic(user, pass)
		}
	}
	resp, err := do(first)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized && user != "" {
		ch := resp.Header.Values("Www-Authenticate")
		resp.Body.Close()
		var authz string
		dig, basicOK := pickChallenge(ch)
		learned, _ := f.learned.Load(learnKey(c.ID, u))
		switch {
		case dig != "":
			d, err := digest(dig, user, pass, method, requestURI(u))
			if err != nil {
				return nil, err
			}
			authz = d
			f.learned.Store(learnKey(c.ID, u), "digest")
		case learned == "digest":
			// a camera that asked for digest is never sent the password in basic,
			// whoever answers in its place
			return nil, errors.New("the camera asked for digest before and now asks for basic: not sending the password")
		case basicOK:
			authz = basic(user, pass)
			f.learned.Store(learnKey(c.ID, u), "basic")
		}
		if authz != "" {
			if resp, err = do(authz); err != nil {
				return nil, err
			}
		} else {
			return nil, errors.New("the camera refused the controller's credentials")
		}
	}
	// a SOAP fault travels in a 500 (or a 400): the caller reads its reason
	if (resp.StatusCode == http.StatusInternalServerError || resp.StatusCode == http.StatusBadRequest) && strings.HasPrefix(ctype, "application/soap+xml") {
		return resp, nil
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, errors.New("the camera refused the controller's credentials")
		}
		return nil, fmt.Errorf("the camera answered %d", resp.StatusCode)
	}
	return resp, nil
}

// scrub says why a request failed without the address or the URL: they name a
// camera's place on the plant network, which is not for an operator's browser
// (a transport error carries both).
func scrub(err error, _ string) error {
	var ne net.Error
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return errors.New("the camera did not answer: connection refused")
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return errors.New("the camera did not answer: timed out")
	case errors.Is(err, context.Canceled):
		return context.Canceled
	}
	var de *net.DNSError
	if errors.As(err, &de) {
		return errors.New("the camera did not answer: its host name does not resolve")
	}
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil && strings.Contains(ue.Err.Error(), "another host") {
		return errors.New("the camera's URL redirected to another host")
	}
	return errors.New("the camera did not answer")
}

// learnKey is what a service of a camera asked for is remembered under: a device
// has several (its SOAP service, its snapshot port) and each speaks for itself.
func learnKey(id, u string) string {
	pu, err := url.Parse(u)
	if err != nil {
		return id
	}
	return id + "|" + pu.Scheme + "://" + pu.Host
}

func basic(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

func requestURI(u string) string {
	pu, err := url.Parse(u)
	if err != nil {
		return "/"
	}
	return pu.RequestURI()
}

// readErr says a picture could not be read, without the camera's address (a
// transport error carries it).
func readErr(err error) error {
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return errors.New("the camera stopped sending")
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	return errors.New("the camera's picture could not be read")
}

// digest answers an RFC 7616 digest challenge with MD5 (what cameras speak).
func digest(challenge, user, pass, method, uri string) (string, error) {
	p := map[string]string{}
	for _, part := range splitParams(challenge[len("digest "):]) {
		k, v, ok := strings.Cut(part, "=")
		if ok {
			p[strings.ToLower(strings.TrimSpace(k))] = strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	// what the camera says is echoed back in the Authorization header: no control
	// character or quote may go with it
	for _, k := range []string{"realm", "nonce", "opaque"} {
		for _, r := range p[k] {
			if r < ' ' || r == 0x7f || r == '"' || r == '\\' {
				return "", errors.New("the camera's authentication challenge is malformed")
			}
		}
	}
	if alg := strings.ToLower(p["algorithm"]); alg != "" && alg != "md5" {
		return "", errors.New("the camera asks for a digest algorithm the controller does not speak (MD5 only)")
	}
	h := func(s string) string { x := md5.Sum([]byte(s)); return hex.EncodeToString(x[:]) }
	ha1 := h(user + ":" + p["realm"] + ":" + pass)
	ha2 := h(method + ":" + uri)
	var nb [8]byte
	rand.Read(nb[:])
	cnonce := hex.EncodeToString(nb[:])
	var resp, extra string
	if strings.Contains(p["qop"], "auth") {
		nc := "00000001"
		resp = h(ha1 + ":" + p["nonce"] + ":" + nc + ":" + cnonce + ":auth:" + ha2)
		extra = fmt.Sprintf(`, qop=auth, nc=%s, cnonce="%s"`, nc, cnonce)
	} else {
		resp = h(ha1 + ":" + p["nonce"] + ":" + ha2)
	}
	out := fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s"%s`, user, p["realm"], p["nonce"], uri, resp, extra)
	if o := p["opaque"]; o != "" {
		out += fmt.Sprintf(`, opaque="%s"`, o)
	}
	return out, nil
}

func splitParams(s string) []string {
	var out []string
	var cur strings.Builder
	q := false
	for _, r := range s {
		switch {
		case r == '"':
			q = !q
			cur.WriteRune(r)
		case r == ',' && !q:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// Meter counts the bytes of a live relay, so the page can show what a stream
// costs a slow link.
type Meter struct {
	bytes atomic.Int64
	since atomic.Int64 // unix nanoseconds of the first byte
	last  atomic.Int64
}

// Add counts n bytes now.
func (m *Meter) Add(n int) {
	now := time.Now().UnixNano()
	m.since.CompareAndSwap(0, now)
	m.last.Store(now)
	m.bytes.Add(int64(n))
}

// BitsPerSecond is the average since the first byte, 0 before one.
func (m *Meter) BitsPerSecond() int64 {
	s, l := m.since.Load(), m.last.Load()
	if s == 0 || l <= s {
		return 0
	}
	return m.bytes.Load() * 8 * int64(time.Second) / (l - s)
}

// Reset starts the count again.
func (m *Meter) Reset() { m.bytes.Store(0); m.since.Store(0); m.last.Store(0) }

// digestResponse is the response value of a digest answer, exposed so the
// test can recompute what a camera would.
func digestResponse(user, realm, pass, method, uri, nonce, nc, cnonce string) string {
	h := func(s string) string { x := md5.Sum([]byte(s)); return hex.EncodeToString(x[:]) }
	ha1 := h(user + ":" + realm + ":" + pass)
	ha2 := h(method + ":" + uri)
	return h(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":auth:" + ha2)
}

// credentials reads the camera's user name and password from the secrets file
// ("", "" when it needs none). The credentials are never in an error.
func (f *Fetcher) credentials(c Camera) (user, pass string, err error) {
	if !c.Credentials {
		return "", "", nil
	}
	key := SecretKey(c.ID)
	if f.Secret == nil {
		return "", "", errors.New("this camera needs credentials and the controller has no secrets file")
	}
	s, ok := f.Secret(key)
	if !ok {
		return "", "", fmt.Errorf("secret %q is not in the secrets file: naut alarms secrets set %s", key, key)
	}
	if user, pass, err = SplitCredentials(s); err != nil {
		return "", "", fmt.Errorf("secret %q: %w", key, err)
	}
	return user, pass, nil
}

// asciiLower lower-cases ASCII letters and leaves every other byte alone, so an
// index into the result is an index into the original (strings.ToLower rewrites
// each invalid UTF-8 byte as three, which moves every index after it).
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// pickChallenge reads a camera's WWW-Authenticate headers: the digest challenge
// (from its "Digest " on, wherever it is in a header that offers several), and
// whether basic is offered. Digest is preferred by the callers.
func pickChallenge(headers []string) (digestChallenge string, basicOffered bool) {
	for _, h := range headers {
		l := asciiLower(h)
		if digestChallenge == "" {
			if i := strings.Index(l, "digest "); i >= 0 {
				digestChallenge = h[i:]
			}
		}
		if strings.HasPrefix(strings.TrimSpace(l), "basic") {
			basicOffered = true
		}
	}
	return
}
