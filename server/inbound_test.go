package server

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/joyautomation/nautilus/oplog"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/alarm/acklink"
	"github.com/joyautomation/nautilus/alarm/channel"
	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/leader"
)

// Inbound acknowledgements (#715 PR 3b-2): an SMS reply's code, a call's
// keypress, posted by the provider, signed.

const (
	inboundBase = "https://hmi.example:8443"
	smsToken    = "tok-sms"
	voiceToken  = "tok-voice"
	alicePhone  = "+15550102"
	bobPhone    = "+15550103"
	sharedPhone = "+15550199"
)

// twilioSign is the provider's signature (alarm/channel, checked there
// against the provider's published vectors).
func twilioSign(token, u string, form url.Values) string { return channel.Signature(token, u, form) }

// testInbound is a provider setup: alice and bob have numbers, two people
// share one; the tokens are smsToken and voiceToken.
func testInbound() *Inbound {
	contacts := map[string]map[string]string{
		"sms":   {"alice": alicePhone, "bob": bobPhone, "akhan": "+15550110", "jsmith": "+15550111", "x1": sharedPhone, "x2": sharedPhone},
		"voice": {"alice": alicePhone, "bob": bobPhone, "akhan": "+15550110", "jsmith": "+15550111"},
	}
	return &Inbound{
		URL: inboundBase, SMS: true, Voice: true,
		Verify: func(kind, u string, form url.Values, sig string) bool {
			tok := map[string]string{"sms": smsToken, "voice": voiceToken}[kind]
			return channel.CheckSignature(tok, u, form, sig)
		},
		Callers: func(kind, number string) []string {
			var out []string
			for p, n := range contacts[kind] {
				if n == number {
					out = append(out, p)
				}
			}
			slices.Sort(out)
			return out
		},
	}
}

// withInbound gives s ack links and inbound replies.
func withInbound(t *testing.T, s *Server) {
	t.Helper()
	withLinks(t, s)
	s.inbound = testInbound()
}

func inboundServer(t *testing.T) *Server {
	t.Helper()
	s := auditServer(t, Options{})
	withInbound(t, s)
	return s
}

// signed is a request modifier: the form as the body, signed by the
// provider under token at the configured URL and path.
func signed(token, path string, form url.Values) func(*http.Request) {
	return func(r *http.Request) {
		body := form.Encode()
		r.Body = io.NopCloser(strings.NewReader(body))
		r.ContentLength = int64(len(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("X-Twilio-Signature", twilioSign(token, inboundBase+path, form))
	}
}

// reply posts an SMS reply body from number.
func reply(t *testing.T, s *Server, from, body string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"From": {from}, "To": {"+15550100"}, "Body": {body}, "MessageSid": {"SM0001"}}
	return call(t, s, "POST", InboundSMSPath, "", signed(smsToken, InboundSMSPath, form))
}

// press posts a keypress on the call whose action URL is u, callee to.
func press(t *testing.T, s *Server, u, to, digits string) *httptest.ResponseRecorder {
	t.Helper()
	path := strings.TrimPrefix(u, inboundBase)
	form := url.Values{"To": {to}, "From": {"+15550101"}, "Digits": {digits}, "CallSid": {"CA0001"}}
	return call(t, s, "POST", path, "", signed(voiceToken, path, form))
}

func codeFor(t *testing.T, s *Server, id, principal string) string {
	t.Helper()
	c := s.AckCode(id, occOf(t, s.alarms, id).Occurrence, principal)
	if len(c) != acklink.CodeDigits {
		t.Fatalf("code %q", c)
	}
	return c
}

// noSecrets fails when the entry names the number, the reply's text or
// the code.
func noSecrets(t *testing.T, e audit.Entry, secrets ...string) {
	t.Helper()
	b, _ := json.Marshal(e)
	for _, x := range append(secrets, alicePhone, bobPhone, sharedPhone) {
		if x != "" && strings.Contains(string(b), x) {
			t.Fatalf("the audit entry holds %q: %s", x, b)
		}
	}
}

// An SMS reply with the page's code acknowledges that occurrence as the
// person the number belongs to, through the same path as a link: the
// journal says via: sms and the code's ID, never the number, the text or
// the code; the provider hears TwiML at 200.
func TestInboundSMSAcks(t *testing.T) {
	s := inboundServer(t)
	code := codeFor(t, s, "HiTemp", "alice")
	occ := occOf(t, s.alarms, "HiTemp").Occurrence
	rec := reply(t, s, alicePhone, "ack "+code[:4]+" "+code[4:])
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<Message>Acknowledged High temperature as alice.") ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/xml") {
		t.Fatalf("reply: %d %s", rec.Code, rec.Body)
	}
	if h := rec.Header(); h.Get(providerAnswerHeader) != "" || h.Get(linkReasonHeader) != "" {
		t.Fatalf("seam headers reached the provider: %v", h)
	}
	if r := occOf(t, s.alarms, "HiTemp"); r.State != alarm.AckActive || r.AckBy != "alice" {
		t.Fatalf("after the reply: %s by %q", r.State, r.AckBy)
	}
	e := lastAudit(t, s)
	if e.Action != "alarm.ack" || e.User != "user:alice" || e.Result != audit.ResultOK || e.Target != "HiTemp" {
		t.Fatalf("audit %+v", e)
	}
	d := e.Detail
	if d["via"] != "sms" || d["channel"] != "sms" || d["occurrence"] != occ || !strings.HasPrefix(fmt.Sprint(d["link"]), acklink.CodeIDPrefix) {
		t.Fatalf("detail %v", d)
	}
	if _, ok := d["level"]; ok {
		t.Fatalf("a code does not say its level: %v", d)
	}
	noSecrets(t, e, code)

	// The same code again: the occurrence waits on nobody now.
	rec = reply(t, s, alicePhone, "ACK "+code)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "matches no alarm waiting on you") {
		t.Fatalf("second reply: %d %s", rec.Code, rec.Body)
	}
	if e := lastAudit(t, s); e.Result == audit.ResultOK {
		t.Fatalf("second reply journalled %+v", e)
	}
}

// A code is spent once even where its occurrence waits again (a pair's
// view dropped the ack at a takeover): the controller's record refuses it.
func TestInboundCodeIsSpentOnce(t *testing.T) {
	s := inboundServer(t)
	code := codeFor(t, s, "HiTemp", "alice")
	c, err := s.ackLinks.MatchCode(code, "alice", s.standing("alice"))
	if err != nil {
		t.Fatal(err)
	}
	s.links.spend(c.ID, c.Expires, time.Now())
	rec := reply(t, s, alicePhone, "ACK "+code)
	if !strings.Contains(rec.Body.String(), "This code was already used") || state(s.alarms, "HiTemp") != alarm.UnackActive {
		t.Fatalf("spent code: %s", rec.Body)
	}
	if e := lastAudit(t, s); e.Result == audit.ResultOK || e.Detail["status"] != http.StatusConflict {
		t.Fatalf("journalled %+v", e)
	}
}

// Every refusal of a signed request is 200 with TwiML on the wire (the
// provider sends no reply and plays the caller an error otherwise) and
// refused in the journal with its own status and reason. A sender nobody
// here has gets nothing back; the number is never journalled.
func TestInboundSMSRefusals(t *testing.T) {
	s := inboundServer(t)
	code := codeFor(t, s, "HiTemp", "alice")
	for _, tc := range []struct {
		name, from, body string
		status           int
		reply, reason    string
	}{
		{"stranger", "+15550999", "ACK " + code, http.StatusForbidden, "", "not an SMS contact"},
		{"shared number", sharedPhone, "ACK " + code, http.StatusForbidden, "more than one person", "more than one person"},
		{"no code", alicePhone, "yes please", http.StatusBadRequest, "Reply ACK and the 8-digit code", "holds no code"},
		{"bob with alice's code", bobPhone, "ACK " + code, http.StatusForbidden, "matches no alarm waiting on you", "matches no alarm"},
	} {
		rec := reply(t, s, tc.from, tc.body)
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Body.String(), "<?xml") {
			t.Fatalf("%s: wire %d %s", tc.name, rec.Code, rec.Body)
		}
		if tc.reply == "" && strings.Contains(rec.Body.String(), "<Message>") {
			t.Fatalf("%s: a stranger got a reply: %s", tc.name, rec.Body)
		}
		if tc.reply != "" && !strings.Contains(rec.Body.String(), tc.reply) {
			t.Fatalf("%s: reply %s", tc.name, rec.Body)
		}
		e := lastAudit(t, s)
		if e.Result == audit.ResultOK || e.Detail["status"] != tc.status || !strings.Contains(fmt.Sprint(e.Detail["reason"]), tc.reason) || e.Detail["via"] != "sms" {
			t.Fatalf("%s: journalled %+v", tc.name, e)
		}
		if _, marked := e.Detail["sender"]; marked != (e.User != "anonymous") {
			t.Fatalf("%s: a person named only by the number is not marked so: %+v", tc.name, e)
		}
		noSecrets(t, e, code, tc.body)
	}
	if st := state(s.alarms, "HiTemp"); st != alarm.UnackActive {
		t.Fatalf("acked %s", st)
	}
}

// The signature is checked over the configured URL, never the request's
// Host: a request signed for another host, unsigned, or signed under
// another token is answered with its status (no TwiML), journalled, and
// changes nothing.
func TestInboundSignatureIsOverTheConfiguredURL(t *testing.T) {
	s := inboundServer(t)
	code := codeFor(t, s, "HiTemp", "alice")
	form := url.Values{"From": {alicePhone}, "Body": {"ACK " + code}}
	for name, mod := range map[string]func(*http.Request){
		"signed for the Host": func(r *http.Request) {
			signed(smsToken, InboundSMSPath, form)(r)
			r.Header.Set("X-Twilio-Signature", twilioSign(smsToken, "http://localhost"+InboundSMSPath, form))
		},
		"unsigned": func(r *http.Request) {
			signed(smsToken, InboundSMSPath, form)(r)
			r.Header.Del("X-Twilio-Signature")
		},
		"voice token": func(r *http.Request) {
			signed(voiceToken, InboundSMSPath, form)(r)
		},
		"body changed": func(r *http.Request) {
			signed(smsToken, InboundSMSPath, form)(r)
			b := url.Values{"From": {bobPhone}, "Body": {"ACK " + code}}.Encode()
			r.Body, r.ContentLength = io.NopCloser(strings.NewReader(b)), int64(len(b))
		},
	} {
		rec := call(t, s, "POST", InboundSMSPath, "", mod)
		if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "<Response") {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
		if e := lastAudit(t, s); e.Result == audit.ResultOK || e.User != "anonymous" || !strings.Contains(fmt.Sprint(e.Detail["reason"]), "signature") {
			t.Fatalf("%s: journalled %+v", name, e)
		}
	}
	if st := state(s.alarms, "HiTemp"); st != alarm.UnackActive {
		t.Fatalf("acked %s", st)
	}
	// Not a form at all.
	rec := call(t, s, "POST", InboundSMSPath, `{"From":"x"}`, func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("json: %d", rec.Code)
	}
}

// A person who sends InboundTries wrong codes is refused, the right code
// too, until the hour has passed; a stranger's tries count against nobody.
func TestInboundWrongCodesAreLimited(t *testing.T) {
	s := inboundServer(t)
	code := codeFor(t, s, "HiTemp", "alice")
	wrong := fmt.Sprintf("%08d", (atoi(code)+1)%100000000)
	// One short of the limit, the right code still acknowledges.
	for range InboundTries - 1 {
		reply(t, s, alicePhone, "ACK "+wrong)
	}
	if rec := reply(t, s, alicePhone, "ACK "+code); !strings.Contains(rec.Body.String(), "Acknowledged") {
		t.Fatalf("after %d wrong codes the right one: %s", InboundTries-1, rec.Body)
	}

	s = inboundServer(t)
	code = codeFor(t, s, "HiTemp", "alice")
	for range InboundTries {
		reply(t, s, alicePhone, "ACK "+wrong)
	}
	reply(t, s, alicePhone, "ACK "+code)
	if state(s.alarms, "HiTemp") != alarm.UnackActive {
		t.Fatalf("after %d wrong codes the right one acknowledged", InboundTries)
	}
	// (Those refusals past five a minute from one address for one person
	// are folded into the audit trail's summary entry, refuseFull.)
	// bob is not alice: his code still works.
	if rec := reply(t, s, bobPhone, "ACK "+codeFor(t, s, "HiTemp", "bob")); !strings.Contains(rec.Body.String(), "Acknowledged") {
		t.Fatalf("bob: %s", rec.Body)
	}

	// The controller-wide budget: InboundTriesAll wrong codes from anyone,
	// then nobody's code is taken for a day.
	s = inboundServer(t)
	people := []string{"alice", "bob", "akhan", "jsmith"}
	phones := map[string]string{"alice": alicePhone, "bob": bobPhone, "akhan": "+15550110", "jsmith": "+15550111"}
	for i := range InboundTriesAll {
		reply(t, s, phones[people[i%len(people)]], "ACK "+wrong)
	}
	for _, p := range people {
		if !s.inboundTries.blocked(p, InboundTries, time.Now()) {
			reply(t, s, phones[p], "ACK "+codeFor(t, s, "HiTemp", p))
			if state(s.alarms, "HiTemp") != alarm.UnackActive {
				t.Fatalf("%s acknowledged past the controller's budget", p)
			}
		}
	}
}

// A person is texted a refusal at most inboundReplies times an hour; past
// that, nothing (a forged sender cannot make the controller text them
// without limit; an auto-responder cannot loop). An ack is always answered.
func TestInboundRepliesAreCapped(t *testing.T) {
	s := inboundServer(t)
	for i := range inboundReplies + 2 {
		rec := reply(t, s, alicePhone, "I am driving")
		if got := strings.Contains(rec.Body.String(), "<Message>"); got != (i < inboundReplies) {
			t.Fatalf("refusal %d: %s", i+1, rec.Body)
		}
	}
	if rec := reply(t, s, alicePhone, "ACK "+codeFor(t, s, "HiTemp", "alice")); !strings.Contains(rec.Body.String(), "<Message>Acknowledged") {
		t.Fatalf("the ack past the cap: %s", rec.Body)
	}
}

// A code is tried only against occurrences whose escalation can page the
// person by SMS: a code for an alarm that never pages them matches nothing.
func TestInboundCodesOnlyForAlarmsThatPageThePerson(t *testing.T) {
	s := inboundServer(t)
	s.inbound.MayPage = func(rec alarm.Record, p, kind string) bool { return kind == "sms" && p == "alice" }
	if rec := reply(t, s, bobPhone, "ACK "+codeFor(t, s, "HiTemp", "bob")); !strings.Contains(rec.Body.String(), "matches no alarm") {
		t.Fatalf("bob, never paged: %s", rec.Body)
	}
	if rec := reply(t, s, alicePhone, "ACK "+codeFor(t, s, "HiTemp", "alice")); !strings.Contains(rec.Body.String(), "Acknowledged") {
		t.Fatalf("alice: %s", rec.Body)
	}
}

// An ack the pair's log took but did not confirm in time may still take
// effect: journalled unknown, never refused, and the person is told to
// check the HMI.
func TestInboundUnknownOutcome(t *testing.T) {
	var lead atomic.Bool
	lead.Store(true)
	s, eng, _, _ := loggedAlarms(t, map[string]any{"HiTempAlm": true}, &lead)
	eng.Evaluate()
	withInbound(t, s)
	f := func(context.Context, oplog.Entry) (oplog.Entry, error) { return oplog.Entry{}, oplog.ErrUnconfirmed }
	alarmProposeSeam.Store(&f)
	t.Cleanup(func() { alarmProposeSeam.Store(nil) })
	rec := reply(t, s, alicePhone, "ACK "+codeFor(t, s, "HiTemp", "alice"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "may still take effect") || strings.Contains(rec.Body.String(), "Not acknowledged") {
		t.Fatalf("unknown outcome: %d %s", rec.Code, rec.Body)
	}
	if e := lastAudit(t, s); e.Result != audit.ResultUnknown || e.Detail["via"] != "sms" {
		t.Fatalf("audit %+v", e)
	}
}

// A manifest may spell a person "Alice"; the contacts file keys "alice".
// The code and the call token are minted over the contacts spelling.
func TestInboundPrincipalCase(t *testing.T) {
	s := inboundServer(t)
	occ := occOf(t, s.alarms, "HiTemp").Occurrence
	if rec := reply(t, s, alicePhone, "ACK "+s.AckCode("HiTemp", occ, "Alice")); !strings.Contains(rec.Body.String(), "Acknowledged High temperature as alice") {
		t.Fatalf("Alice's code: %s", rec.Body)
	}
	s = inboundServer(t)
	u := s.VoiceAckURL("HiTemp", occOf(t, s.alarms, "HiTemp").Occurrence, "user:ALICE", 1)
	if rec := press(t, s, u, alicePhone, "1"); !strings.Contains(rec.Body.String(), "<Say>Acknowledged.") {
		t.Fatalf("ALICE's call: %s", rec.Body)
	}
}

// An expired call token is refused (410 in the journal), and a keypress
// acts as the person only while they may: under auth, akhan acks, jsmith
// (fenced to another area) does not.
func TestInboundVoiceExpiryAndAuth(t *testing.T) {
	s := inboundServer(t)
	s.ackLinks.SetClock(func() time.Time { return time.Now().Add(-acklink.MaxTTL - time.Minute) })
	u := s.VoiceAckURL("HiTemp", occOf(t, s.alarms, "HiTemp").Occurrence, "alice", 1)
	s.ackLinks.SetClock(time.Now)
	if rec := press(t, s, u, alicePhone, "1"); !strings.Contains(rec.Body.String(), "Not acknowledged") || state(s.alarms, "HiTemp") != alarm.UnackActive {
		t.Fatalf("expired: %s", rec.Body)
	}
	if e := lastAudit(t, s); e.Detail["status"] != http.StatusGone {
		t.Fatalf("expired journalled %+v", e)
	}

	s, _, _ = patServer(t)
	withInbound(t, s)
	occ := occOf(t, s.alarms, "HiTemp").Occurrence
	if rec := press(t, s, s.VoiceAckURL("HiTemp", occ, "jsmith", 1), "+15550111", "1"); !strings.Contains(rec.Body.String(), "Not acknowledged") || state(s.alarms, "HiTemp") != alarm.UnackActive {
		t.Fatalf("jsmith: %s", rec.Body)
	}
	if rec := press(t, s, s.VoiceAckURL("HiTemp", occ, "akhan", 1), "+15550110", "1"); !strings.Contains(rec.Body.String(), "Acknowledged") {
		t.Fatalf("akhan: %s", rec.Body)
	}
}

// Neither the number, the text, the code nor the token reaches the alarm
// journal, the log entry or the process log.
func TestInboundSecretsStayOut(t *testing.T) {
	var lead atomic.Bool
	lead.Store(true)
	s, eng, _, got := loggedAlarms(t, map[string]any{"HiTempAlm": true}, &lead)
	eng.Evaluate()
	withInbound(t, s)
	logs := captureLog(t)
	code := codeFor(t, s, "HiTemp", "alice")
	reply(t, s, alicePhone, "ACK 00000000 please")
	reply(t, s, bobPhone, "ACK "+code)
	if rec := reply(t, s, alicePhone, "ACK "+code); !strings.Contains(rec.Body.String(), "Acknowledged") {
		t.Fatalf("reply: %s", rec.Body)
	}
	evs, _ := eng.Journal(time.Now().Add(-time.Hour), time.Now().Add(time.Hour), alarm.Filter{})
	ej, _ := json.Marshal(evs)
	oj, _ := json.Marshal(*got)
	for _, x := range []string{alicePhone, bobPhone, code, "please"} {
		for where, b := range map[string]string{"alarm journal": string(ej), "log entries": string(oj), "process log": logs.String()} {
			if strings.Contains(b, x) {
				t.Fatalf("%s holds %q", where, x)
			}
		}
	}
	for _, e := range s.audit.ring.Recent() {
		noSecrets(t, e, code, "please")
	}
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}

func TestTriesWindow(t *testing.T) {
	var tr tries
	t0 := time.Unix(1_800_000_000, 0)
	for i := range InboundTries {
		if tr.blocked("alice", InboundTries, t0.Add(time.Duration(i)*time.Minute)) {
			t.Fatalf("blocked after %d", i)
		}
		tr.fail("alice", t0.Add(time.Duration(i)*time.Minute))
	}
	if !tr.blocked("alice", InboundTries, t0.Add(10*time.Minute)) || tr.blocked("bob", InboundTries, t0.Add(10*time.Minute)) {
		t.Fatal("limit not per person")
	}
	// The first wrong code leaves the window an hour after it was sent.
	if tr.blocked("alice", InboundTries, t0.Add(inboundTriesWindow)) != false {
		t.Fatal("still blocked once the first wrong code is an hour old")
	}
	all := tries{window: inboundAllWindow}
	for range InboundTriesAll {
		all.fail(everyoneKey, t0)
	}
	if !all.blocked(everyoneKey, InboundTriesAll, t0.Add(23*time.Hour)) || all.blocked(everyoneKey, InboundTriesAll, t0.Add(inboundAllWindow)) {
		t.Fatal("the controller-wide budget is not a day")
	}
	tr.fail("bob", t0.Add(3*time.Hour)) // prunes alice
	if _, ok := tr.wrong["alice"]; ok {
		t.Fatal("old tries not pruned")
	}
}

// A call's keypress: the call's instructions name a URL carrying a voice
// link; the provider posts the key, signed. 1 from the callee acks as the
// person called; another key, another callee or the URL used as a link
// does nothing.
func TestInboundVoiceKeypress(t *testing.T) {
	s := inboundServer(t)
	occ := occOf(t, s.alarms, "HiTemp").Occurrence
	u := s.VoiceAckURL("HiTemp", occ, "alice", 2)
	if !strings.HasPrefix(u, inboundBase+InboundVoicePath) {
		t.Fatalf("voice URL %q", u)
	}
	tok := strings.TrimPrefix(u, inboundBase+InboundVoicePath)

	// The token as an ack link: refused, GET and POST.
	for _, m := range []string{"GET", "POST"} {
		rec := call(t, s, m, acklink.Path+tok, "")
		if rec.Code != http.StatusNotFound || state(s.alarms, "HiTemp") != alarm.UnackActive {
			t.Fatalf("voice token at %s %s: %d %s", m, acklink.Path, rec.Code, rec.Body)
		}
	}
	// Another key; another callee.
	if rec := press(t, s, u, alicePhone, "2"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<Say>Not acknowledged.</Say>") {
		t.Fatalf("key 2: %d %s", rec.Code, rec.Body)
	}
	if e := lastAudit(t, s); e.Result == audit.ResultOK || e.Detail["via"] != "voice" {
		t.Fatalf("key 2 journalled %+v", e)
	}
	if rec := press(t, s, u, bobPhone, "1"); !strings.Contains(rec.Body.String(), "Not acknowledged") {
		t.Fatalf("bob's phone: %s", rec.Body)
	}
	if st := state(s.alarms, "HiTemp"); st != alarm.UnackActive {
		t.Fatalf("acked %s", st)
	}
	rec := press(t, s, u, alicePhone, "1")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<Say>Acknowledged.") {
		t.Fatalf("press 1: %d %s", rec.Code, rec.Body)
	}
	if r := occOf(t, s.alarms, "HiTemp"); r.State != alarm.AckActive || r.AckBy != "alice" {
		t.Fatalf("after the keypress: %s by %q", r.State, r.AckBy)
	}
	e := lastAudit(t, s)
	if e.User != "user:alice" || e.Result != audit.ResultOK || e.Detail["via"] != "voice" || e.Detail["channel"] != "voice" || e.Detail["level"] != 2 {
		t.Fatalf("audit %+v", e)
	}
	noSecrets(t, e, tok)
	// Pressed again (the provider retried): spent.
	if rec := press(t, s, u, alicePhone, "1"); !strings.Contains(rec.Body.String(), "This call was already used") {
		t.Fatalf("again: %s", rec.Body)
	}
	// An email link is not a call's URL.
	mail, _ := s.ackLinks.Mint("HiTemp", occ, "alice", "email", 1, time.Hour)
	if rec := press(t, s, inboundBase+InboundVoicePath+mail, alicePhone, "1"); !strings.Contains(rec.Body.String(), "Not acknowledged") ||
		!strings.Contains(fmt.Sprint(lastAudit(t, s).Detail["reason"]), "not valid") {
		t.Fatalf("an email link as a call's URL: %s", rec.Body)
	}
}

// Under an auth: block a reply acts as the person only while they stand
// and hold ack for the alarm: jsmith (fenced to another area) is refused,
// akhan acknowledges.
func TestInboundActsOnlyAsAPersonWhoMayAck(t *testing.T) {
	s, _, _ := patServer(t)
	withInbound(t, s)
	rec := reply(t, s, "+15550111", "ACK "+codeFor(t, s, "HiTemp", "jsmith"))
	if !strings.Contains(rec.Body.String(), "Not acknowledged") || state(s.alarms, "HiTemp") != alarm.UnackActive {
		t.Fatalf("jsmith: %s", rec.Body)
	}
	if e := lastAudit(t, s); e.Result == audit.ResultOK || e.User != "user:jsmith" {
		t.Fatalf("jsmith journalled %+v", e)
	}
	rec = reply(t, s, "+15550110", "ACK "+codeFor(t, s, "HiTemp", "akhan"))
	if !strings.Contains(rec.Body.String(), "Acknowledged") || state(s.alarms, "HiTemp") != alarm.AckActive {
		t.Fatalf("akhan: %s", rec.Body)
	}
}

// Off (no alarms.oncall.inbound, or not this kind): 404, journalled.
func TestInboundOff(t *testing.T) {
	s := auditServer(t, Options{})
	withLinks(t, s)
	if c := s.AckCode("HiTemp", "o", "alice"); c != "" || s.VoiceAckURL("HiTemp", "o", "alice", 1) != "" {
		t.Fatal("codes or call URLs minted with inbound off")
	}
	if rec := reply(t, s, alicePhone, "ACK 12345678"); rec.Code != http.StatusNotFound {
		t.Fatalf("off: %d", rec.Code)
	}
	s.inbound = testInbound()
	s.inbound.SMS = false
	if rec := reply(t, s, alicePhone, "ACK 12345678"); rec.Code != http.StatusNotFound || s.AckCode("HiTemp", "o", "alice") != "" {
		t.Fatalf("sms off: %d", rec.Code)
	}
}

// On a pair the code's ID rides the ack's log entry, as a link's does.
func TestInboundIsSpentThroughTheLog(t *testing.T) {
	var lead atomic.Bool
	lead.Store(true)
	s, eng, al, got := loggedAlarms(t, map[string]any{"HiTempAlm": true}, &lead)
	eng.Evaluate()
	withInbound(t, s)
	if rec := reply(t, s, alicePhone, "ACK "+codeFor(t, s, "HiTemp", "alice")); !strings.Contains(rec.Body.String(), "Acknowledged") {
		t.Fatalf("reply: %s", rec.Body)
	}
	if len(*got) != 1 || !strings.HasPrefix((*got)[0].Link, acklink.CodeIDPrefix) || !al.LinkSpent((*got)[0].Link) {
		t.Fatalf("proposed %+v", *got)
	}
}

// A reply or a keypress that lands on a standby is forwarded to the
// leader, signature and all; with no leader known, the provider hears
// TwiML saying so.
func TestInboundOnAStandby(t *testing.T) {
	var seen []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = append(seen, r.URL.Path+" "+r.Header.Get("X-Twilio-Signature")+" "+string(b))
		w.Write([]byte("from-leader"))
	}))
	defer backend.Close()
	s := auditServer(t, Options{Cluster: fakeCluster{status: leader.Status{
		Mode: "quorum", Pod: "b", Leader: "a", LeaderAddr: standbyAddr(backend)}}})
	withInbound(t, s)
	rec := reply(t, s, alicePhone, "ACK 12345678")
	if !strings.Contains(rec.Body.String(), "from-leader") || len(seen) != 1 || !strings.HasPrefix(seen[0], InboundSMSPath+" ") ||
		strings.Contains(seen[0], "  ") || !strings.Contains(seen[0], "Body=ACK+12345678") {
		t.Fatalf("forwarded %v: %s", seen, rec.Body)
	}

	s = auditServer(t, Options{Cluster: fakeCluster{status: leader.Status{Mode: "quorum", Pod: "b"}}})
	withInbound(t, s)
	// No SMS goes back (the standby cannot tell a person from a forged
	// sender); a call hears why.
	rec = reply(t, s, alicePhone, "ACK 12345678")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "<Message>") || !strings.Contains(rec.Body.String(), "<Response>") {
		t.Fatalf("no leader: %d %s", rec.Code, rec.Body)
	}
	if e := lastAudit(t, s); e.Result == audit.ResultOK || e.Detail["status"] != http.StatusServiceUnavailable {
		t.Fatalf("no leader journalled %+v", e)
	}
	if rec := press(t, s, inboundBase+InboundVoicePath+"tok", alicePhone, "1"); !strings.Contains(rec.Body.String(), "<Say>Not acknowledged: no controller is leading") {
		t.Fatalf("no leader, voice: %s", rec.Body)
	}
	// Only the provider's POST to its routes is an inbound request: a GET
	// or a POST elsewhere under the prefix is not answered as one, and no
	// seam header leaks.
	for _, mp := range [][2]string{{"GET", InboundSMSPath}, {"POST", InboundPath + "x"}, {"POST", InboundVoicePath + "a/b"}} {
		rec = call(t, s, mp[0], mp[1], "")
		if rec.Header().Get(providerAnswerHeader) != "" || rec.Header().Get(linkReasonHeader) != "" || strings.Contains(rec.Body.String(), "<Response>") {
			t.Fatalf("%s %s with no leader: %d %v %s", mp[0], mp[1], rec.Code, rec.Header(), rec.Body)
		}
	}
}

// The provider signs a keypress over https with the port dropped: a
// controller configured at :8443 takes it.
func TestInboundVoiceSignedWithoutThePort(t *testing.T) {
	s := inboundServer(t)
	u := s.VoiceAckURL("HiTemp", occOf(t, s.alarms, "HiTemp").Occurrence, "alice", 1)
	path := strings.TrimPrefix(u, inboundBase)
	form := url.Values{"To": {alicePhone}, "Digits": {"1"}}
	rec := call(t, s, "POST", path, "", signed(voiceToken, path, form), func(r *http.Request) {
		r.Header.Set("X-Twilio-Signature", twilioSign(voiceToken, "https://hmi.example"+path, form))
	})
	if !strings.Contains(rec.Body.String(), "<Say>Acknowledged.") {
		t.Fatalf("portless signature: %d %s", rec.Code, rec.Body)
	}
}

// The provider holds no NAUTILUS_TOKEN and sends no Origin: its signed
// POST is not refused by the write rule.
func TestInboundNeedsNoOtherCredential(t *testing.T) {
	s := auditServer(t, Options{AuthToken: "tok"})
	withInbound(t, s)
	rec := reply(t, s, alicePhone, "ACK "+codeFor(t, s, "HiTemp", "alice"))
	if !strings.Contains(rec.Body.String(), "Acknowledged") {
		t.Fatalf("with a write token configured: %d %s", rec.Code, rec.Body)
	}
}

// A standby's forward reaches a real leader that checks the signature
// over its own configured URL and acknowledges.
func TestInboundForwardedReplyVerifiesAtTheLeader(t *testing.T) {
	lead := inboundServer(t)
	backend := httptest.NewServer(lead.Handler())
	defer backend.Close()
	s := auditServer(t, Options{Cluster: fakeCluster{status: leader.Status{
		Mode: "quorum", Pod: "b", Leader: "a", LeaderAddr: standbyAddr(backend)}}})
	withInbound(t, s)
	rec := reply(t, s, alicePhone, "ACK "+codeFor(t, lead, "HiTemp", "alice"))
	if !strings.Contains(rec.Body.String(), "Acknowledged") || state(lead.alarms, "HiTemp") != alarm.AckActive {
		t.Fatalf("forwarded reply: %d %s", rec.Code, rec.Body)
	}
	if state(s.alarms, "HiTemp") != alarm.UnackActive {
		t.Fatal("the standby's engine was acked")
	}
}

func TestInboundRoutesAreTheirPaths(t *testing.T) {
	if InboundSMSPath != "/alarm-inbound/sms" || InboundVoicePath != "/alarm-inbound/voice/" {
		t.Fatalf("paths %q %q: change the routes in server.go with them", InboundSMSPath, InboundVoicePath)
	}
}
