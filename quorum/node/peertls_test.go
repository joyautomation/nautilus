package node

import (
	"bytes"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/peertls"
	"github.com/joyautomation/nautilus/quorum"
)

// waitLink waits for the last EventPeerTLS or EventPeerIdentity event of
// kind about voter, as seen by the events so far, to contain want ("" =
// cleared).
func (c *cluster) waitLink(kind EventKind, voter quorum.NodeID, want string) {
	c.t.Helper()
	c.waitFor(string(kind)+" about "+string(voter)+" = "+want, leases(20), func() bool {
		c.evMu.Lock()
		defer c.evMu.Unlock()
		last, seen := "", false
		for _, ev := range c.evs {
			if ev.Kind == kind && ev.Voter == voter {
				last, seen = ev.Detail, true
			}
		}
		if want == "" {
			return seen && last == ""
		}
		return seen && strings.Contains(last, want)
	})
}

// Peer traffic is TLS (#710): neither the peer token nor the vote
// protocol's paths cross the wire in the clear. The same run with peer TLS
// off shows both, so the tap is seeing the traffic.
func TestLoopbackPeerTrafficIsEncrypted(t *testing.T) {
	for _, plain := range []bool{false, true} {
		t.Run(map[bool]string{false: "tls", true: "plain"}[plain], func(t *testing.T) { sniff(t, plain) })
	}
}

func sniff(t *testing.T, plain bool) {
	{
		c := newCluster(t)
		for _, id := range []quorum.NodeID{"a", "b", WitnessID} {
			c.bytes[id] = &tap{}
			c.plain[id] = plain
		}
		c.startAll()
		c.commission()
		who := c.settledLeader(leases(20))
		c.steady(who, 4)
		for id, tp := range c.bytes {
			seen := tp.bytes()
			if len(seen) == 0 {
				t.Fatalf("the tap on %s saw nothing", id)
			}
			token := bytes.Contains(seen, []byte(testToken))
			path := bytes.Contains(seen, []byte(Prefix))
			switch {
			case !plain && (token || path):
				t.Errorf("peer TLS on: %s's wire shows the peer token (%v) or the vote paths (%v)", id, token, path)
			case plain && !(token && path):
				t.Errorf("peer TLS off: %s's wire should show the token and the paths (the tap's control): %v %v", id, token, path)
			}
		}
	}
}

// A witness that speaks plain HTTP while the pair speaks TLS is named on
// both controllers, the pair runs on with its two votes, and the report
// clears once the witness speaks TLS again.
func TestLoopbackPlainWitnessIsNamed(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	who := c.settledLeader(leases(20))
	c.stopWitness()
	c.mu.Lock()
	c.plain[WitnessID] = true
	c.mu.Unlock()
	c.startWitness()
	c.waitLink(EventPeerTLS, WitnessID, "the witness answers plain HTTP")
	// It never tells the operator to turn peer TLS off: what answers plain
	// HTTP may be something else (#1230 review).
	c.waitLink(EventPeerTLS, WitnessID, "never turn it off here")
	c.steady(who, 4)
	c.stopWitness()
	c.mu.Lock()
	c.plain[WitnessID] = false
	c.mu.Unlock()
	c.startWitness()
	c.waitLink(EventPeerTLS, WitnessID, "")
}

// A controller with peer TLS off beside a peer and a witness with it on:
// it names both (they speak TLS, it doesn't), and its peer names it.
func TestLoopbackPlainControllerIsNamedBothWays(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	c.settledLeader(leases(20))
	c.kill("b")
	c.mu.Lock()
	c.plain["b"] = true
	c.mu.Unlock()
	c.start("b")
	c.waitLink(EventPeerTLS, "a", "controller a speaks TLS on its peer port and this controller plain HTTP")
	c.waitLink(EventPeerTLS, WitnessID, "the witness speaks TLS")
	c.waitLink(EventPeerTLS, "b", "controller b answers plain HTTP")
	if c.waitLeader(leases(20)) != "a" {
		t.Fatal("a and the witness should elect a")
	}
	if s := c.status("b"); s.Leading || s.PeerAge != -1 || s.WitnessAge != -1 {
		t.Fatalf("the plain controller was answered: %+v", s)
	}
}

// Something else at the witness's address (a relay in the middle, another
// pair's witness): its key is not the pair's, so no controller sends it a
// request, let alone the peer token, nothing it could answer counts, and
// both controllers name it. The pair runs on its two controllers.
func TestLoopbackImpostorAtTheWitnessAddress(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	who := c.settledLeader(leases(20))
	c.stopWitness()
	other := newTestPeer(t, strings.Repeat("another pair's token ", 3), "", WitnessID, t.TempDir())
	var asked atomic.Int64
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tp := &tap{}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		w.WriteHeader(http.StatusOK) // a forged grant would go here
	})}
	// It takes any client, as an impostor would: the controllers' own
	// check is what keeps them away.
	srvTLS := other.Server()
	srvTLS.ClientAuth, srvTLS.VerifyConnection = tls.NoClientCert, nil
	go srv.Serve(tls.NewListener(&tapListener{Listener: ln, tap: tp}, srvTLS))
	t.Cleanup(func() { srv.Close() })
	c.mu.Lock()
	c.real[c.addr[WitnessID]] = ln.Addr().String()
	c.mu.Unlock()
	c.waitLink(EventPeerIdentity, WitnessID, "the witness's TLS certificate is not this pair's")
	c.steady(who, 4)
	if n := asked.Load(); n != 0 {
		t.Fatalf("the impostor got %d requests", n)
	}
	if bytes.Contains(tp.bytes(), []byte(testToken)) {
		t.Fatal("the peer token reached the impostor")
	}
	if s := c.status(who); s.WitnessAge != -1 && s.WitnessAge < leases(4) {
		t.Fatalf("the impostor was heard as the witness: %+v", s)
	}
}

// faultOf tells each TLS fault apart, and finds none in an error that
// says nothing about TLS.
func TestFaultOf(t *testing.T) {
	a := newTestPeer(t, testToken, testCluster, "a", t.TempDir())
	b := newTestPeer(t, testToken, testCluster, "b", t.TempDir())
	foreign := newTestPeer(t, strings.Repeat("b", 40), testCluster, "b", t.TempDir())
	b2 := newTestPeer(t, testToken, testCluster, "b", t.TempDir()) // b's box replaced: another key
	a2 := newTestPeer(t, testToken, testCluster, "a", t.TempDir())
	serve := func(conf *tls.Config) string {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})}
		if conf != nil {
			go srv.Serve(tls.NewListener(ln, conf))
		} else {
			go srv.Serve(ln)
		}
		t.Cleanup(func() { srv.Close() })
		return ln.Addr().String()
	}
	call := func(wrap TLSWrap, addr string) error {
		c := &caller{client: newClient(nil, wrap), token: testToken, lease: loopLease, members: "a,b", tls: wrap != nil}
		_, err := c.call(t.Context(), leases(4), http.MethodGet, baseURL(addr, wrap != nil)+Prefix+"x/hello", nil)
		return err
	}
	bound := func(p *peertls.Peer) string {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		srv := &http.Server{Handler: bindPeer(p, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))}
		go srv.Serve(tls.NewListener(ln, p.Server()))
		t.Cleanup(func() { srv.Close() })
		return ln.Addr().String()
	}
	bAddr := bound(b)
	pinned := call(wrapTo(a, "b"), bAddr) // a and b pin each other
	for _, tc := range []struct {
		name string
		err  error
		want linkFault
	}{
		{"same key", pinned, faultNone},
		{"plain peer", call(wrapTo(a, "b"), serve(nil)), faultPeerPlain},
		{"TLS peer", call(nil, serve(b.Server())), faultPeerTLS},
		{"foreign token", call(wrapTo(a, "b"), serve(foreign.Server())), faultForeignKey},
		{"another key than the pinned one", call(wrapTo(a, "b"), serve(b2.Server())), faultForeignKey},
		{"key refused", call(wrapTo(a2, "b"), bAddr), faultKeyRefused},
		{"unrelated", errors.New("connection refused"), faultNone},
		// With peer TLS on, a TLS listener's own 400 text from a handler is
		// just an answer: only a plain call can meet the real one.
		{"TLS answer quoting the 400", call(wrapTo(a, "b"), serveText(t, b.Server(), http.StatusBadRequest, "Client sent an HTTP request to an HTTPS server.")), faultNone},
	} {
		if got := faultOf(tc.err); got != tc.want {
			t.Errorf("%s: faultOf(%v) = %d, want %d", tc.name, tc.err, got, tc.want)
		}
	}
}

// TestMeasurePeerTLS measures what peer TLS costs on loopback (#710 PR 1):
// a vote round on a fresh connection (the handshake included) and on a
// kept one, and the takeover time after the leader is killed, TLS against
// plain. Skipped unless QUORUM_MEASURE_PEER_TLS=<takeovers per mode>.
// Loopback says nothing about an ARM controller's handshake cost.
func TestMeasurePeerTLS(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("QUORUM_MEASURE_PEER_TLS"))
	if n <= 0 {
		t.Skip("set QUORUM_MEASURE_PEER_TLS=<takeovers per mode>")
	}
	pct := func(d []time.Duration, p float64) time.Duration {
		slices.Sort(d)
		return d[min(len(d)-1, int(float64(len(d))*p))]
	}
	for _, plain := range []bool{true, false} {
		mode := map[bool]string{true: "plain", false: "tls"}[plain]
		// Vote rounds against a witness.
		w, err := NewWitness(WitnessOptions{Dir: t.TempDir(), Token: testToken, Lease: loopLease})
		if err != nil {
			t.Fatal(err)
		}
		h, _ := Guard(testToken, w.Handler())
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		var conf TLSWrap
		served := ln
		if !plain {
			wp := newTestPeer(t, testToken, "", WitnessID, t.TempDir())
			p := newTestPeer(t, testToken, testCluster, "a", t.TempDir())
			served, conf, h = tls.NewListener(ln, wp.Server()), wrapTo(p, WitnessID), bindPeer(wp, h)
		}
		srv := &http.Server{Handler: h}
		go srv.Serve(served)
		url := baseURL(ln.Addr().String(), !plain) + Prefix + testCluster + "/vote"
		body, _ := quorum.Request{From: "a", Nonce: 3, ID: 1}.MarshalBinary()
		round := func(c *caller) time.Duration {
			start := time.Now()
			_, _ = c.call(t.Context(), time.Second, http.MethodPost, url, body)
			return time.Since(start)
		}
		var cold, warm []time.Duration
		kept := &caller{client: newClient(nil, conf), token: testToken, lease: loopLease, members: "a,b"}
		round(kept)
		for range 300 {
			c := &caller{client: newClient(nil, conf), token: testToken, lease: loopLease, members: "a,b"}
			cold = append(cold, round(c))
			c.client.CloseIdleConnections()
			warm = append(warm, round(kept))
		}
		srv.Close()
		w.Close()
		t.Logf("%s: vote round, fresh connection p50 %v p99 %v; kept connection p50 %v p99 %v",
			mode, pct(cold, .5), pct(cold, .99), pct(warm, .5), pct(warm, .99))
		// Takeovers.
		var takes []time.Duration
		for range n {
			takes = append(takes, measureTakeover(t, plain))
		}
		t.Logf("%s: takeover after a kill (lease %v), %d runs: p50 %v p99 %v max %v",
			mode, loopLease, n, pct(takes, .5), pct(takes, .99), pct(takes, 1))
	}
}

// measureTakeover runs one pair to a settled leader, kills it, and times
// the other's lead.
func measureTakeover(t *testing.T, plain bool) time.Duration {
	var d time.Duration
	t.Run("takeover", func(t *testing.T) {
		c := newClusterHere(t)
		for _, id := range []quorum.NodeID{"a", "b", WitnessID} {
			c.plain[id] = plain
		}
		c.startAll()
		c.commission()
		who := c.settledLeader(leases(20))
		c.steady(who, 2)
		other := c.other(who)
		start := time.Now()
		c.kill(who)
		c.waitFor("the takeover", leases(20), func() bool { return c.elector(other).IsLeader() })
		d = time.Since(start)
	})
	return d
}

// A peer that accepts and never speaks (a hung process whose kernel still
// accepts) holds no connection past the handshake bound: a call's deadline
// ends the call, and tlsHandshakeTimeout ends the transport's own dial and
// handshake, which would otherwise outlive it, one per call (#1230 review).
func TestStalledHandshakesAreClosed(t *testing.T) {
	old := tlsHandshakeTimeout
	tlsHandshakeTimeout = 300 * time.Millisecond
	t.Cleanup(func() { tlsHandshakeTimeout = old })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var open atomic.Int64
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			open.Add(1)
			go func() {
				defer open.Add(-1)
				defer c.Close()
				_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
				_, _ = io.Copy(io.Discard, c) // until the client closes it
			}()
		}
	}()
	p := newTestPeer(t, testToken, testCluster, "a", t.TempDir())
	c := &caller{client: newClient(nil, wrapTo(p, "b")), token: testToken, lease: loopLease, members: "a,b"}
	for range 20 {
		_, err := c.call(t.Context(), 50*time.Millisecond, http.MethodPost, baseURL(ln.Addr().String(), true)+Prefix+testCluster+"/vote", []byte{1})
		if err == nil {
			t.Fatal("a call to a silent peer succeeded")
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for open.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := open.Load(); n > 0 {
		t.Fatalf("%d connections to a silent peer still open well after the handshake bound", n)
	}
}

// serveText serves one status and body over TLS conf, at an address it
// returns.
func serveText(t *testing.T, conf *tls.Config, code int, body string) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, body, code) })}
	go srv.Serve(tls.NewListener(ln, conf))
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String()
}

// A peer-TLS or identity change whose event is dropped (the handler
// behind) is not recorded, so the next call raises it again: a raise or a
// clear is never lost for good (#1230 review).
func TestLinkStateMovesOnlyWithItsEvent(t *testing.T) {
	e := &Elector{events: make(chan Event, 1), links: map[string]string{}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	e.events <- Event{} // the handler is behind
	e.linkState(EventPeerTLS, WitnessID, "plain")
	if len(e.links) != 0 {
		t.Fatalf("a change whose event was dropped was recorded: %v", e.links)
	}
	<-e.events
	e.linkState(EventPeerTLS, WitnessID, "plain")
	if ev := <-e.events; ev.Kind != EventPeerTLS || ev.Voter != WitnessID || ev.Detail != "plain" {
		t.Fatalf("the retried raise: %+v", ev)
	}
	e.events <- Event{}
	e.linkState(EventPeerTLS, WitnessID, "")
	if e.links[string(EventPeerTLS)+"/"+string(WitnessID)] != "plain" {
		t.Fatal("a clear whose event was dropped was recorded")
	}
	<-e.events
	e.linkState(EventPeerTLS, WitnessID, "")
	if ev := <-e.events; ev.Detail != "" || len(e.links) != 0 {
		t.Fatalf("the retried clear: %+v, links %v", ev, e.links)
	}
}
