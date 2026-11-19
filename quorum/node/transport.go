package node

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// TokenHeader carries the peer token on every peer request: the header
// oplog's replication already uses, so one token and one check serve the
// whole peer listener.
const TokenHeader = "X-Nautilus-Cluster"

// LeaseHeader carries the sender's lease, in nanoseconds, on every vote.
const LeaseHeader = "X-Nautilus-Lease"

// Prefix is where the vote protocol lives on the peer listener:
// Prefix + pair + "/vote", "/adopt" and "/hello".
const Prefix = "/quorum/v1/"

// maxFrame bounds a request body: a frame is well under 200 bytes.
const maxFrame = 4 << 10

var pairName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidPair reports whether name can name a pair: it keys the witness's
// state file, so it is a plain file name.
func ValidPair(name string) bool { return pairName.MatchString(name) }

// Guard serves h on the peer listener (design §6.1): a request that does
// not carry the peer token is refused with 401 whatever its Host, so no
// Host, localhost included, stands in for the token, and a browser page
// rebound by DNS can't vote. It is never a browser surface: no CORS, no
// static files. Guard refuses to build without a token.
func Guard(token string, h http.Handler) (http.Handler, error) {
	if token == "" {
		return nil, errors.New("quorum: the peer listener needs NAUTILUS_PEER_TOKEN")
	}
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mut("peer_host_trusted") && trustedHost(r.Host) {
			h.ServeHTTP(w, r)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get(TokenHeader)), want) != 1 {
			http.Error(w, "peer token required", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}), nil
}

// trustedHost is the tag API's Host rule (localhost or an IP), which the
// peer listener must NOT apply: only the peer_host_trusted mutation uses
// it.
func trustedHost(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	return strings.EqualFold(h, "localhost") || net.ParseIP(strings.Trim(h, "[]")) != nil
}

// baseURL turns a peer address from the manifest (host:port, or a URL)
// into the base URL calls go to.
func baseURL(addr string) string {
	if strings.Contains(addr, "://") {
		return strings.TrimRight(addr, "/")
	}
	return "http://" + addr
}

// newClient is the HTTP client for peer calls: connections kept alive,
// a few per peer, and no overall timeout (each call has its own deadline).
func newClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		Proxy:               nil, // peers are reached directly, never through an HTTP proxy
		DialContext:         (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: 15 * time.Second}).DialContext,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   false,
	}}
}

// caller makes peer calls for one sender.
type caller struct {
	client *http.Client
	token  string
	lease  time.Duration
}

// call sends one request with a deadline of d and returns the body of a
// 200 answer, nil for a 204, or an error.
func (c *caller) call(ctx context.Context, d time.Duration, method, url string, body []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set(TokenHeader, c.token)
	req.Header.Set(LeaseHeader, strconv.FormatInt(int64(c.lease), 10))
	if body != nil {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxFrame))
	switch {
	case err != nil:
		return nil, err
	case resp.StatusCode == http.StatusNoContent:
		return nil, nil
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%s %s: %s: %s", method, url, resp.Status, bytes.TrimSpace(b))
	}
	return b, nil
}

// leaseOK reports whether a vote request's stated lease fits this voter's
// own: a lease longer than the promise it rests on would outlive it. A
// request with no lease stated is refused too.
func leaseOK(r *http.Request, own time.Duration) bool {
	if mut("lease_unchecked") {
		return true
	}
	n, err := strconv.ParseInt(r.Header.Get(LeaseHeader), 10, 64)
	return err == nil && n > 0 && time.Duration(n) <= own
}

// readFrame reads a request body of at most maxFrame bytes.
func readFrame(r *http.Request) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxFrame+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxFrame {
		return nil, errors.New("frame too large")
	}
	return b, nil
}

// writeFrame answers 200 with a binary frame.
func writeFrame(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}
