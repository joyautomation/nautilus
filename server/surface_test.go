package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/auth"
	"github.com/joyautomation/nautilus/auth/users"
)

// recentEntry is the newest journal entry with action, or false.
func recentEntry(srv *Server, action string) (audit.Entry, bool) {
	for _, e := range srv.audit.ring.Recent() {
		if e.Action == action {
			return e, true
		}
	}
	return audit.Entry{}, false
}

// A surface's sign-in is the browser's: the same users file, the same
// grants, and every attempt journalled as auth.signin from that surface,
// the refused ones with why (the client is told only ErrSignInRefused).
func TestSurfaceSignInIsSignIn(t *testing.T) {
	srv, _, _ := patServer(t)
	u := srv.Surface(audit.SourceOPCUA)
	if !u.SignsIn() {
		t.Fatal("a server with users and an auth: block does not sign in")
	}
	ctx := context.Background()
	user, cred, err := u.SignIn(ctx, "192.0.2.7", " JSmith ", "password-jsmith", map[string]any{"thumbprint": "abcd"})
	if err != nil || user != "jsmith" || cred == "" {
		t.Fatalf("jsmith: %q %q %v", user, cred, err)
	}
	e, ok := recentEntry(srv, "auth.signin")
	if !ok || e.Result != audit.ResultOK || e.User != "user:jsmith" || e.Source != audit.SourceOPCUA || e.From != "192.0.2.7" || e.Detail["thumbprint"] != "abcd" {
		t.Errorf("the sign-in's entry: %+v", e)
	}
	for _, c := range []struct{ name, pw, reason string }{
		{"jsmith", "wrong", "password"},
		{"nobody", "password-nobody", "no such user"},
		{"ghost", "password-ghost", "grants this user nothing"},
	} {
		if _, _, err := u.SignIn(ctx, "192.0.2.8", c.name, c.pw, nil); !errors.Is(err, ErrSignInRefused) {
			t.Errorf("%s: %v, want ErrSignInRefused", c.name, err)
		}
		e, _ := recentEntry(srv, "auth.signin")
		if e.Result != audit.ResultRefused || e.User != "anonymous" || e.Source != audit.SourceOPCUA || e.Target != "user:"+c.name ||
			!strings.Contains(strings.ToLower(e.Detail["reason"].(string)), c.reason) {
			t.Errorf("%s: refused entry %+v, want reason containing %q", c.name, e, c.reason)
		}
	}
	// Without users nothing signs in, and a nil Surface is inert.
	plain := auditServer(t, Options{})
	if pu := plain.Surface(audit.SourceOPCUA); pu.SignsIn() || pu.May(auth.User("jsmith"), "write", "SP") || pu.Holds("jsmith", cred) {
		t.Error("a server without users signs in, allows or holds")
	}
	if _, _, err := plain.Surface(audit.SourceOPCUA).SignIn(ctx, "192.0.2.7", "jsmith", "password-jsmith", nil); !errors.Is(err, ErrSignInRefused) {
		t.Errorf("sign-in without users: %v", err)
	}
	var none *Surface
	none.Record(audit.Entry{Action: "tag.write"})
	if none.SignsIn() {
		t.Error("a nil Surface signs in")
	}
}

// A surface spends the same throttle as POST /api/signin: guesses over
// OPC UA from one address leave the browser sign-in from that address
// throttled too, so a second door is not a way around the first.
func TestSurfaceSharesTheSignInThrottle(t *testing.T) {
	srv, _, _ := patServer(t)
	now := time.Now()
	clock := func() time.Time { return now }
	srv.signin.addrs.now, srv.signin.names.now = clock, clock
	u := srv.Surface(audit.SourceOPCUA)
	for i := range 10 {
		if _, _, err := u.SignIn(context.Background(), "192.0.2.50", "akhan", "guess", nil); err == nil {
			t.Fatalf("guess %d signed in", i+1)
		}
	}
	if _, _, err := u.SignIn(context.Background(), "192.0.2.50", "akhan", "password-akhan", nil); err == nil {
		t.Error("the 11th attempt from one address was not throttled")
	}
	// (Its entry is coalesced with the guesses before it: the sixth
	// refusal from one address in a minute is summed up, not written.)
	rec := call(t, srv, "POST", "/api/signin", `{"name":"akhan","password":"password-akhan"}`, ownOrigin, asJSON,
		func(r *http.Request) { r.RemoteAddr = "192.0.2.50:1"; overTLS(r) })
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("a browser sign-in from the address OPC UA guessed from: %d, want 429", rec.Code)
	}
	// And the hashing slots are the same: with them all taken, a surface's
	// attempt is refused as busy, not hashed beside them.
	for range signInHashing {
		srv.signin.hashing <- struct{}{}
	}
	old := signInQueue
	signInQueue = 50 * time.Millisecond
	_, _, err := u.SignIn(context.Background(), "192.0.2.60", "akhan", "password-akhan", nil)
	signInQueue = old
	for range signInHashing {
		<-srv.signin.hashing
	}
	if err == nil {
		t.Error("a surface sign-in hashed while every slot was taken")
	}
	if e, _ := recentEntry(srv, "auth.signin"); e.Detail["reason"] != "busy" {
		t.Errorf("the busy attempt's entry: %+v", e.Detail)
	}
}

// Holds is what a surface asks at every action: the user still in the
// file, enabled, with the password the sign-in proved, and granted
// something. Each change ends it, as it ends a browser's session.
func TestSurfaceHoldsEndsWithTheUser(t *testing.T) {
	srv, _, path := patServer(t)
	u := srv.Surface(audit.SourceOPCUA)
	for _, c := range []struct {
		what string
		do   func(*users.File) error
		undo func(*users.File) error
	}{
		{"disabled", func(f *users.File) error { return f.SetDisabled("jsmith", true) }, func(f *users.File) error { return f.SetDisabled("jsmith", false) }},
		{"password", func(f *users.File) error { return f.SetPassword("jsmith", "new-password") }, func(f *users.File) error { return f.SetPassword("jsmith", "password-jsmith") }},
		{"removed", func(f *users.File) error { delete(f.Users, "jsmith"); return nil }, func(f *users.File) error { return f.Add("jsmith", "password-jsmith") }},
	} {
		_, cred, err := u.SignIn(context.Background(), "192.0.2.9", "jsmith", "password-jsmith", nil)
		if err != nil {
			t.Fatal(err)
		}
		_, akCred, _ := u.SignIn(context.Background(), "192.0.2.9", "akhan", "password-akhan", nil)
		if !u.Holds("jsmith", cred) {
			t.Fatalf("%s: a fresh sign-in does not hold", c.what)
		}
		if u.Holds("akhan", cred) || u.Holds("jsmith", "") {
			t.Errorf("%s: a credential holds for another user, or an empty one holds", c.what)
		}
		if err := users.Update(path, "opsadmin", c.do); err != nil {
			t.Fatal(err)
		}
		srv.users.Reload()
		if u.Holds("jsmith", cred) {
			t.Errorf("%s: jsmith's sign-in still holds", c.what)
		}
		if !u.Holds("akhan", akCred) {
			t.Errorf("%s: akhan's sign-in ended too", c.what)
		}
		if err := users.Update(path, "opsadmin", c.undo); err != nil {
			t.Fatal(err)
		}
		srv.users.Reload()
	}
}

// May is the auth: block's answer for a user: jsmith operates the sp area
// only, akhan (engineer) everything.
func TestSurfaceMayFollowsTheAreas(t *testing.T) {
	srv, _, _ := patServer(t)
	u := srv.Surface(audit.SourceOPCUA)
	for _, c := range []struct {
		user, perm, target string
		want               bool
	}{
		{"jsmith", "write", "SP", true},
		{"jsmith", "write", "sp.Member", true},
		{"jsmith", "write", "PV", false},
		{"jsmith", "force", "SP", false},
		{"akhan", "write", "PV", true},
		{"ghost", "write", "SP", false},
		{"nobody", "write", "SP", false},
	} {
		if got := u.May(auth.User(c.user), c.perm, c.target); got != c.want {
			t.Errorf("May(%s, %s, %s) = %v, want %v", c.user, c.perm, c.target, got, c.want)
		}
	}
}

// Record goes through the journal's recorder: the surface's source is
// set whatever the entry said, values are capped as the HTTP seam caps
// them, and refusals from one client are coalesced.
func TestSurfaceRecordGoesThroughTheRecorder(t *testing.T) {
	srv, _, _ := patServer(t)
	u := srv.Surface(audit.SourceOPCUA)
	big := strings.Repeat("x", valueMax+10)
	u.Record(audit.Entry{User: "user:jsmith", Source: "hmi", Action: "tag.write", Target: strings.Repeat("T", targetMax+10), After: big, From: "abcd", Result: audit.ResultOK})
	e, _ := recentEntry(srv, "tag.write")
	if e.Source != audit.SourceOPCUA || len(e.Target) > targetMax+8 || e.After == big {
		t.Errorf("recorded: source %q target %d bytes, after capped %v", e.Source, len(e.Target), e.After != big)
	}
	before := len(srv.audit.ring.Recent())
	for range refuseFull + 20 {
		u.Record(audit.Entry{User: "anonymous", Action: "tag.write", Target: "SP", From: "192.0.2.99", Result: audit.ResultRefused})
	}
	if got := len(srv.audit.ring.Recent()) - before; got != refuseFull {
		t.Errorf("%d refusals from one client journalled in full, want %d (the rest coalesced)", got, refuseFull)
	}
}
