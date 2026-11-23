package node

import (
	"testing"
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
