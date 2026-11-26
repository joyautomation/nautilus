package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/auth/users"
	"github.com/joyautomation/nautilus/oplog"
)

const pairIssuer = "https://idp.example" // fakeOIDC.as's

// oidcPairMember is one controller of a pair with OIDC sign-in (#710): its
// own provider client, its own state directory (users file, pins,
// sign-outs) and its own session log, joined to the other's by bus.
type oidcPairMember struct {
	srv  *Server
	fake *fakeOIDC
	path string // its users file; the pins are beside it
	log  *SessionLog
}

func joinBus(bus *fakeBus, l *SessionLog) {
	l.Attach(busEnd{bus})
	bus.mu.Lock()
	bus.members = append(bus.members, l)
	bus.mu.Unlock()
}

func oidcPairOn(t *testing.T, bus *fakeBus, issuer string, before ...oplog.Entry) oidcPairMember {
	t.Helper()
	l := NewSessionLog()
	t.Cleanup(l.Close)
	for _, e := range before { // the log's replay at start, before the server binds
		l.Apply(e)
	}
	srv, fake, path := oidcServer(t, Options{SessionLog: l, OIDCIssuer: issuer})
	joinBus(bus, l)
	return oidcPairMember{srv: srv, fake: fake, path: path, log: l}
}

func (m oidcPairMember) me(t *testing.T, cookie string) string {
	t.Helper()
	return call(t, m.srv, "GET", "/api/me", "", withCookie(cookie)).Body.String()
}

func (m oidcPairMember) pin(t *testing.T, name string) (users.Pin, bool) {
	t.Helper()
	pins, err := users.LoadPins(users.PinsPath(m.path))
	if err != nil {
		t.Fatal(err)
	}
	p, ok := pins.Pins[name]
	return p, ok
}

func (m oidcPairMember) pinEntry(result string) map[string]any {
	for _, e := range m.srv.audit.ring.Recent() {
		if e.Action == "auth.pin" && string(e.Result) == result {
			return e.Detail
		}
	}
	return nil
}

// Each controller of a pair signs in at the provider with its own config
// and keeps its own pins; a sign-in on the leader pins the name on the
// other controller too, through the log, so after a takeover the operator
// is still signed in there, and no other account can take the name there
// first. A fresh sign-in on the new leader works.
func TestOIDCSignInCarriesAcrossAPair(t *testing.T) {
	bus := &fakeBus{synced: true}
	a := oidcPairOn(t, bus, pairIssuer)
	b := oidcPairOn(t, bus, pairIssuer)
	a.fake.as("jsmith@corp.example", "sub-1")
	_, c := oidcSignIn(t, a.srv, a.fake, "/")
	if c == nil {
		t.Fatal("sign-in on a failed")
	}
	waitFor(t, "the sign-in to act on b", func() bool {
		return strings.Contains(b.me(t, c.Value), `"principal":"oidc:jsmith@corp.example"`)
	})
	if p, ok := b.pin(t, "jsmith@corp.example"); !ok || !p.Same(pairIssuer, "sub-1") {
		t.Errorf("b's pin = %+v %v, want sub-1", p, ok)
	}
	if d := b.pinEntry("ok"); d == nil || d["from"] != "the other controller" || d["subject"] != "sub-1" {
		t.Errorf("b journalled %+v", d)
	}
	// Another account under the same name is refused on b, as on a.
	b.fake.as("jsmith@corp.example", "sub-2")
	cb, c2 := oidcSignIn(t, b.srv, b.fake, "/")
	if cb.Code != http.StatusForbidden || c2 != nil || !strings.Contains(cb.Body.String(), "naut users unpin jsmith@corp.example on the controller (on a redundant pair, on both controllers)") {
		t.Fatalf("another account on b: %d %s", cb.Code, cb.Body)
	}
	// The same account signs in afresh on b (the new leader).
	b.fake.as("jsmith@corp.example", "sub-1")
	if _, c3 := oidcSignIn(t, b.srv, b.fake, "/"); c3 == nil || !strings.Contains(b.me(t, c3.Value), `"principal":"oidc:jsmith@corp.example"`) {
		t.Fatal("a fresh sign-in on b failed")
	}
}

// A name pinned on the other controller to another account keeps its pin:
// the session from the log does not act there, and the journal names the
// fix.
func TestOIDCPairPinConflictKeepsThePin(t *testing.T) {
	bus := &fakeBus{synced: true}
	a := oidcPairOn(t, bus, pairIssuer)
	b := oidcPairOn(t, bus, pairIssuer)
	if err := users.UpdatePins(users.PinsPath(b.path), "opsadmin", func(p *users.Pins) error {
		p.Pins["jsmith@corp.example"] = users.Pin{Issuer: pairIssuer, Subject: "sub-9", Pinned: time.Now()}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	b.srv.users.Reload()
	a.fake.as("jsmith@corp.example", "sub-1")
	_, c := oidcSignIn(t, a.srv, a.fake, "/")
	if c == nil {
		t.Fatal("sign-in on a failed")
	}
	waitFor(t, "b to refuse it", func() bool { return b.pinEntry("refused") != nil })
	if d := b.pinEntry("refused"); !strings.Contains(d["fix"].(string), "naut users unpin jsmith@corp.example on both controllers") {
		t.Errorf("refusal %+v", d)
	}
	if p, _ := b.pin(t, "jsmith@corp.example"); !p.Same(pairIssuer, "sub-9") {
		t.Errorf("b's pin was overwritten: %+v", p)
	}
	if got := b.me(t, c.Value); !strings.Contains(got, `"principal":"anonymous"`) {
		t.Errorf("the session acts on b against its pin: %s", got)
	}
}

// A sign-in at another issuer than the controller's own pins nothing there
// and does not act there; the journal says why and what to fix.
func TestOIDCPairOtherIssuerIsRefused(t *testing.T) {
	bus := &fakeBus{synced: true}
	a := oidcPairOn(t, bus, pairIssuer)
	b := oidcPairOn(t, bus, "https://other-idp.example")
	a.fake.as("jsmith@corp.example", "sub-1")
	_, c := oidcSignIn(t, a.srv, a.fake, "/")
	if c == nil {
		t.Fatal("sign-in on a failed")
	}
	waitFor(t, "b to refuse it", func() bool { return b.pinEntry("refused") != nil })
	if d := b.pinEntry("refused"); !strings.Contains(d["fix"].(string), "same auth.oidc.issuer") {
		t.Errorf("refusal %+v", d)
	}
	if _, ok := b.pin(t, "jsmith@corp.example"); ok {
		t.Error("b pinned a name from another issuer")
	}
	if got := b.me(t, c.Value); !strings.Contains(got, `"principal":"anonymous"`) {
		t.Errorf("a session of another issuer acts on b: %s", got)
	}
	// Even with a pin there that names that issuer (a config changed since):
	// the issuer is checked on every request.
	if err := users.UpdatePins(users.PinsPath(b.path), "opsadmin", func(p *users.Pins) error {
		p.Pins["jsmith@corp.example"] = users.Pin{Issuer: pairIssuer, Subject: "sub-1", Pinned: time.Now()}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	b.srv.users.Reload()
	if got := b.me(t, c.Value); !strings.Contains(got, `"principal":"anonymous"`) {
		t.Errorf("a session of another issuer acts on b with a pin: %s", got)
	}
}

// Sign-out wins: a session signed out on the leader acts no more on the
// other controller after a takeover; and a controller that starts with an
// open and its end in the log's replay pins nothing from it (an unpin's
// end is such an end), while one with a standing open pins it at start.
// An expired open pins nothing either.
func TestOIDCPairSignOutWinsAndReplayPinsOnlyStanding(t *testing.T) {
	bus := &fakeBus{synced: true}
	a := oidcPairOn(t, bus, pairIssuer)
	b := oidcPairOn(t, bus, pairIssuer)
	a.fake.as("jsmith@corp.example", "sub-1")
	_, c := oidcSignIn(t, a.srv, a.fake, "/")
	if c == nil {
		t.Fatal("sign-in on a failed")
	}
	waitFor(t, "the sign-in to act on b", func() bool {
		return strings.Contains(b.me(t, c.Value), `"principal":"oidc:jsmith@corp.example"`)
	})
	a.srv.sessions.signOut(c.Value) // returns once the end committed
	if got := b.me(t, c.Value); !strings.Contains(got, `"principal":"anonymous"`) {
		t.Errorf("a signed-out session acts on b: %s", got)
	}

	bus.mu.Lock()
	ended := append([]oplog.Entry(nil), bus.log...) // its open, then its end
	bus.mu.Unlock()
	r := oidcPairOn(t, &fakeBus{synced: true}, pairIssuer, ended...)
	if _, ok := r.pin(t, "jsmith@corp.example"); ok {
		t.Error("a replayed open whose end followed it pinned the name")
	}

	a.fake.as("akhan", "sub-7")
	_, c2 := oidcSignIn(t, a.srv, a.fake, "/")
	if c2 == nil {
		t.Fatal("second sign-in on a failed")
	}
	var open oplog.Entry
	waitFor(t, "akhan's open to commit", func() bool {
		bus.mu.Lock()
		defer bus.mu.Unlock()
		for _, e := range bus.log {
			if b, ok := e.SessionBody(); ok && b.Op == oplog.SessionOpen && b.User == "akhan" {
				open = e
				return true
			}
		}
		return false
	})
	s := oidcPairOn(t, &fakeBus{synced: true}, pairIssuer, open)
	if p, ok := s.pin(t, "akhan"); !ok || !p.Same(pairIssuer, "sub-7") {
		t.Errorf("a standing replayed open did not pin at start: %+v %v", p, ok)
	}

	body, _ := open.SessionBody()
	body.Hash, body.User, body.Expires = "ff"+body.Hash[2:], "gone@corp.example", time.Now().Add(-time.Minute).UnixMilli()
	x := oidcPairOn(t, &fakeBus{synced: true}, pairIssuer, oplog.SessionEntryOf("system", body))
	if _, ok := x.pin(t, "gone@corp.example"); ok {
		t.Error("an expired open pinned its name")
	}
}

// A name signed out on the other controller (naut users signout there, at
// or after the session's start) is not pinned from that session.
func TestOIDCPairSignedOutNameIsNotPinned(t *testing.T) {
	bus := &fakeBus{synced: true}
	a := oidcPairOn(t, bus, pairIssuer)
	b := oidcPairOn(t, bus, pairIssuer)
	if err := users.SignOut(users.SignoutsPath(b.path), "opsadmin", "oidc:jsmith@corp.example", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	b.srv.users.Reload()
	a.fake.as("jsmith@corp.example", "sub-1")
	_, c := oidcSignIn(t, a.srv, a.fake, "/")
	if c == nil {
		t.Fatal("sign-in on a failed")
	}
	waitFor(t, "b to adopt it", func() bool { return b.srv.sessions.has(sessionHash(c.Value)) })
	if _, ok := b.pin(t, "jsmith@corp.example"); ok {
		t.Error("b pinned a name from a session its sign-out ended")
	}
}
