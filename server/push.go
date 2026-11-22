package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/auth"
	"github.com/joyautomation/nautilus/internal/webpush"
	"github.com/joyautomation/nautilus/retain"
	"github.com/joyautomation/nautilus/runtime"
)

// Web Push (#722 slice 2): a person's phones are subscribed to the
// controller's pager, and the alarm channel pages them through Send.
//
//	GET    /api/push/key            the VAPID public key a browser subscribes with
//	POST   /api/push/subscribe      {endpoint, keys:{p256dh, auth}}: this person's phone
//	DELETE /api/push/subscribe      {id} or {endpoint}: take it off
//	GET    /api/push/subscriptions  this person's own, without endpoint or keys
//	GET    /api/push/health         config permission: counts, never a row
//
// A subscription belongs to the principal the session names, never one in
// the body, and only a person (user or OIDC) may hold one. It does not
// end with the session (a pager must keep working on a phone signed out
// for a day), but only while that person still proves what they proved
// when subscribing: a password change, a removed or disabled user, a
// revoked role or a gone OIDC pin ends it (sweep, at each send and on a
// timer), with no live session needed. The rows are retained so a
// takeover pages the same phones, sealed (internal/webpush): endpoint,
// p256dh and auth are never in a response, a journal entry or a log line.

// PushOptions turns Web Push on. The key is the VAPID key from the
// secrets file; the same file (so key) must be on both members of a pair.
type PushOptions struct {
	Key *webpush.Key
	// Subject is the mailto: or https: URL a push service may reach the
	// site by (alarms.channels.push.subject). Never defaulted.
	Subject string
	// AllowHosts narrows the push services the controller sends to (and
	// may name a private one, a site's own relay). Empty: any public host.
	AllowHosts []string
}

const (
	pushSaveWait  = 10 * time.Second
	pushBodyMax   = 8 << 10
	pushSweepEach = 30 * time.Second
	// pushOIDCWindow is how long an OIDC person's subscription stands
	// without a sign-in here to refresh their groups.
	pushOIDCWindow = 14 * 24 * time.Hour
)

// usersReadable reports whether the users file can be trusted as it is
// now: a file that cannot be read is an empty one to stands, which would
// end every subscription. Nothing is revoked or pruned on its word.
func (p *pushService) usersReadable() bool { return p.s.users != nil && p.s.users.Err() == nil }

type pushService struct {
	s      *Server
	reg    *webpush.Registry
	sender *webpush.Sender
	policy webpush.Policy

	heldMu sync.Mutex
	held   map[string]time.Time // rows a mass revocation is holding, by rowKey, since when

	// add serializes changes to the registry with their durable save, so a
	// failed save can put the rows back as they were.
	add sync.Mutex
}

func newPushService(s *Server, o *PushOptions) (*pushService, error) {
	reg, err := webpush.NewRegistry(o.Key)
	if err != nil {
		return nil, err
	}
	pol := webpush.Policy{AllowHosts: o.AllowHosts}
	return &pushService{s: s, reg: reg, policy: pol,
		sender: &webpush.Sender{Key: o.Key, Subject: o.Subject, Policy: pol}}, nil
}

// RetainedPush and RestorePush make the service the runtime's PushKeeper.
func (p *pushService) RetainedPush() *retain.PushState {
	rows := p.reg.Rows()
	if len(rows) == 0 {
		return nil
	}
	st := &retain.PushState{Rows: make([]retain.PushRow, len(rows))}
	for i, r := range rows {
		st.Rows[i] = retain.PushRow(r)
	}
	return st
}

func (p *pushService) RestorePush(st *retain.PushState) {
	var rows []webpush.Row
	if st != nil {
		rows = make([]webpush.Row, len(st.Rows))
		for i, r := range st.Rows {
			rows[i] = webpush.Row(r)
		}
	}
	p.reg.Load(rows)
}

// pushPrincipal is who a push request acts as: a person signed in at the
// HMI (a session; a personal access token is a credential for scripts, not
// a phone), with what that sign-in was proved by, as the session recorded
// it, and the groups their grants came from.
func (s *Server) pushPrincipal(r *http.Request) (pr auth.Principal, cred string, code int, msg string) {
	a := s.actorOf(r)
	if a.session != nil {
		switch a.pr.Kind {
		case auth.KindUser:
			return a.pr, a.session.cred, 0, ""
		case auth.KindOIDC:
			return a.pr, oidcCred(a.session.issuer, a.session.sub), 0, ""
		}
	}
	if a.pr.Kind == auth.KindUser || a.pr.Kind == auth.KindOIDC {
		return a.pr, "", http.StatusForbidden, "push pages a phone signed in at the HMI: subscribe from the HMI's own page (a personal access token is for scripts and has no phone)"
	}
	return a.pr, "", http.StatusForbidden, "push pages a person: sign in as a user (naut users add, an auth: block that grants them a role) or through OIDC; a service token, NAUTILUS_TOKEN or an anonymous request has no phone to page"
}

func oidcCred(issuer, sub string) string { return credPrint(issuer + "\x00" + sub) }

// stands reports whether the person a row belongs to still proves what
// they proved when it was made: sessionStands's rule, with no session.
func (p *pushService) stands(r webpush.Row) bool {
	s := p.s
	if s.policy == nil || s.users == nil {
		return false
	}
	name, oidc := strings.CutPrefix(r.Principal, "oidc:")
	if !oidc {
		name = strings.TrimPrefix(r.Principal, "user:")
		u, known := s.users.Current().Users[name]
		return known && !u.Disabled && credPrint(u.Password) == r.Cred && s.policy.HasUser(name)
	}
	if s.oidc == nil || !s.policy.OIDC() {
		return false
	}
	pin, ok, err := s.users.Pin(name)
	if err != nil || !ok || oidcCred(pin.Issuer, pin.Subject) != r.Cred {
		return false
	}
	// An OIDC person's groups are those of their last sign-in here: a row
	// not refreshed by one within pushOIDCWindow no longer stands (a person
	// removed from a group at the provider is not paged for ever).
	if s.sessions.clock().Sub(r.Seen) > pushOIDCWindow {
		return false
	}
	return s.policy.HasGrants(auth.OIDC(name, r.Groups))
}

// pushEntry journals what the controller did to a subscription on its own
// (a takeover of an endpoint, an eviction, a revocation).
func (p *pushService) pushEntry(action string, r webpush.Row, why string, extra map[string]any) {
	d := map[string]any{"principal": r.Principal, "host": r.Host, "reason": why}
	for k, v := range extra {
		d[k] = v
	}
	p.s.audit.record(audit.Entry{
		Source: audit.SourceSystem, User: "system", Action: action,
		Target: "push:" + r.ID, Detail: d, Result: audit.ResultOK,
	})
}

// save makes the registry durable before an API answers.
func (p *pushService) save(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, pushSaveWait)
	defer cancel()
	err := p.s.rt.SaveRetained(ctx)
	if err == nil || errors.Is(err, runtime.ErrNoRetainStore) {
		return nil
	}
	return err
}

// pushHoldFor is how long a mass revocation is held before it is carried
// out: a variable for tests.
var pushHoldFor = 10 * time.Minute

func rowKey(r webpush.Row) string { return r.Principal + "\x00" + r.ID + "\x00" + r.Sealed }

// sweep ends the subscriptions whose person no longer stands, except a mass
// of them at once. A users file that reads fine but is wrong (a bad deploy
// of users.json, a pair member with separately salted passwords) makes
// many people fail at the same moment, and deleting their phones on its
// word would silence the pager until each re-subscribes. So a pass that
// would revoke more than half the rows, or every person's, across two or
// more people, HOLDS them instead: kept, not sent, a condition raised
// (PushHeld; the system alarm), and carried out only when a later sweep,
// pushHoldFor on, still finds that row failing. A row that stands again is
// released. One person's change, or a few among many, ends at once.
func (p *pushService) sweep(ctx context.Context) {
	if !p.s.rt.Leading() || !p.usersReadable() {
		return
	}
	p.add.Lock()
	defer p.add.Unlock()
	now := p.s.sessions.clock()
	rows := p.reg.Rows()
	var failing []webpush.Row
	people, failingPeople := map[string]bool{}, map[string]bool{}
	for _, r := range rows {
		people[r.Principal] = true
		if !p.stands(r) {
			failing = append(failing, r)
			failingPeople[r.Principal] = true
		}
	}
	mass := len(failingPeople) >= 2 && (len(failing)*2 > len(rows) || len(failingPeople) == len(people))

	p.heldMu.Lock()
	// A held row that stands again is released; the rest stay as they were first held.
	still := map[string]time.Time{}
	for _, r := range failing {
		if t, ok := p.held[rowKey(r)]; ok {
			still[rowKey(r)] = t
		}
	}
	var end []webpush.Row
	newlyHeld := 0
	for _, r := range failing {
		k := rowKey(r)
		t, wasHeld := still[k]
		switch {
		case wasHeld && now.Sub(t) >= pushHoldFor:
			end = append(end, r) // a later sweep agrees, row by row
		case wasHeld || mass:
			if !wasHeld {
				still[k] = now
				newlyHeld++
			}
		default:
			end = append(end, r)
		}
	}
	p.held = still
	for _, r := range end {
		delete(p.held, rowKey(r))
	}
	held := len(p.held)
	p.heldMu.Unlock()

	if newlyHeld > 0 {
		p.s.audit.record(audit.Entry{
			Source: audit.SourceSystem, User: "system", Action: "push.hold", Target: "push",
			Detail: map[string]any{"rows": newlyHeld, "people": len(failingPeople), "of": len(rows),
				"reason": "a pass would end more than half the subscriptions at once: held, not sent, until a later sweep agrees; check users.json (and that a pair carries the same one: the pair's users check)"},
			Result: audit.ResultOK,
		})
		slog.Warn("push: a mass of subscriptions stopped standing at once; holding them, not deleting (check users.json and the pair's users check)", "held", held, "of", len(rows))
	}
	if len(end) == 0 {
		return
	}
	p.reg.RemoveRows(end)
	for _, r := range end {
		p.pushEntry("push.unsubscribe", r, "the person no longer proves what they did when they subscribed (password changed, user removed or disabled, role revoked, OIDC account unpinned)", nil)
	}
	if err := p.save(ctx); err != nil {
		slog.Warn("push: revoked subscriptions could not be saved; they are ended here and will be again at the next save", "count", len(end), "error", err)
	}
}

// PushHeld is how many subscriptions the standing sweep is holding rather
// than deleting (see sweep): the condition's input.
func (s *Server) PushHeld() int {
	p := s.push
	if p == nil {
		return 0
	}
	p.heldMu.Lock()
	defer p.heldMu.Unlock()
	return len(p.held)
}

func (p *pushService) run(ctx context.Context) {
	t := time.NewTicker(pushSweepEach)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.sweep(ctx)
		}
	}
}

func (s *Server) pushOff(w http.ResponseWriter) bool {
	if s.push != nil {
		return false
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "push is not set up on this controller: add alarms.channels.push to the manifest (naut alarms contacts shows the secrets file it keeps its key in)"})
	return true
}

type pushSubscribeBody struct {
	Endpoint string `json:"endpoint"`
	// ExpirationTime is in a browser's PushSubscription.toJSON() (null, or
	// a time the push service may end it by): accepted and not used.
	ExpirationTime any `json:"expirationTime"`
	Keys           struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

func readPushBody(r *http.Request, v any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, pushBodyMax+1))
	if err != nil || len(b) > pushBodyMax {
		return errors.New("body unreadable or over 8 KiB")
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errors.New("body is not the browser's PushSubscription JSON ({endpoint, keys:{p256dh, auth}})")
	}
	return nil
}

// pushSubscribeTarget names the subscription in the journal: its id and
// push service host, never the endpoint or the keys.
func pushTarget(r *http.Request, body []byte) auditTarget {
	var b struct {
		Endpoint string `json:"endpoint"`
		ID       string `json:"id"`
	}
	// The first JSON value, as the handlers read it: trailing bytes do not
	// change what the entry names.
	if json.NewDecoder(bytes.NewReader(body)).Decode(&b) != nil {
		return auditTarget{}
	}
	id := b.ID
	if b.Endpoint != "" {
		id = webpush.EndpointID(b.Endpoint)
	}
	if id == "" || len(id) > 64 {
		return auditTarget{}
	}
	return auditTarget{target: "push:" + id}
}

func (s *Server) handlePushKey(w http.ResponseWriter, r *http.Request) {
	if s.pushOff(w) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"publicKey": s.pushKey})
}

func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	if s.pushOff(w) {
		return
	}
	if code, msg := s.authorizeWrite(r); code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	pr, cred, code, msg := s.pushPrincipal(r)
	if code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	if !s.rt.Leading() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "this replica does not lead: subscribe on the leader"})
		return
	}
	var b pushSubscribeBody
	if err := readPushBody(r, &b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	sub := webpush.Subscription{Endpoint: b.Endpoint, P256dh: b.Keys.P256dh, Auth: b.Keys.Auth}
	host, err := s.push.policy.CheckEndpoint(sub.Endpoint)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the push endpoint is refused: it must be an https URL on a public host (and on one of alarms.channels.push.allow-hosts when that list is set)"})
		return
	}
	sub, err = webpush.Normalize(sub)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	p := s.push
	p.add.Lock()
	defer p.add.Unlock()
	row, ch, err := p.reg.Add(pr.String(), pr.Groups(), cred, host, sub, s.sessions.clock())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the subscription could not be kept"})
		return
	}
	if err := p.save(r.Context()); err != nil {
		p.reg.Undo(row, ch) // not Load(before): a takeover's load may have landed meanwhile
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the subscription could not be saved to the retain store, so it was not kept: try again"})
		return
	}
	for _, t := range ch.Taken {
		p.pushEntry("push.unsubscribe", t, "this browser subscribed as "+pr.String(), nil)
	}
	for _, e := range ch.Evicted {
		p.pushEntry("push.unsubscribe", e, "over "+strconv.Itoa(webpush.MaxPerPrincipal)+" devices: the oldest ends", nil)
	}
	writeJSON(w, http.StatusCreated, pushView(row))
}

func (s *Server) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	if s.pushOff(w) {
		return
	}
	if code, msg := s.authorizeWrite(r); code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	pr, _, code, msg := s.pushPrincipal(r)
	if code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	var b struct {
		Endpoint string `json:"endpoint"`
		ID       string `json:"id"`
	}
	if err := readPushBody(r, &b); err != nil || (b.ID == "") == (b.Endpoint == "") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name the subscription by its id, or by its endpoint (one of the two)"})
		return
	}
	id := b.ID
	if b.Endpoint != "" {
		id = webpush.EndpointID(b.Endpoint)
	}
	p := s.push
	p.add.Lock()
	defer p.add.Unlock()
	removed, ok := p.reg.Remove(pr.String(), id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "you have no such subscription (GET /api/push/subscriptions lists yours)"})
		return
	}
	if err := p.save(r.Context()); err != nil {
		p.reg.Restore(removed)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the change could not be saved to the retain store, so it was not made: try again"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type pushRowView struct {
	ID   string    `json:"id"`
	Host string    `json:"host"`
	At   time.Time `json:"at"`
}

func pushView(r webpush.Row) pushRowView { return pushRowView{r.ID, r.Host, r.At} }

func (s *Server) handlePushList(w http.ResponseWriter, r *http.Request) {
	if s.pushOff(w) {
		return
	}
	pr, _, code, msg := s.pushPrincipal(r)
	if code != 0 {
		if pr.Kind == auth.KindAnonymous {
			code, msg = http.StatusUnauthorized, msg+signInHint
		}
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	out := []pushRowView{}
	for _, row := range s.push.reg.Of(pr.String()) {
		out = append(out, pushView(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscriptions": out})
}

// PushHealth is what GET /api/push/health says: counts, never a row.
type PushHealth struct {
	KeyFingerprint string `json:"keyFingerprint"`
	Subscriptions  int    `json:"subscriptions"`
	People         int    `json:"people"`
	// Sealed are rows sealed under another push key: kept, not sent. A
	// pair whose members hold different keys shows them on the standby.
	Sealed int `json:"sealed"`
	// Held are subscriptions a mass revocation is holding instead of
	// deleting (check users.json and the pair's users check).
	Held int `json:"held"`
}

func (s *Server) pushHealth() PushHealth {
	rows := s.push.reg.Rows()
	people := map[string]bool{}
	for _, r := range rows {
		people[r.Principal] = true
	}
	return PushHealth{KeyFingerprint: s.push.reg.KeyFingerprint(), Subscriptions: len(rows), People: len(people), Sealed: s.push.reg.Sealed(), Held: s.PushHeld()}
}

func (s *Server) handlePushHealth(w http.ResponseWriter, r *http.Request) {
	if s.pushOff(w) {
		return
	}
	if code, msg := s.authorize(r, PermConfig, ""); code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	writeJSON(w, http.StatusOK, s.pushHealth())
}

// PushMessage is one page for one person: Target is what the person must
// be allowed to read (an alarm's tag), Payload what the phone's worker
// shows, Topic what replaces an earlier page of the same alarm.
type PushMessage struct {
	Target  string
	Topic   string
	Payload []byte
	Urgency string
	TTL     time.Duration
}

// PushResult says what a page did, in counts: never an endpoint or a key.
type PushResult struct {
	Sent    int // pushes the push service accepted
	Failed  int // pushes it refused or that did not get there
	Pruned  int // subscriptions the push service said are gone
	Held    int // subscriptions whose person does not stand now: not sent, left to the sweep
	Skipped int // subscriptions whose person may not read Target
	Sealed  int // subscriptions under another push key: kept, not sent
	// NoSubscription is true when the person has none at all: reported in
	// the escalation like a missing address.
	NoSubscription bool
	// Class is the last failure's class, "" when none failed.
	Class string
}

// ErrPushOff and ErrPushNotLeading are the refusals PushSend gives.
var (
	ErrPushOff        = errors.New("push is not set up on this controller")
	ErrPushNotLeading = errors.New("this replica does not lead: only the leader pages")
	// ErrPushUsers: the users file cannot be read, so who still stands is unknown.
	ErrPushUsers = errors.New("the users file cannot be read, so nobody is paged until it can")
)

// PushSend pages principal ("ann", "user:ann", "oidc:ann") on every phone they
// subscribed, and only on a leader. It is the seam the alarm channel
// calls: the person must still stand and still be allowed to read target,
// both checked now, not when they subscribed.
func (s *Server) PushSend(ctx context.Context, principal string, m PushMessage) (PushResult, error) {
	p := s.push
	if p == nil {
		return PushResult{}, ErrPushOff
	}
	if !s.rt.Leading() {
		return PushResult{}, ErrPushNotLeading
	}
	if !p.usersReadable() {
		return PushResult{}, ErrPushUsers // nothing is judged, or pruned, on an unreadable users file
	}
	// A level names a person as the manifest does: a local user's bare
	// name (alice, any case), or oidc:<name>.
	if n, ok := strings.CutPrefix(principal, "oidc:"); ok {
		principal = "oidc:" + strings.ToLower(n)
	} else {
		principal = "user:" + strings.ToLower(strings.TrimPrefix(principal, "user:"))
	}
	rows := p.reg.Of(principal)
	res := PushResult{NoSubscription: len(rows) == 0}
	var drop []webpush.Row
	for _, row := range rows {
		if !p.stands(row) {
			// Not paged, and not deleted here: ending it is the sweep's, which
			// holds a mass of them instead (a wrong users file).
			res.Held++
			continue
		}
		if !s.policy.Can(rowPrincipal(row), auth.Read, m.Target).Allowed {
			res.Skipped++
			continue
		}
		sub, err := p.reg.Open(row)
		if err != nil {
			res.Sealed++
			continue
		}
		err = p.sender.Send(ctx, sub, m.Payload, webpush.Options{TTL: m.TTL, Urgency: m.Urgency, Topic: m.Topic})
		var se *webpush.SendError
		switch {
		case err == nil:
			res.Sent++
		case errors.As(err, &se) && se.Gone:
			res.Pruned++
			drop = append(drop, row)
			p.pushEntry("push.unsubscribe", row, "the push service says the subscription is gone (HTTP "+strconv.Itoa(se.Status)+")", nil)
		default:
			res.Failed++
			res.Class = pushClass(err)
		}
	}
	if len(drop) > 0 {
		p.add.Lock()
		p.reg.RemoveRows(drop) // exactly these: a row re-made since (same endpoint) is not theirs
		if err := p.save(ctx); err != nil {
			slog.Warn("push: pruned subscriptions could not be saved; they are ended here and will be again at the next save", "count", len(drop), "error", err)
		}
		p.add.Unlock()
	}
	return res, nil
}

func rowPrincipal(r webpush.Row) auth.Principal {
	if n, ok := strings.CutPrefix(r.Principal, "oidc:"); ok {
		return auth.OIDC(n, r.Groups)
	}
	return auth.User(strings.TrimPrefix(r.Principal, "user:"))
}

// pushClass names a failed push the way the escalation names a failed
// send: a class and a code, never the service's text or the endpoint.
func pushClass(err error) string {
	var se *webpush.SendError
	if errors.As(err, &se) {
		switch {
		case se.Status == 401 || se.Status == 403:
			return "auth"
		case se.Status == 429 || se.Status >= 500:
			return "provider-error"
		case se.Status != 0:
			return "rejected"
		case se.Why == "timed out":
			return "timeout"
		}
		return "unreachable"
	}
	return "error"
}

// pushOIDCSignIn is told of an OIDC sign-in: what the person proved and the
// groups they hold now are recorded on their subscriptions (re-sealed), so
// a phone is paged for as long as they keep signing in here, and with the
// groups they have, not the ones they had.
func (s *Server) pushOIDCSignIn(se session) {
	p := s.push
	if p == nil || !s.rt.Leading() {
		return
	}
	p.add.Lock()
	n := p.reg.Rebind(se.principal().String(), oidcCred(se.issuer, se.sub), se.principal().Groups(), s.sessions.clock())
	p.add.Unlock()
	if n > 0 {
		go func() {
			if err := p.save(context.Background()); err != nil {
				slog.Warn("push: a sign-in's refresh of subscriptions could not be saved; it is kept here", "error", err)
			}
		}()
	}
}
