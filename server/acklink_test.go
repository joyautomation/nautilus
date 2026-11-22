package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/alarm/acklink"
	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/auth"
	"github.com/joyautomation/nautilus/auth/users"
	"github.com/joyautomation/nautilus/internal/pairkey"
	"github.com/joyautomation/nautilus/leader"
	"github.com/joyautomation/nautilus/oplog"
)

// Ack links (#715 PR 3b-1): acknowledging from a page.

func linkRing(t *testing.T, token string) *acklink.Keyring {
	t.Helper()
	k, err := pairkey.Derive([]byte(token), pairkey.AckLink)
	if err != nil {
		t.Fatal(err)
	}
	r, err := acklink.New(k)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// withLinks gives s ack links under the pair token "peer".
func withLinks(t *testing.T, s *Server) *acklink.Keyring {
	t.Helper()
	s.ackLinks, s.ackURL = linkRing(t, "peer"), "https://hmi.example"
	return s.ackLinks
}

// linkPath is the path of a link for alarm id's current occurrence.
func linkPath(t *testing.T, s *Server, id, principal string) (path, token string) {
	t.Helper()
	u := s.AckLinkURL(id, occOf(t, s.alarms, id).Occurrence, principal, "email", 2)
	if u == "" {
		t.Fatal("no link minted")
	}
	path = strings.TrimPrefix(u, "https://hmi.example")
	return path, strings.TrimPrefix(path, acklink.Path)
}

// lastAudit is the newest audit entry.
func lastAudit(t *testing.T, s *Server) audit.Entry {
	t.Helper()
	es := s.audit.ring.Recent()
	if len(es) == 0 {
		t.Fatal("nothing journalled")
	}
	return es[0]
}

// Opening the link changes nothing (a mail scanner opens every link):
// the page shows the alarm and a button. Pressing it acknowledges the
// occurrence as the person paged, journalled as an HMI ack is, with
// via: link, the occurrence, the level and the link's ID, never the
// token.
func TestAckLinkConfirmsThenAcks(t *testing.T) {
	s := auditServer(t, Options{})
	withLinks(t, s)
	path, tok := linkPath(t, s, "HiTemp", "alice")
	occ := occOf(t, s.alarms, "HiTemp").Occurrence

	before := len(s.audit.ring.Recent())
	rec := call(t, s, "GET", path, "", func(r *http.Request) { r.Header.Set("Sec-Fetch-Mode", "navigate") })
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `<form method="post">`) || !strings.Contains(rec.Body.String(), "High temperature") {
		t.Fatalf("confirm page: %d\n%s", rec.Code, rec.Body)
	}
	if st := state(s.alarms, "HiTemp"); st != alarm.UnackActive || len(s.audit.ring.Recent()) != before {
		t.Fatalf("GET changed something: %s, %d journalled", st, len(s.audit.ring.Recent())-before)
	}
	h := rec.Header()
	if h.Get("Cache-Control") != "no-store" || !strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") || h.Get("Referrer-Policy") != "same-origin" {
		t.Fatalf("headers %v", h)
	}

	rec = call(t, s, "POST", path, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Acknowledged as alice") {
		t.Fatalf("POST: %d\n%s", rec.Code, rec.Body)
	}
	r := occOf(t, s.alarms, "HiTemp")
	if r.State != alarm.AckActive || r.AckBy != "alice" {
		t.Fatalf("after the link: %s by %q", r.State, r.AckBy)
	}
	e := lastAudit(t, s)
	if e.Action != "alarm.ack" || e.User != "user:alice" || e.Result != audit.ResultOK || e.Target != "HiTemp" {
		t.Fatalf("audit %+v", e)
	}
	d := e.Detail
	if d["via"] != "link" || d["occurrence"] != occ || d["level"] != 2 || d["channel"] != "email" || d["link"] == "" {
		t.Fatalf("audit detail %v", d)
	}
	b, _ := json.Marshal(e)
	if strings.Contains(string(b), tok) {
		t.Fatal("the token itself reached the audit journal")
	}
}

// A push page's link (#715 PR 6) acts as an email's does, journalled
// with channel: push; its page, opened in a phone's own window, leads
// back to the HMI.
func TestAckLinkFromAPush(t *testing.T) {
	s := auditServer(t, Options{})
	withLinks(t, s)
	u := s.AckLinkURL("HiTemp", occOf(t, s.alarms, "HiTemp").Occurrence, "alice", "push", 1)
	path := strings.TrimPrefix(u, "https://hmi.example")
	rec := call(t, s, "GET", path, "", func(r *http.Request) { r.Header.Set("Sec-Fetch-Mode", "navigate") })
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `<form method="post">`) || !strings.Contains(rec.Body.String(), `<a href="https://hmi.example" rel="noreferrer">Open the HMI</a>`) {
		t.Fatalf("confirm page: %d\n%s", rec.Code, rec.Body)
	}
	rec = call(t, s, "POST", path, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Acknowledged as alice") || !strings.Contains(rec.Body.String(), "Open the HMI") {
		t.Fatalf("POST: %d\n%s", rec.Code, rec.Body)
	}
	if e := lastAudit(t, s); e.Action != "alarm.ack" || e.Detail["channel"] != "push" || e.Detail["via"] != "link" {
		t.Fatalf("audit %+v", e)
	}
}

// A link is spent once: pressed again, or opened again, it says so and
// acknowledges nothing (on a controller with no pair, the record is this
// controller's).
func TestAckLinkIsSpentOnce(t *testing.T) {
	s := auditServer(t, Options{})
	withLinks(t, s)
	path, _ := linkPath(t, s, "HiTemp", "alice")
	if rec := call(t, s, "POST", path, ""); rec.Code != http.StatusOK {
		t.Fatalf("first: %d %s", rec.Code, rec.Body)
	}
	rec := call(t, s, "POST", path, "")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "already used") {
		t.Fatalf("second: %d %s", rec.Code, rec.Body)
	}
	if e := lastAudit(t, s); e.Result == audit.ResultOK || e.User != "user:alice" || e.Detail["via"] != "link" {
		t.Fatalf("the refused replay journalled %+v", e)
	}
	if rec := do(t, s, "GET", path, ""); !strings.Contains(rec.Body.String(), "already used") || strings.Contains(rec.Body.String(), "<form") {
		t.Fatalf("GET after use: %s", rec.Body)
	}
}

// A link names one occurrence: once the alarm is on another, it
// acknowledges nothing and says why.
func TestAckLinkIsBoundToItsOccurrence(t *testing.T) {
	s := auditServer(t, Options{})
	ring := withLinks(t, s)
	tok, _ := ring.Mint("HiTemp", "00000000deadbeef", "alice", "sms", 1, time.Hour)
	path := acklink.Path + tok
	if rec := do(t, s, "GET", path, ""); !strings.Contains(rec.Body.String(), "activated again") || strings.Contains(rec.Body.String(), "<form") {
		t.Fatalf("GET: %s", rec.Body)
	}
	rec := call(t, s, "POST", path, "")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "activated again") {
		t.Fatalf("POST: %d %s", rec.Code, rec.Body)
	}
	if st := state(s.alarms, "HiTemp"); st != alarm.UnackActive {
		t.Fatalf("acked %s", st)
	}
}

// A link that does not check acknowledges nothing: altered, signed by
// another controller, or expired (journalled under the person it named).
func TestAckLinkRefusesWhatDoesNotCheck(t *testing.T) {
	s := auditServer(t, Options{})
	ring := withLinks(t, s)
	occ := occOf(t, s.alarms, "HiTemp").Occurrence
	_, tok := linkPath(t, s, "HiTemp", "alice")
	altered := tok[:len(tok)-2] + map[bool]string{true: "AA", false: "BB"}[!strings.HasSuffix(tok, "AA")]
	other, _ := linkRing(t, "another pair").Mint("HiTemp", occ, "alice", "email", 1, time.Hour)
	for name, tk := range map[string]string{"altered": altered, "another key": other, "garbage": "x"} {
		rec := call(t, s, "POST", acklink.Path+tk, "")
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "not valid") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	ring.SetClock(func() time.Time { return time.Now().Add(-2 * time.Hour) })
	old, _ := ring.Mint("HiTemp", occ, "alice", "email", 1, time.Hour)
	ring.SetClock(time.Now)
	rec := call(t, s, "POST", acklink.Path+old, "")
	if rec.Code != http.StatusGone || !strings.Contains(rec.Body.String(), "expired") {
		t.Fatalf("expired: %d %s", rec.Code, rec.Body)
	}
	if e := lastAudit(t, s); e.User != "user:alice" || e.Result == audit.ResultOK || e.Detail["via"] != "link" {
		t.Fatalf("expired link journalled %+v", e)
	}
	if st := state(s.alarms, "HiTemp"); st != alarm.UnackActive {
		t.Fatalf("acked %s", st)
	}
	s.ackLinks = nil
	if rec := call(t, s, "POST", acklink.Path+tok, ""); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "sends no") {
		t.Fatalf("no links here: %d %s", rec.Code, rec.Body)
	}
}

// Without an auth: block, writes need NAUTILUS_TOKEN (or same origin);
// the link is its own credential for its one occurrence, and only that:
// the ack API still needs the token.
func TestAckLinkNeedsNoOtherCredential(t *testing.T) {
	s := auditServer(t, Options{AuthToken: "tok"})
	withLinks(t, s)
	path, _ := linkPath(t, s, "HiTemp", "alice")
	cross := func(r *http.Request) { r.Header.Set("Origin", "https://elsewhere.example") }
	if rec := call(t, s, "POST", "/api/alarms/ack", `{"ids":["HiTemp"],"by":"x"}`, cross); rec.Code != http.StatusUnauthorized {
		t.Fatalf("the ack API without the token: %d", rec.Code)
	}
	if rec := call(t, s, "POST", path, ""); rec.Code != http.StatusOK {
		t.Fatalf("the link without the token: %d %s", rec.Code, rec.Body)
	}
}

// Under an auth: block the link acts as the person it names only while
// they stand here and hold ack for the alarm: an engineer acks; an
// operator fenced to another area, a user with no grant, an unknown name
// and an OIDC name never signed in here are refused, and nothing is
// acknowledged.
func TestAckLinkActsOnlyAsAPersonWhoMayAck(t *testing.T) {
	s, _, _ := patServer(t)
	withLinks(t, s)
	for _, who := range []string{"jsmith", "ghost", "nobody", "oidc:carol"} {
		path, _ := linkPath(t, s, "HiTemp", who)
		rec := call(t, s, "POST", path, "")
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d %s", who, rec.Code, rec.Body)
		}
		if st := state(s.alarms, "HiTemp"); st != alarm.UnackActive {
			t.Fatalf("%s acked it: %s", who, st)
		}
	}
	// The confirm page is a page a browser opens: the read gate lets the
	// link holder see it without signing in.
	path, _ := linkPath(t, s, "HiTemp", "akhan")
	if rec := call(t, s, "GET", path, "", func(r *http.Request) { r.Header.Set("Sec-Fetch-Mode", "navigate") }); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<form") {
		t.Fatalf("confirm page under auth: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, s, "POST", path, ""); rec.Code != http.StatusOK {
		t.Fatalf("akhan: %d %s", rec.Code, rec.Body)
	}
	if r := occOf(t, s.alarms, "HiTemp"); r.State != alarm.AckActive || r.AckBy != "akhan" {
		t.Fatalf("akhan's link: %s by %q", r.State, r.AckBy)
	}
	if e := lastAudit(t, s); e.User != "user:akhan" || e.Result != audit.ResultOK {
		t.Fatalf("audit %+v", e)
	}
}

// With ack-from-notification: sign-in, a link acknowledges only for a
// browser signed in as the person it names: anonymous is sent to sign in,
// someone else is refused, the person acks.
func TestAckLinkSignInMode(t *testing.T) {
	s, _, _ := patServer(t)
	withLinks(t, s)
	s.ackLinkSignIn = true
	path, _ := linkPath(t, s, "HiTemp", "akhan")
	if rec := do(t, s, "GET", path, ""); !strings.Contains(rec.Body.String(), "/signin?next=") || strings.Contains(rec.Body.String(), "<form") {
		t.Fatalf("anonymous GET: %s", rec.Body)
	}
	if rec := call(t, s, "POST", path, ""); rec.Code != http.StatusUnauthorized || state(s.alarms, "HiTemp") != alarm.UnackActive {
		t.Fatalf("anonymous POST: %d %s", rec.Code, rec.Body)
	}
	other := signIn(t, s, "jsmith")
	if rec := call(t, s, "POST", path, "", withCookie(other.Value), ownOrigin); rec.Code != http.StatusUnauthorized || state(s.alarms, "HiTemp") != alarm.UnackActive {
		t.Fatalf("jsmith's browser: %d %s", rec.Code, rec.Body)
	}
	c := signIn(t, s, "akhan")
	if rec := call(t, s, "GET", path, "", withCookie(c.Value)); !strings.Contains(rec.Body.String(), "<form") {
		t.Fatalf("akhan's GET: %s", rec.Body)
	}
	if rec := call(t, s, "POST", path, "", withCookie(c.Value), ownOrigin); rec.Code != http.StatusOK {
		t.Fatalf("akhan's POST: %d %s", rec.Code, rec.Body)
	}
	if e := lastAudit(t, s); e.User != "user:akhan" || e.Session == "" {
		t.Fatalf("audit %+v: want akhan's session", e)
	}
}

// On a pair the link's ID rides the ack's log entry, and every replica
// refuses a second ack naming it: at the server before it is proposed,
// and at apply when two got past that at once.
func TestAckLinkIsSpentThroughTheLog(t *testing.T) {
	var lead atomic.Bool
	lead.Store(true)
	s, eng, al, got := loggedAlarms(t, map[string]any{"HiTempAlm": true, "LoFlowAlm": true}, &lead)
	eng.Evaluate()
	withLinks(t, s)
	path, _ := linkPath(t, s, "HiTemp", "alice")
	if rec := call(t, s, "POST", path, ""); rec.Code != http.StatusOK {
		t.Fatalf("first: %d %s", rec.Code, rec.Body)
	}
	if len(*got) != 1 || (*got)[0].Link == "" || (*got)[0].By != "alice" || len((*got)[0].Occs) != 1 {
		t.Fatalf("proposed %+v; want one ack carrying the link", *got)
	}
	link := (*got)[0].Link
	if !al.LinkSpent(link) {
		t.Fatal("the log does not hold the link as spent")
	}
	if rec := call(t, s, "POST", path, ""); rec.Code != http.StatusConflict || len(*got) != 1 {
		t.Fatalf("second: %d, %d proposed; want 409 and nothing proposed", rec.Code, len(*got))
	}

	// Two presses that both got past the server's check: the second entry
	// is not applied, and the leader journals why.
	lo := occOf(t, eng, "LoFlow")
	if _, err := al.Apply(oplog.Entry{Kind: oplog.KindAck, IDs: []string{"LoFlow"}, Occs: []oplog.Occ{{ID: "LoFlow", Occ: lo.Occurrence, ActiveMs: lo.ActiveMs}}, By: "mallory", Link: link, TS: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if st := state(eng, "LoFlow"); st != alarm.UnackActive {
		t.Fatalf("a replayed link acked LoFlow: %s", st)
	}
	if al.Held(true, nil)["LoFlow"].Acked {
		t.Fatal("a replayed link set an ack in the view")
	}
	evs, _ := eng.Journal(time.Now().Add(-time.Hour), time.Now().Add(time.Hour), alarm.Filter{})
	found := false
	for _, e := range evs {
		found = found || strings.Contains(e.Name, "its link was already used")
	}
	if !found {
		t.Fatal("the refused replay is not in the alarm journal")
	}
}

// A takeover: links are signed under a key both members derive from the
// peer token, so one minted by A checks on B; and B, whose log applied
// A's acks (as a standby does), refuses a link A spent.
func TestAckLinkSurvivesATakeover(t *testing.T) {
	var leadA, leadB atomic.Bool
	leadA.Store(true)
	cond := map[string]any{"HiTempAlm": true, "LoFlowAlm": true}
	a, engA, _, gotA := loggedAlarms(t, cond, &leadA)
	engA.Evaluate()
	withLinks(t, a)
	b, engB, alB, _ := loggedAlarms(t, cond, &leadB)
	engB.Evaluate()
	withLinks(t, b) // the same peer token, so the same key
	spent, _ := linkPath(t, a, "HiTemp", "alice")
	if rec := call(t, a, "POST", spent, ""); rec.Code != http.StatusOK {
		t.Fatalf("on A: %d %s", rec.Code, rec.Body)
	}
	for _, e := range *gotA { // B, standing by, applies what A committed
		if _, err := alB.Apply(e); err != nil {
			t.Fatal(err)
		}
	}
	// B takes over: a synced restore of its view (its engine is live:
	// noTerm), so HiTemp continues on A's occurrence, acknowledged.
	leadB.Store(true)
	PairAlarms{Engine: engB, Log: alB}.RestoreAlarmsSynced(nil, true)
	engB.Evaluate()
	if hi := occOf(t, engB, "HiTemp"); hi.State != alarm.AckActive || hi.AckBy != "alice" {
		t.Fatalf("HiTemp on B after the takeover: %s by %q", hi.State, hi.AckBy)
	}
	if rec := call(t, b, "POST", spent, ""); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "already used") {
		t.Fatalf("A's spent link on B: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, b, "GET", spent, ""); !strings.Contains(rec.Body.String(), "already used") {
		t.Fatalf("A's spent link's page on B: %s", rec.Body)
	}
	// A link A minted for LoFlow, unused, acks on B.
	lo := occOf(t, engB, "LoFlow")
	u := a.AckLinkURL("LoFlow", lo.Occurrence, "bob", "sms", 1)
	if rec := call(t, b, "POST", strings.TrimPrefix(u, "https://hmi.example"), ""); rec.Code != http.StatusOK {
		t.Fatalf("A's link on B: %d %s", rec.Code, rec.Body)
	}
	if r := occOf(t, engB, "LoFlow"); r.State != alarm.AckActive || r.AckBy != "bob" {
		t.Fatalf("LoFlow on B: %s by %q", r.State, r.AckBy)
	}
}

// The log forgets a spent link only once no link minted when it was
// spent could still check, by the entries' own times.
func TestSpentLinksArePrunedByTheLogsClock(t *testing.T) {
	var lead atomic.Bool
	_, _, al, _ := loggedAlarms(t, map[string]any{"HiTempAlm": true}, &lead)
	t0 := int64(1_700_000_000_000)
	al.Apply(oplog.Entry{Kind: oplog.KindAck, IDs: []string{"HiTemp"}, By: "a", Link: "old", TS: t0})
	al.Apply(oplog.Entry{Kind: oplog.KindAck, IDs: []string{"HiTemp"}, By: "a", Link: "mid", TS: t0 + acklink.MaxTTL.Milliseconds()})
	if !al.LinkSpent("old") || !al.LinkSpent("mid") {
		t.Fatal("pruned too soon")
	}
	al.Apply(oplog.Entry{Kind: oplog.KindAck, IDs: []string{"HiTemp"}, By: "a", Link: "new", TS: t0 + linkSpentFor.Milliseconds() + 1})
	if al.LinkSpent("old") || !al.LinkSpent("mid") || !al.LinkSpent("new") {
		t.Fatal("pruning wrong")
	}
	// A lapsed ack spends nothing: it never took effect.
	al.Apply(oplog.Entry{Kind: oplog.KindAck, IDs: []string{"HiTemp"}, By: "a", Link: "lapsed", TS: t0, Lapsed: true})
	if al.LinkSpent("lapsed") {
		t.Fatal("a lapsed ack spent its link")
	}
}

// A person the auth: block still grants ack but the users file disables
// (or no longer holds) is refused: the link stands only as long as the
// person does, as a session or a push subscription does.
func TestAckLinkStopsWithItsPerson(t *testing.T) {
	s, _, path := patServer(t)
	withLinks(t, s)
	link, _ := linkPath(t, s, "HiTemp", "akhan")
	f, err := users.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	f.SetDisabled("akhan", true)
	if err := f.Save(path, "opsadmin"); err != nil {
		t.Fatal(err)
	}
	s.users.Reload()
	rec := call(t, s, "POST", link, "")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "not an enabled user") {
		t.Fatalf("a disabled user's link: %d %s", rec.Code, rec.Body)
	}
	if st := state(s.alarms, "HiTemp"); st != alarm.UnackActive {
		t.Fatalf("acked %s", st)
	}
}

// Two presses of one link at once: the second is refused while the first
// is in flight (it cannot yet see the first as spent), and the link is
// free again only if the first did not spend it.
func TestLinkBookOneUseAtATime(t *testing.T) {
	var b linkBook
	if !b.begin("l1") || b.begin("l1") {
		t.Fatal("a link in use was begun twice")
	}
	if !b.begin("l2") {
		t.Fatal("another link waited on the first")
	}
	b.end("l1")
	if !b.begin("l1") {
		t.Fatal("a link ended is still busy")
	}
	now := time.Now()
	b.spend("old", now.Add(-time.Minute), now)
	b.spend("new", now.Add(time.Hour), now.Add(time.Second))
	if b.isSpent("old") || !b.isSpent("new") {
		t.Fatal("expired links are not pruned, or a live one was")
	}
}

// A person who stands here but holds no ack at all (a viewer) is refused
// by the link's own page, never a bare API refusal.
func TestAckLinkRefusesAViewerWithAPage(t *testing.T) {
	s, _, _ := patServer(t)
	withLinks(t, s)
	p, err := auth.Compile(auth.Settings{Users: map[string][]auth.Grant{"akhan": {{Role: auth.Engineer}}, "ghost": {{Role: auth.Viewer}}}})
	if err != nil {
		t.Fatal(err)
	}
	s.policy = p
	path, _ := linkPath(t, s, "HiTemp", "ghost")
	rec := call(t, s, "POST", path, "")
	if rec.Code != http.StatusForbidden || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") || !strings.Contains(rec.Body.String(), "may not acknowledge") {
		t.Fatalf("a viewer's link: %d %s %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	if st := state(s.alarms, "HiTemp"); st != alarm.UnackActive {
		t.Fatalf("acked %s", st)
	}
}

// On a pair, a press whose acknowledgement the log does not confirm in
// time may still take effect: the page says so in words a person can act
// on (not the API's), and the audit entry is "unknown".
func TestAckLinkUnknownOutcomePage(t *testing.T) {
	var lead atomic.Bool
	lead.Store(true)
	s, eng, _, _ := loggedAlarms(t, map[string]any{"HiTempAlm": true}, &lead)
	eng.Evaluate()
	withLinks(t, s)
	f := func(context.Context, oplog.Entry) (oplog.Entry, error) { return oplog.Entry{}, oplog.ErrUnconfirmed }
	alarmProposeSeam.Store(&f)
	t.Cleanup(func() { alarmProposeSeam.Store(nil) })
	path, _ := linkPath(t, s, "HiTemp", "alice")
	rec := call(t, s, "POST", path, "")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "may still take effect") || strings.Contains(rec.Body.String(), "/api/") {
		t.Fatalf("unknown outcome: %d %s", rec.Code, rec.Body)
	}
	if e := lastAudit(t, s); e.Result != audit.ResultUnknown || e.Detail["via"] != "link" {
		t.Fatalf("audit %+v", e)
	}
}

// The routes are registered by literal (the audit test reads them): they
// must be acklink.Path, which the links are built with.
func TestAckLinkRoutesAreAcklinkPath(t *testing.T) {
	if acklink.Path != "/alarm-ack/" {
		t.Fatalf("acklink.Path is %q: change the two routes in server.go with it", acklink.Path)
	}
}

// A link that lands on a standby is the leader's to answer: GET and POST
// are forwarded (the leader checks the link with the same pair key), with
// a sign-in cookie or without; the standby's own engine is never asked.
func TestAckLinkIsForwardedByAStandby(t *testing.T) {
	var paths []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.Write([]byte("from-leader"))
	}))
	defer backend.Close()
	s := auditServer(t, Options{Cluster: fakeCluster{status: leader.Status{
		Mode: "quorum", Pod: "b", Leader: "a", LeaderAddr: standbyAddr(backend)}}})
	withLinks(t, s)
	path, _ := linkPath(t, s, "HiTemp", "alice")
	for _, m := range []string{"GET", "POST"} {
		rec := call(t, s, m, path, "", withCookie("some-session"))
		if !strings.Contains(rec.Body.String(), "from-leader") {
			t.Fatalf("%s on the standby: %d %s", m, rec.Code, rec.Body)
		}
	}
	if len(paths) != 2 || paths[0] != "GET "+path || paths[1] != "POST "+path {
		t.Fatalf("the leader saw %v", paths)
	}
	if st := state(s.alarms, "HiTemp"); st != alarm.UnackActive {
		t.Fatalf("the standby's engine was acked: %s", st)
	}
}

// With no leader known, a link can do nothing here: GET and POST answer a
// page that says so (not "retry" on a standby's engine), the press is
// journalled with that reason in words, and nothing is acknowledged.
func TestAckLinkWithNoLeaderSaysSo(t *testing.T) {
	s := auditServer(t, Options{Cluster: fakeCluster{status: leader.Status{Mode: "quorum", Pod: "b"}}})
	withLinks(t, s)
	path, _ := linkPath(t, s, "HiTemp", "alice")
	for _, m := range []string{"GET", "POST"} {
		rec := call(t, s, m, path, "")
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "No controller is leading") {
			t.Fatalf("%s with no leader: %d %s", m, rec.Code, rec.Body)
		}
		if rec.Header().Get(linkReasonHeader) != "" {
			t.Fatal("the seam's reason header reached the client")
		}
	}
	if e := lastAudit(t, s); e.Result == audit.ResultOK || !strings.Contains(fmt.Sprint(e.Detail["reason"]), "No controller is leading") {
		t.Fatalf("audit %+v", e)
	}
	if st := state(s.alarms, "HiTemp"); st != alarm.UnackActive {
		t.Fatalf("acked %s", st)
	}
}

// A replica that does not lead and has no leader's address (the proxy
// lets it answer) never acknowledges on its own engine.
func TestAckLinkNotLeadingNeverAcksItsEngine(t *testing.T) {
	s := auditServer(t, Options{Cluster: fakeCluster{status: leader.Status{Mode: "cluster", Pod: "b"}}})
	withLinks(t, s)
	path, _ := linkPath(t, s, "HiTemp", "alice")
	rec := call(t, s, "POST", path, "")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "not leading") {
		t.Fatalf("POST on a replica not leading: %d %s", rec.Code, rec.Body)
	}
	if st := state(s.alarms, "HiTemp"); st != alarm.UnackActive {
		t.Fatalf("acked %s", st)
	}
}

// A refused press is journalled with the page's words as its reason,
// never its HTML.
func TestAckLinkRefusalReasonIsWords(t *testing.T) {
	s := auditServer(t, Options{})
	withLinks(t, s)
	path, _ := linkPath(t, s, "HiTemp", "alice")
	call(t, s, "POST", path, "")
	rec := call(t, s, "POST", path, "")
	r := fmt.Sprint(lastAudit(t, s).Detail["reason"])
	if !strings.Contains(r, "already used") || strings.Contains(r, "<") {
		t.Fatalf("reason %q", r)
	}
	if rec.Header().Get(linkReasonHeader) != "" {
		t.Fatal("the seam's reason header reached the client")
	}
	call(t, s, "POST", acklink.Path+"forged", "")
	if r := fmt.Sprint(lastAudit(t, s).Detail["reason"]); !strings.Contains(r, "not valid") || strings.Contains(r, "<") {
		t.Fatalf("forged link reason %q", r)
	}
}

// A superseded history drops the view's acks, not the spent links: a link
// refused once too often costs a visit to the HMI, one honoured twice an
// ack nobody gave.
func TestSpentLinksOutliveADroppedView(t *testing.T) {
	var lead atomic.Bool
	_, _, al, _ := loggedAlarms(t, map[string]any{"HiTempAlm": true}, &lead)
	al.Apply(oplog.Entry{Kind: oplog.KindAck, IDs: []string{"HiTemp"}, By: "a", Link: "l1", TS: 1})
	al.dropView()
	if !al.LinkSpent("l1") {
		t.Fatal("dropping the view forgot a spent link")
	}
}

// The read gate lets through only the link's own page, one segment: a
// deeper path under it is a page like any other.
func TestReadGateExemptsOnlyTheLinkPage(t *testing.T) {
	for p, want := range map[string]bool{acklink.Path + "tok": true, acklink.Path + "a/b": false, "/alarm-acks": false, "/signin": true} {
		if signInPath(p) != want {
			t.Errorf("signInPath(%q) = %v", p, !want)
		}
	}
}

// A browser signed in as the paged person whose request did not come from
// this controller's own page (a TLS proxy not in server.trusted-proxies):
// by default its sign-in is ignored and the link alone acknowledges, as
// for a browser not signed in, journalled with why; under sign-in it is
// refused with the proxy fix named.
func TestAckLinkIgnoresASessionFromAnotherOrigin(t *testing.T) {
	proxied := func(r *http.Request) { r.Header.Set("Origin", "https://hmi.example") }
	s, _, _ := patServer(t)
	withLinks(t, s)
	c := signIn(t, s, "akhan")
	path, _ := linkPath(t, s, "HiTemp", "akhan")
	rec := call(t, s, "POST", path, "", withCookie(c.Value), proxied)
	if rec.Code != http.StatusOK {
		t.Fatalf("link mode, proxied sign-in: %d %s", rec.Code, rec.Body)
	}
	e := lastAudit(t, s)
	if e.User != "user:akhan" || e.Session != "" || !strings.Contains(fmt.Sprint(e.Detail["session"]), "ignored") {
		t.Fatalf("audit %+v: want akhan by the link, the sign-in noted as ignored", e)
	}

	s2, _, _ := patServer(t)
	withLinks(t, s2)
	s2.ackLinkSignIn = true
	c2 := signIn(t, s2, "akhan")
	path2, _ := linkPath(t, s2, "HiTemp", "akhan")
	rec = call(t, s2, "POST", path2, "", withCookie(c2.Value), proxied)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "server.trusted-proxies") {
		t.Fatalf("sign-in mode, proxied: %d %s", rec.Code, rec.Body)
	}
	if st := state(s2.alarms, "HiTemp"); st != alarm.UnackActive {
		t.Fatalf("acked %s", st)
	}
	// The same browser from the controller's own page acks with its session.
	if rec := call(t, s2, "POST", path2, "", withCookie(c2.Value), ownOrigin); rec.Code != http.StatusOK {
		t.Fatalf("sign-in mode, own page: %d %s", rec.Code, rec.Body)
	}
}
