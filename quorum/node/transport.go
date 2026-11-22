package node

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// TokenHeader carries the peer token on every peer request: the header
// oplog's replication already uses, so one token and one check serve the
// whole peer listener.
const TokenHeader = "X-Nautilus-Cluster"

// LeaseHeader carries the sender's lease, in nanoseconds, on every vote.
const LeaseHeader = "X-Nautilus-Lease"

// MembersHeader carries the pair's two controller names, "a,b" sorted, on
// every request a controller sends. The witness binds a cluster to them at
// its first vote and answers no other sender for it.
const MembersHeader = "X-Nautilus-Members"

// Prefix is where the vote protocol lives on the peer listener:
// Prefix + cluster + "/vote", "/adopt" and "/hello".
const Prefix = "/quorum/v1/"

// maxFrame bounds a request body: a frame is well under 200 bytes.
const maxFrame = 4 << 10

// MaxTermJump bounds how far one request may lift a voter's promise: a
// request for a term more than this above what the voter has promised (or
// its floor) is refused and logged. A token holder (or a bug) can't push a
// pair's terms to the top of the range and jam it for good (#1021 review):
// that now takes about 16 million forced jumps. It is wide enough that an
// isolated controller campaigning every few seconds for decades still
// rejoins (2^20 was crossed in about 50 days). A fresh voter (nothing
// promised) takes any first term; that, like the cap itself, is liveness
// only. An operator's re-commission floor stays within half of it
// (Elector.Recommission).
const MaxTermJump uint64 = 1 << 40

// RefusedHeader says why a voter refused a vote with 409: "lease" (the
// stated lease is longer than the voter's) or "term-jump" (MaxTermJump).
const RefusedHeader = "X-Nautilus-Refused"

var clusterID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ValidCluster reports whether id is a cluster ID: 32 lowercase hex
// digits (NewClusterID). It keys the witness's state for the pair, and the
// paths votes go to.
func ValidCluster(id string) bool { return clusterID.MatchString(id) }

// NewClusterID draws a cluster ID: 128 random bits, drawn once when a pair
// is commissioned, so two pairs never share a witness voter, even two
// sites running one project (#1021 review).
func NewClusterID() string {
	var b [16]byte
	if _, err := crand.Read(b[:]); err != nil {
		panic("quorum: crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// members is the MembersHeader value for a pair.
func members(x, y quorum.NodeID) string {
	if y < x {
		x, y = y, x
	}
	return string(x) + "," + string(y)
}

// termJump reports whether a request for term would lift a voter that has
// promised promised (floor floor) by more than MaxTermJump.
func termJump(term, promised, floor uint64) bool {
	base := max(promised, floor)
	limit := uint64(MaxTermJump)
	if mut("term_cap_narrow") {
		limit = 1 << 20 // the first cap: an isolated controller crossed it in about 50 days
	}
	return base != 0 && term > base && term-base > limit
}

// statusError is a peer call answered with an HTTP error.
type statusError struct {
	code   int
	msg    string
	reason string // RefusedHeader
}

func (e *statusError) Error() string { return e.msg }

// LimitListener accepts at most n connections at once from l: a peer port
// serves three voters, so a flood of connections waits rather than
// exhausting the host.
func LimitListener(l net.Listener, n int) net.Listener {
	return &limitListener{Listener: l, sem: make(chan struct{}, n)}
}

type limitListener struct {
	net.Listener
	sem chan struct{}
}

func (l *limitListener) Accept() (net.Conn, error) {
	l.sem <- struct{}{}
	c, err := l.Listener.Accept()
	if err != nil {
		<-l.sem
		return nil, err
	}
	return &limitConn{Conn: c, free: func() { <-l.sem }}, nil
}

type limitConn struct {
	net.Conn
	once sync.Once
	free func()
}

func (c *limitConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.free)
	return err
}

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

// baseURL turns a peer address from the manifest (host:port, or a URL in
// tests) into the base URL calls go to: https with peer TLS on.
func baseURL(addr string, tls bool) string {
	if strings.Contains(addr, "://") {
		return strings.TrimRight(addr, "/")
	}
	if tls {
		return "https://" + addr
	}
	return "http://" + addr
}

// newClient is the HTTP client for peer calls: connections kept alive,
// a few per peer, and no overall timeout (each call has its own deadline).
// conf, when set, is peer TLS's client side (#710): the handshake rides
// the same dial, so a close is still a reset.
func newClient(dial func(ctx context.Context, network, addr string) (net.Conn, error), conf *tls.Config) *http.Client {
	if dial == nil {
		dial = (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: 15 * time.Second}).DialContext
	}
	handshake := tlsHandshakeTimeout
	if mut("peer_tls_handshake_unbounded") {
		handshake = 0
	}
	return &http.Client{Transport: &http.Transport{
		Proxy:           nil, // peers are reached directly, never through an HTTP proxy
		DialContext:     resetOnClose(dial),
		TLSClientConfig: conf,
		// A call's deadline ends the call, not the transport's own dial and
		// handshake: without a bound, a peer that accepts and never speaks
		// (a hung process) would keep one connection per call (#1230 review).
		TLSHandshakeTimeout: handshake,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   false,
	}}
}

// tlsHandshakeTimeout bounds a peer TLS handshake.
var tlsHandshakeTimeout = 2 * time.Second

// httpsAnswer is how Go's TLS listener answers a plain HTTP request (a 400
// with this body): the peer speaks TLS and this side doesn't.
const httpsAnswer = "HTTP request to an HTTPS server"

// errPeerSpeaksTLS is a plain call answered by a TLS listener.
var errPeerSpeaksTLS = errors.New("the peer port answers with TLS")

// linkFault says what a failed peer call shows about peer TLS: nothing
// (a timeout, a refused connection, an HTTP error), the other protocol at
// the peer, or a key that is not the pair's on one end.
type linkFault int

const (
	faultNone       linkFault = iota
	faultPeerPlain            // this side TLS, the peer plain HTTP
	faultPeerTLS              // this side plain HTTP, the peer TLS
	faultForeignKey           // the peer's key is not the pair's
	faultKeyRefused           // the peer refused this side's key
)

// faultOf classifies a peer call's error.
func faultOf(err error) linkFault {
	var id interface{ PeerIdentity() bool }
	var op *net.OpError
	switch {
	case err == nil:
		return faultNone
	case errors.Is(err, http.ErrSchemeMismatch):
		return faultPeerPlain
	case errors.Is(err, errPeerSpeaksTLS):
		return faultPeerTLS
	case errors.As(err, &id) && id.PeerIdentity():
		return faultForeignKey
	case errors.As(err, &op) && op.Op == "remote error" && strings.Contains(op.Err.Error(), "certificate"):
		// A TLS 1.3 client learns that the server refused its key only
		// at its first read: an alert, "bad certificate".
		return faultKeyRefused
	}
	return faultNone
}

// resetOnClose dials with SO_LINGER 0, so every close of a peer
// connection is a reset: a process that exits (or closes a connection)
// drops the frames its kernel still holds instead of retransmitting them
// through a link outage. Such a frame of a dead boot arriving more than a
// lease late could make both voters name that boot the incumbent, and its
// restarted controller a spare (#1029 3c-2, #1133 review). The residual
// then needs that much delay inside the network itself.
func resetOnClose(dial func(ctx context.Context, network, addr string) (net.Conn, error)) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := dial(ctx, network, addr)
		if tc, ok := c.(*net.TCPConn); ok && err == nil && !mut("peer_dial_lingers") {
			_ = tc.SetLinger(0)
		}
		return c, err
	}
}

// caller makes peer calls for one sender.
type caller struct {
	client  *http.Client
	token   string
	lease   time.Duration
	members string
	// note, when set, hears every call's outcome at the transport: nil
	// for an answer (any status), else the error (noteLink).
	note func(url string, err error)
	// tls: calls go out with peer TLS. Only a plain call can meet a TLS
	// listener's 400, so only then is that answer read as one.
	tls bool
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
	req.Header.Set(MembersHeader, c.members)
	if body != nil {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		c.noted(url, err)
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxFrame))
	if err == nil && !c.tls && resp.StatusCode == http.StatusBadRequest && bytes.Contains(b, []byte(httpsAnswer)) {
		err = fmt.Errorf("%s %s: %w", method, url, errPeerSpeaksTLS)
	}
	c.noted(url, err)
	switch {
	case err != nil:
		return nil, err
	case resp.StatusCode == http.StatusNoContent:
		return nil, nil
	case resp.StatusCode != http.StatusOK:
		return nil, &statusError{code: resp.StatusCode, reason: resp.Header.Get(RefusedHeader),
			msg: fmt.Sprintf("%s %s: %s: %s", method, url, resp.Status, bytes.TrimSpace(b))}
	}
	return b, nil
}

func (c *caller) noted(url string, err error) {
	if c.note != nil {
		c.note(url, err)
	}
}

// statedLease reads a vote request's stated lease: 0 when missing or bad,
// which every voter refuses.
func statedLease(r *http.Request) time.Duration {
	n, err := strconv.ParseInt(r.Header.Get(LeaseHeader), 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return time.Duration(n)
}

// leaseFits reports whether a request's stated lease fits within max
// (quorum.Voter.MaxRequestLease): a lease longer than the promise it rests
// on would outlive it.
func leaseFits(stated, max time.Duration) bool { return stated > 0 && stated <= max }

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

// refuse answers 409 with why (RefusedHeader).
func refuse(w http.ResponseWriter, why, msg string) {
	w.Header().Set(RefusedHeader, why)
	http.Error(w, msg, http.StatusConflict)
}
