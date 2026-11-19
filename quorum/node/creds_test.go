package node

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// credPair is two electors, a and b, each with credentials the test sets,
// exchanging hellos through their own handlers: a asks, b answers.
type credPair struct {
	mu      sync.Mutex
	a, b    *Elector
	ca, cb  Credentials
	eventsA []Event
}

func newCredPair(t *testing.T) *credPair {
	t.Helper()
	p := &credPair{}
	oa := testOptions(t, "a", "b")
	oa.Credentials = func() Credentials { p.mu.Lock(); defer p.mu.Unlock(); return p.ca }
	ob := testOptions(t, "b", "a")
	ob.Credentials = func() Credentials { p.mu.Lock(); defer p.mu.Unlock(); return p.cb }
	var err error
	if p.a, err = New(oa); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.a.Close() })
	if p.b, err = New(ob); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.b.Close() })
	return p
}

// hello has a ask b once and judge b's answer; answer, when set, edits
// b's answer on the way (a forgery, an older build).
func (p *credPair) hello(t *testing.T, answer func(*helloAnswer)) {
	t.Helper()
	c := newChallenge()
	rec := httptest.NewRecorder()
	p.b.serveHello(rec, httptest.NewRequest(http.MethodGet, "/hello?c="+c+"&from=a", nil))
	var h helloAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &h); err != nil {
		t.Fatal(err)
	}
	if answer != nil {
		answer(&h)
	}
	_, verified := p.a.answerVerified(c, h)
	p.a.checkCredentials(c, h, verified)
}

// judge has a judge answer h for challenge c, as checkToken does.
func (p *credPair) judge(c string, h helloAnswer) {
	_, verified := p.a.answerVerified(c, h)
	p.a.checkCredentials(c, h, verified)
}

// answerFrom has e answer a hello asked by from with challenge c.
func answerFrom(t *testing.T, e *Elector, from, c string) helloAnswer {
	t.Helper()
	rec := httptest.NewRecorder()
	e.serveHello(rec, httptest.NewRequest(http.MethodGet, "/hello?c="+c+"&from="+from, nil))
	var h helloAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &h); err != nil {
		t.Fatal(err)
	}
	return h
}

func (p *credPair) set(ca, cb Credentials) { p.mu.Lock(); p.ca, p.cb = ca, cb; p.mu.Unlock() }

// drain moves what a raised so far into eventsA (its events wait on a
// channel that only Run's goroutine hands to OnEvent; a is not run here).
func (p *credPair) drain() {
	for {
		select {
		case ev := <-p.a.events:
			p.mu.Lock()
			p.eventsA = append(p.eventsA, ev)
			p.mu.Unlock()
		default:
			return
		}
	}
}

// lastEvent is the last event of kind k a raised, and whether there was one.
func (p *credPair) lastEvent(k EventKind) (Event, bool) {
	p.drain()
	p.mu.Lock()
	defer p.mu.Unlock()
	var got Event
	var ok bool
	for _, ev := range p.eventsA {
		if ev.Kind == k {
			got, ok = ev, true
		}
	}
	return got, ok
}

func digest(s string) [32]byte { return sha256.Sum256([]byte(s)) }

// #708, report only: at every hello a compares b's users file and tag API
// certificate with its own, read anew each time, through MACs that never
// carry the digests. Each part is same, differs, unreadable or off, shown
// in Status and raised as an event (the banner's condition) on a change.
// creds_unbound turns the differs cases red (they would read as same).
func TestCredentialCheck(t *testing.T) {
	p := newCredPair(t)
	st := func() (CredState, CredState) { s := p.a.Status(); return s.Users, s.TLS }

	// Neither has users or server.tls.
	p.hello(t, nil)
	if u, tl := st(); u != CredOff || tl != CredOff {
		t.Fatalf("no credentials on either: users %s, tls %s; want off, off", u, tl)
	}

	// The same users file and certificate on both.
	same := Credentials{Users: digest("users v1"), TLS: digest("leaf 1")}
	p.set(same, same)
	p.hello(t, nil)
	if u, tl := st(); u != CredSame || tl != CredSame {
		t.Fatalf("the same on both: users %s, tls %s; want same, same", u, tl)
	}

	// Users added separately on b: re-read at the next hello, reported.
	p.set(same, Credentials{Users: digest("users v1, salted on b"), TLS: digest("leaf 1")})
	p.hello(t, nil)
	if u, _ := st(); u != CredDiffers {
		t.Fatalf("different users files: %s; want differs", u)
	}
	if ev, ok := p.lastEvent(EventUsersDiffer); !ok || !strings.Contains(ev.Detail, "copy the same users.json") || !strings.Contains(ev.Detail, "never add users separately") {
		t.Fatalf("event %+v (%v); want the users-differ message saying how to fix it", ev, ok)
	}
	if !p.a.mayCampaign(p.a.now()) {
		t.Fatal("a users mismatch held this controller's campaigns; the check is report only")
	}

	// Fixed: the same file copied to both. The condition clears.
	p.set(same, same)
	p.hello(t, nil)
	if u, _ := st(); u != CredSame {
		t.Fatalf("after copying the file: %s; want same", u)
	}
	if ev, _ := p.lastEvent(EventUsersDiffer); ev.Detail != "" {
		t.Fatalf("the cleared condition still says %q", ev.Detail)
	}

	// A renewed certificate on b only: differs, with the renewal note.
	p.set(same, Credentials{Users: digest("users v1"), TLS: digest("leaf 2")})
	p.hello(t, nil)
	if _, tl := st(); tl != CredDiffers {
		t.Fatalf("different leaves: %s; want differs", tl)
	}
	if ev, ok := p.lastEvent(EventTLSDiffers); !ok || !strings.Contains(ev.Detail, "renewed certificate") {
		t.Fatalf("event %+v (%v); want the TLS message naming a renewal", ev, ok)
	}

	// b cannot read its users file; then a cannot read its own.
	p.set(same, Credentials{UsersErr: "users.json: malformed", TLS: digest("leaf 1")})
	p.hello(t, nil)
	if u, _ := st(); u != CredUnreadable {
		t.Fatalf("the peer's file unreadable: %s; want unreadable", u)
	}
	if ev, _ := p.lastEvent(EventUsersDiffer); !strings.Contains(ev.Detail, "peer controller (b) cannot read") {
		t.Fatalf("event %q; want it to name the peer", ev.Detail)
	}
	p.set(Credentials{UsersErr: "permission denied", TLS: digest("leaf 1")}, same)
	p.hello(t, nil)
	if ev, _ := p.lastEvent(EventUsersDiffer); !strings.Contains(ev.Detail, "this controller (a) cannot read") || strings.Contains(ev.Detail, "permission denied") {
		t.Fatalf("event %q; want it to name this controller, and never carry the error (/api/cluster answers without a token: the detail is for the log)", ev.Detail)
	}

	// An answer that does not prove itself under the peer token (another
	// token, or a forgery) is unverified: never same, and the files are
	// not blamed.
	p.set(same, same)
	p.hello(t, func(h *helloAnswer) { h.UsersMAC, h.InfoMAC = strings.Repeat("0", 64), strings.Repeat("0", 64) })
	if u, _ := st(); u != CredUnverified {
		t.Fatalf("a forged answer: %s; want unverified", u)
	}
	if ev, _ := p.lastEvent(EventUsersDiffer); !strings.Contains(ev.Detail, "could not be verified") || strings.Contains(ev.Detail, "users files differ") {
		t.Fatalf("event %q; want the cannot-verify message, not the files blamed", ev.Detail)
	}

	// A build before the check sends nothing: unknown, nothing raised.
	p.drain()
	n := len(p.eventsA)
	p.hello(t, func(h *helloAnswer) { h.Users, h.UsersMAC, h.TLS, h.TLSMAC = "", "", "", "" })
	if u, tl := st(); u != CredUnknown || tl != CredUnknown {
		t.Fatalf("an older peer: users %s, tls %s; want unknown", u, tl)
	}
	if ev, _ := p.lastEvent(EventUsersDiffer); ev.Detail != "" {
		t.Fatalf("an older peer left the earlier condition standing: %q", ev.Detail)
	}
	_ = n
}

// #1046 review (3): a raised condition does not outlive the check. Users
// differ, then the peer stops answering: the condition clears instead of
// standing on a state nobody re-checks.
func TestCredentialConditionClearsWhenTheCheckCannotRun(t *testing.T) {
	p := newCredPair(t)
	p.set(Credentials{Users: digest("a")}, Credentials{Users: digest("b")})
	p.hello(t, nil)
	if ev, _ := p.lastEvent(EventUsersDiffer); ev.Detail == "" {
		t.Fatal("no condition raised for different files")
	}
	p.a.credentialsUnknown() // checkToken's path when the peer does not answer
	if ev, _ := p.lastEvent(EventUsersDiffer); ev.Detail != "" {
		t.Fatalf("the condition stood after the check could not run: %q", ev.Detail)
	}
	if u := p.a.Status().Users; u != CredUnknown {
		t.Fatalf("users %s; want unknown", u)
	}
}

// #1046 review (2): two controllers with different peer tokens cannot
// verify each other's answers: the check says so instead of blaming the
// users files.
func TestCredentialCheckWithAnotherPeerToken(t *testing.T) {
	p := newCredPair(t)
	same := Credentials{Users: digest("users"), TLS: digest("leaf")}
	p.set(same, same)
	p.b.o.Token = "another-peer-token"
	p.hello(t, nil)
	if u, tl := p.a.Status().Users, p.a.Status().TLS; u != CredUnverified || tl != CredUnverified {
		t.Fatalf("another peer token: users %s, tls %s; want unverified", u, tl)
	}
}

// #1046 review (4), end to end: a's own answer to a question "from b",
// reflected back as b's answer, and b's answer to an earlier challenge
// replayed, never read as the same credentials, though a and b differ.
func TestCredentialAnswersReflectedOrReplayed(t *testing.T) {
	p := newCredPair(t)
	p.set(Credentials{Users: digest("a's")}, Credentials{Users: digest("b's")})
	c := newChallenge()
	p.judge(c, answerFrom(t, p.a, "b", c)) // a answering itself
	if u := p.a.Status().Users; u == CredSame {
		t.Fatalf("a reflected answer read as %s", u)
	}
	p.set(Credentials{Users: digest("a's")}, Credentials{Users: digest("a's")})
	old := newChallenge()
	h := answerFrom(t, p.b, "a", old)
	p.set(Credentials{Users: digest("a's, edited")}, Credentials{Users: digest("b's, edited")})
	p.judge(newChallenge(), h) // replayed for a fresh challenge
	if u := p.a.Status().Users; u == CredSame {
		t.Fatalf("a replayed answer read as %s", u)
	}
}

// #1046 review (4), the credential MAC itself binds the names and the
// challenge, even for an answer otherwise verified: a's own MAC (asked
// "from b") and a MAC for an earlier challenge never match a's digest.
// creds_names_unbound and creds_challenge_unbound turn it red.
func TestCredMACBindsNamesAndChallenge(t *testing.T) {
	d := digest("users")
	c := newChallenge()
	reflected := credMAC(testToken, c, "b", "a", "users", credOK, d) // a's answer to b
	if got := judgeCred(testToken, c, "a", "b", "users", credOK, reflected, d, "", true); got == CredSame {
		t.Errorf("a reflected MAC read as %s", got)
	}
	replayed := credMAC(testToken, newChallenge(), "a", "b", "users", credOK, d)
	if got := judgeCred(testToken, c, "a", "b", "users", credOK, replayed, d, "", true); got == CredSame {
		t.Errorf("a replayed MAC read as %s", got)
	}
	if got := judgeCred(testToken, c, "a", "b", "users", credOK, credMAC(testToken, c, "a", "b", "users", credOK, d), d, "", true); got != CredSame {
		t.Fatalf("the genuine MAC read as %s", got)
	}
}
