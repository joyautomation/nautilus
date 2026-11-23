package node

import (
	"testing"
	"time"
)

// The hello's answer names the answerer's tag API, bound by InfoMAC like
// the rest of its info: an answer whose API was changed on the way fails,
// so whoever sits between the controllers cannot point a standby's proxy
// somewhere else (peer_api_unbound turns this red). An answer without one
// (a build before it) verifies as before.
func TestTheAnnouncedAPIIsBoundByTheMAC(t *testing.T) {
	o := testOptions(t, "a", "b")
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	const c = "challenge-0123456789"
	sent := peerInfo{Config: "c", Nonce: 7, API: "10.0.0.2:8082"}
	h := helloAnswer{Node: "b", Config: sent.Config, Nonce: "7", API: sent.API, InfoMAC: infoMAC(o.Token, c, "a", "b", sent)}
	if pi, ok := e.answerVerified(c, h); !ok || pi.API != sent.API {
		t.Fatalf("the answer as sent: verified %v, API %q", ok, pi.API)
	}
	h.API = "10.0.0.66:8082"
	if _, ok := e.answerVerified(c, h); ok {
		t.Fatal("an answer whose tag API was changed on the way still verifies")
	}
	old := peerInfo{Config: "c", Nonce: 7}
	h = helloAnswer{Node: "b", Config: old.Config, Nonce: "7", InfoMAC: infoMAC(o.Token, c, "a", "b", old)}
	if pi, ok := e.answerVerified(c, h); !ok || pi.API != "" {
		t.Fatalf("an answer without a tag API (a build before it): verified %v, API %q", ok, pi.API)
	}
}

// The peer's tag API is what its verified answer announced, and nothing
// else: never this controller's own (a standby that proxied there would
// proxy to itself: peer_api_self turns this red), and an answer without
// one names none, so the standby refuses writes rather than guess. The
// asker's part of a hello (heard, from its query, which no MAC binds)
// leaves it alone.
func TestThePeersAPIIsOnlyWhatItAnnounced(t *testing.T) {
	o := testOptions(t, "a", "b")
	o.SelfAPI = "10.0.0.1:8081"
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	peerAPI := func() string { e.mu.Lock(); defer e.mu.Unlock(); return e.peerAPI }
	if got := peerAPI(); got != "" {
		t.Fatalf("before any hello: %q", got)
	}
	e.heardAPI("10.0.0.2:8082")
	if got := peerAPI(); got != "10.0.0.2:8082" {
		t.Fatalf("announced 10.0.0.2:8082, named %q", got)
	}
	e.heard(peerInfo{Config: o.Config, Nonce: 3, API: "10.0.0.66:1"})
	if got := peerAPI(); got != "10.0.0.2:8082" {
		t.Fatalf("an asker's query moved the peer's tag API to %q", got)
	}
	e.heardAPI(o.SelfAPI)
	if got := peerAPI(); got != "" {
		t.Fatalf("the peer announced this controller's own tag API and it is named: %q", got)
	}
	e.heardAPI("10.0.0.2:8082")
	e.heardAPI("")
	if got := peerAPI(); got != "" {
		t.Fatalf("an answer without a tag API kept %q", got)
	}
	// Not a host and a port: names nothing.
	for _, bad := range []string{"10.0.0.2", ":8082", "10.0.0.2:", "not an address"} {
		e.heardAPI("10.0.0.2:8082")
		e.heardAPI(bad)
		if got := peerAPI(); got != "" {
			t.Errorf("announced %q, named %q", bad, got)
		}
	}
}

// A standby whose hello found no verified answer asks again within about a
// lease, not a whole hello interval later (#1120): a standby that started
// before its peer had no leader API address, and refused writes it could
// forward, for up to 16 leases. hello_retry_slow turns this red.
func TestAHelloWithNoAnswerIsRetriedSoon(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	who := c.settledLeader(leases(20))
	sb := c.other(who)
	c.mu.Lock()
	c.noHello[sb] = true
	c.mu.Unlock()
	c.kill(sb)
	// Its first hello, and the one the leader's first vote asks for (a
	// retry after it), are lost; the backed-off retry at about 3 s is not.
	c.start(sb)
	time.Sleep(leases(3))
	c.mu.Lock()
	c.noHello[sb] = false
	c.mu.Unlock()
	c.waitFor("the standby to learn the leader's API", leases(8), func() bool {
		s := c.status(sb)
		return s.Leader == string(who) && s.LeaderAddr == apiAddr(who)
	})
}

// A standby whose hellos have long gone unanswered, its retries backed off
// toward the interval, asks at once when its peer restarts (a new boot in
// its votes) or the link to it comes up, rather than at its next retry.
// hello_not_asked_again turns this red.
func TestAPeersRestartAsksForAHelloAtOnce(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	who := c.settledLeader(leases(20))
	sb := c.other(who)
	c.mu.Lock()
	c.noHello[sb] = true
	c.mu.Unlock()
	c.kill(sb)
	c.start(sb)
	// Retries at about 1, 3, 7 and 15 s after its start: lift the loss at
	// 8 s, so the next retry is some 7 s away.
	time.Sleep(8 * time.Second)
	if s := c.status(sb); s.ClientToken != TokenUnknown {
		t.Fatalf("the hello got through while lost: %+v", s)
	}
	c.mu.Lock()
	c.noHello[sb] = false
	c.mu.Unlock()
	c.kill(who)
	c.start(who)
	c.waitFor("a hello as the peer restarts", leases(6), func() bool {
		return c.status(sb).ClientToken == TokenSame
	})
}

// Asks for a hello come from a peer's votes and its link, which can flap
// at every other call (two processes behind its name alternating boot
// nonces, or a loaded link timing out votes): the hellos they send are
// spaced by a retry, never one after another. hello_asks_unspaced turns
// this red.
func TestAsksForAHelloAreSpaced(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	who := c.settledLeader(leases(20))
	sb := c.other(who)
	e := c.elector(sb)
	time.Sleep(leases(2)) // the start's own hellos are done
	c.mu.Lock()
	before := c.hellos[sb]
	c.mu.Unlock()
	const span = 3 * time.Second
	for end := time.Now().Add(span); time.Now().Before(end); time.Sleep(time.Millisecond) {
		e.hellosAgain()
	}
	c.mu.Lock()
	n := c.hellos[sb] - before
	c.mu.Unlock()
	// One a retry (a second here), and the one already waiting.
	if limit := int(span/helloRetry(loopLease)) + 2; n > limit {
		t.Fatalf("%d hellos in %v of asks, want at most %d", n, span, limit)
	}
}
