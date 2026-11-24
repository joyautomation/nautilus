package node

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"crypto/subtle"
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

// VoterLeaseHeader carries a voter's own lease, in nanoseconds, on every
// vote answer (#710). A dialer clears a lease refusal only when its lease
// fits it: during a voter's restart wait the voter still takes a longer
// lease it refuses after (quorum.Voter.MaxRequestLease), which is no
// all-clear. An answer without it (an older voter) clears as before.
const VoterLeaseHeader = "X-Nautilus-Voter-Lease"

// voterLeaseFits reports whether lease fits the voter's own lease the
// answer's headers state (true when they state none).
func voterLeaseFits(h http.Header, lease time.Duration) bool {
	if mut("lease_clear_on_wait") {
		return true
	}
	n, err := strconv.ParseInt(h.Get(VoterLeaseHeader), 10, 64)
	if err != nil || n <= 0 {
		return true
	}
	return lease <= time.Duration(n)
}

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
	// body is the answer's own text, at most maxRefusalText bytes: what a
	// refusal says, for an operator's condition (msg adds the call).
	body string
}

// maxRefusalText bounds the refusal text a voter's answer puts in a report.
const maxRefusalText = 512

// said is what the voter's answer says, or the whole error when it says
// nothing.
func (e *statusError) said() string {
	if e.body == "" {
		return e.msg
	}
	return e.body
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

// TLSWrap is peer TLS's client side (#710): it runs the handshake over
// raw, a connection dialed to addr (the host:port of a peer address as
// Options names it: DialAddr), and returns it once the other end is the
// member at that address (internal/peertls Peer.Wrap: its certificate
// signed by the token's key, naming that member, its key the pinned one).
// ctx bounds the handshake. Nil: plain HTTP.
type TLSWrap func(ctx context.Context, raw net.Conn, addr string) (net.Conn, error)

// DialAddr is the host:port a peer call to addr (a peer address from the
// manifest, or a URL in tests) dials: what a TLSWrap is handed.
func DialAddr(addr string) string { return hostOf(baseURL(addr, true)) }

// newClient is the HTTP client for peer calls: connections kept alive,
// a few per peer, and no overall timeout (each call has its own deadline).
// wrap, when set, is peer TLS's client side (#710): the handshake rides
// the same dial, so a close is still a reset, and each destination gets
// its own check (a TLS ServerName can't name the member: it is empty for
// an IP address).
func newClient(dial func(ctx context.Context, network, addr string) (net.Conn, error), wrap TLSWrap) *http.Client {
	if dial == nil {
		dial = (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: 15 * time.Second}).DialContext
	}
	tr := &http.Transport{
		Proxy:               nil, // peers are reached directly, never through an HTTP proxy
		DialContext:         resetOnClose(dial),
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   false,
	}
	if wrap != nil {
		tr.DialTLSContext = tlsDial(resetOnClose(dial), wrap)
	}
	return &http.Client{Transport: tr}
}

// tlsDial dials raw and runs wrap's handshake over it, bounded: a call's
// deadline ends the call, not the dial and handshake, so without a bound
// a peer that accepts and never speaks (a hung process) would keep one
// connection per call (#1230 review).
func tlsDial(dial func(ctx context.Context, network, addr string) (net.Conn, error), wrap TLSWrap) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		raw, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		hctx := ctx
		if !mut("peer_tls_handshake_unbounded") {
			var cancel context.CancelFunc
			hctx, cancel = context.WithTimeout(ctx, tlsHandshakeTimeout)
			defer cancel()
		}
		return wrap(hctx, raw, addr)
	}
}

// NewPeerClient is an HTTP client for a peer call of its own, outside the
// vote protocol's: the pair's state stream (#710 S1b) keeps its own
// connections, so a busy stream never delays a renewal (design §6). Same
// dial (every close a reset), same peer TLS, same handshake bound.
func NewPeerClient(dial func(ctx context.Context, network, addr string) (net.Conn, error), wrap TLSWrap) *http.Client {
	return newClient(dial, wrap)
}

// PeerURL is the base URL of a peer address (host:port, or a URL in
// tests): https with peer TLS on.
func PeerURL(addr string, tls bool) string { return baseURL(addr, tls) }

// peerKey is the request context key of the member a peer listener's
// TLS verified (WithPeer).
type peerKey struct{}

type verifiedPeer struct{ cluster, name string }

// WithPeer is r carrying the member peer TLS verified for it (cluster, ""
// for the witness, and name): what internal/peertls's Bind hands the
// handlers, through cmd/naut, which imports both.
func WithPeer(r *http.Request, cluster, name string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), peerKey{}, verifiedPeer{cluster, name}))
}

// VerifiedPeer is the member peer TLS verified for r (WithPeer).
func VerifiedPeer(r *http.Request) (cluster, name string, ok bool) {
	v, ok := r.Context().Value(peerKey{}).(verifiedPeer)
	return v.cluster, v.name, ok
}

// SenderOK reports whether a request that names sender of cluster in its
// body (or query) comes from that member: the one its TLS certificate
// names (#710 per-box keys). A request without TLS is peer TLS off (the
// loud lab opt-out): nothing to bind. With TLS and no verified member (a
// listener without Bind) it fails closed. mutation is the handler's own
// row, which skips the check.
func SenderOK(r *http.Request, cluster, sender, mutation string) bool {
	if r.TLS == nil || mut(mutation) {
		return true
	}
	c, n, ok := VerifiedPeer(r)
	return ok && c == cluster && n == sender
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
	case keyRefusal(err):
		return faultKeyRefused
	case errors.As(err, &op) && op.Op == "remote error" && strings.Contains(op.Err.Error(), "certificate"):
		// A TLS 1.3 client learns that the server refused its key only
		// at its first read: an alert, "bad certificate".
		return faultKeyRefused
	}
	return faultNone
}

// keyRefusal reports a peer listener's refusal of this side's key at a
// request (internal/peertls Bind: a 403 marked peer-key), as when a name's
// pin changed under a kept connection.
func keyRefusal(err error) bool {
	var se *statusError
	return errors.As(err, &se) && se.reason == "peer-key"
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
	b, _, err := c.callHeader(ctx, d, method, url, body)
	return b, err
}

// callHeader is call with the answer's headers (nil when none came).
func (c *caller) callHeader(ctx context.Context, d time.Duration, method, url string, body []byte) ([]byte, http.Header, error) {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return nil, nil, err
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
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxFrame))
	if err == nil && !c.tls && resp.StatusCode == http.StatusBadRequest && bytes.Contains(b, []byte(httpsAnswer)) {
		err = fmt.Errorf("%s %s: %w", method, url, errPeerSpeaksTLS)
	}
	c.noted(url, err)
	switch {
	case err != nil:
		return nil, resp.Header, err
	case resp.StatusCode == http.StatusNoContent:
		return nil, resp.Header, nil
	case resp.StatusCode != http.StatusOK:
		body := bytes.TrimSpace(b)
		said := strings.ToValidUTF8(string(body[:min(len(body), maxRefusalText)]), "")
		return nil, resp.Header, &statusError{code: resp.StatusCode, reason: resp.Header.Get(RefusedHeader),
			msg: fmt.Sprintf("%s %s: %s: %s", method, url, resp.Status, body), body: said}
	}
	return b, resp.Header, nil
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

// CloseIdle closes the elector's kept peer connections: after a peer's pin
// is dropped (#710 per-box keys), its next call meets the new key.
func (e *Elector) CloseIdle() { e.call.client.CloseIdleConnections() }
