//go:build !js

package camera

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
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
	// Client is the HTTP client; nil is one with a short connect timeout and no
	// redirect to another host.
	Client *http.Client
}

func (f *Fetcher) client() *http.Client {
	if f.Client != nil {
		return f.Client
	}
	return &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			if req.URL.Host != via[0].URL.Host {
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
		return nil, fmt.Errorf("reading the snapshot: %w", err)
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
	user, pass := "", ""
	if c.Credentials {
		key := SecretKey(c.ID)
		if f.Secret == nil {
			return nil, errors.New("this camera needs credentials and the controller has no secrets file")
		}
		s, ok := f.Secret(key)
		if !ok {
			return nil, fmt.Errorf("secret %q is not in the secrets file: naut alarms secrets set %s", key, key)
		}
		var err error
		if user, pass, err = SplitCredentials(s); err != nil {
			return nil, fmt.Errorf("secret %q: %w", key, err)
		}
	}
	do := func(authz string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, scrub(err, u)
		}
		if authz != "" {
			req.Header.Set("Authorization", authz)
		} else if user != "" {
			req.SetBasicAuth(user, pass)
		}
		resp, err := f.client().Do(req)
		if err != nil {
			return nil, scrub(err, u)
		}
		return resp, nil
	}
	resp, err := do("")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized && user != "" {
		if ch := resp.Header.Get("Www-Authenticate"); strings.HasPrefix(strings.ToLower(ch), "digest ") {
			resp.Body.Close()
			pu, _ := url.Parse(u)
			resp, err = do(digest(ch, user, pass, http.MethodGet, pu.RequestURI()))
			if err != nil {
				return nil, err
			}
		}
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

// digest answers an RFC 7616 digest challenge with MD5 (what cameras speak).
func digest(challenge, user, pass, method, uri string) string {
	p := map[string]string{}
	for _, part := range splitParams(challenge[len("digest "):]) {
		k, v, ok := strings.Cut(part, "=")
		if ok {
			p[strings.ToLower(strings.TrimSpace(k))] = strings.Trim(strings.TrimSpace(v), `"`)
		}
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
	return out
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
