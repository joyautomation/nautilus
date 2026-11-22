package server

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
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

// twilioSign is the provider's signature of a form POST to u (the scheme
// alarm/channel checks against the provider's published vectors).
func twilioSign(token, u string, form url.Values) string {
	var b strings.Builder
	b.WriteString(u)
	names := make([]string, 0, len(form))
	for k := range form {
		names = append(names, k)
	}
	slices.Sort(names)
	for _, k := range names {
		vs := slices.Clone(form[k])
		slices.Sort(vs)
		for _, v := range slices.Compact(vs) {
			b.WriteString(k + v)
		}
	}
	m := hmac.New(sha1.New, []byte(token))
	m.Write([]byte(b.String()))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

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
			return tok != "" && hmac.Equal([]byte(sig), []byte(twilioSign(tok, u, form)))
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
	c, err := s.ackLinks.MatchCode(code, "alice", s.standing())
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
	for range InboundTries {
		reply(t, s, alicePhone, "ACK "+wrong)
	}
	rec := reply(t, s, alicePhone, "ACK "+code)
	if !strings.Contains(rec.Body.String(), "too many wrong codes") || state(s.alarms, "HiTemp") != alarm.UnackActive {
		t.Fatalf("after %d wrong codes the right one: %s", InboundTries, rec.Body)
	}
	// (Repeated refusals of one person are coalesced by the audit seam.)
	// bob is not alice: his code still works.
	if rec := reply(t, s, bobPhone, "ACK "+codeFor(t, s, "HiTemp", "bob")); !strings.Contains(rec.Body.String(), "Acknowledged") {
		t.Fatalf("bob: %s", rec.Body)
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
		if tr.blocked("alice", t0.Add(time.Duration(i)*time.Minute)) {
			t.Fatalf("blocked after %d", i)
		}
		tr.fail("alice", t0.Add(time.Duration(i)*time.Minute))
	}
	if !tr.blocked("alice", t0.Add(10*time.Minute)) || tr.blocked("bob", t0.Add(10*time.Minute)) {
		t.Fatal("limit not per person")
	}
	// The first wrong code leaves the window an hour after it was sent.
	if tr.blocked("alice", t0.Add(inboundTriesWindow)) != false {
		t.Fatal("still blocked once the first wrong code is an hour old")
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
		seen = append(seen, r.URL.Path+" "+r.Header.Get("X-Twilio-Signature"))
		w.Write([]byte("from-leader"))
	}))
	defer backend.Close()
	s := auditServer(t, Options{Cluster: fakeCluster{status: leader.Status{
		Mode: "quorum", Pod: "b", Leader: "a", LeaderAddr: standbyAddr(backend)}}})
	withInbound(t, s)
	rec := reply(t, s, alicePhone, "ACK 12345678")
	if !strings.Contains(rec.Body.String(), "from-leader") || len(seen) != 1 || !strings.HasPrefix(seen[0], InboundSMSPath+" ") || strings.HasSuffix(seen[0], " ") {
		t.Fatalf("forwarded %v: %s", seen, rec.Body)
	}

	s = auditServer(t, Options{Cluster: fakeCluster{status: leader.Status{Mode: "quorum", Pod: "b"}}})
	withInbound(t, s)
	rec = reply(t, s, alicePhone, "ACK 12345678")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<Message>Not acknowledged: no controller is leading") {
		t.Fatalf("no leader: %d %s", rec.Code, rec.Body)
	}
	if e := lastAudit(t, s); e.Result == audit.ResultOK || e.Detail["status"] != http.StatusServiceUnavailable {
		t.Fatalf("no leader journalled %+v", e)
	}
}

func TestInboundRoutesAreTheirPaths(t *testing.T) {
	if InboundSMSPath != "/alarm-inbound/sms" || InboundVoicePath != "/alarm-inbound/voice/" {
		t.Fatalf("paths %q %q: change the routes in server.go with them", InboundSMSPath, InboundVoicePath)
	}
}
