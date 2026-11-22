package server

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/alarm/acklink"
)

// Inbound acknowledgements (#715): a person paged by SMS replies
// "ACK <code>"; a person called presses 1. The SMS and voice provider
// (Twilio-compatible) posts the reply or the keypress here, signed under
// the account's token.
//
// What each proves:
//   - the provider's signature, over the URL this controller is
//     configured to be called at (never the request's Host), proves the
//     provider relayed it. It does not prove who sent an SMS: a sender's
//     number can be forged.
//   - an SMS reply's code (alarm/acklink, CodeDigits digits) is its
//     credential: it names one occurrence of one alarm, for one person,
//     within a day of the page. The sender's number says which person
//     (exactly one contact, else nothing is said back); a person who
//     sends InboundTries wrong codes is refused for an hour.
//   - a keypress's URL carries an ack link minted for the call (kind
//     voice), which only the provider holds; the call's To must be the
//     person's number, and the key must be 1. A voice link is refused at
//     /alarm-ack/: it acts only on the provider's signed keypress.
//
// Then it is a link: the person must still stand here and hold ack, and
// the acknowledgement takes the same path (ackByClaim), spent once by its
// ID through the pair's log, journalled alarm.ack with via sms|voice.
// Neither the number nor the message's text is journalled or logged.
//
// The provider must hear 200 with TwiML, a refusal too (otherwise it
// sends no reply and plays the caller an error): the audit seam journals
// the refusal's own status and answers 200 (providerAnswerHeader). A
// request whose signature does not check is answered with its status.

// Inbound is how a controller takes acknowledgements back through the
// SMS and voice provider (alarms.oncall.inbound).
type Inbound struct {
	// URL is alarms.oncall.inbound.url: this controller (or its pair) as
	// the provider reaches it. Signatures are checked against it.
	URL string
	// SMS and Voice say which kinds take replies.
	SMS, Voice bool
	// Verify reports whether sig is the provider's signature of a form
	// POST to url, under the token of channel kind (sms or voice).
	Verify func(kind, url string, form url.Values, sig string) bool
	// Callers lists the principals whose contact for kind is number.
	Callers func(kind, number string) []string
	// MayPage reports whether the escalation of rec can page principal by
	// kind (any level, any shift): a reply's code is tried only against
	// those occurrences. Nil: every standing occurrence.
	MayPage func(rec alarm.Record, principal, kind string) bool
}

// Inbound paths.
const (
	InboundPath      = "/alarm-inbound/"
	InboundSMSPath   = InboundPath + "sms"
	InboundVoicePath = InboundPath + "voice/"
)

const (
	// InboundTries is how many wrong codes a person may send in
	// inboundTriesWindow before every code from them is refused until
	// the window has passed.
	InboundTries       = 5
	inboundTriesWindow = time.Hour
	// InboundTriesAll is how many wrong codes, from anyone, a controller
	// takes in inboundAllWindow before it refuses every code until the
	// oldest leaves it (links and the HMI still acknowledge): the bound on
	// guessing however many numbers are forged.
	InboundTriesAll  = 10
	inboundAllWindow = 24 * time.Hour
	// inboundReplies is how many refusals a person is texted back in
	// inboundTriesWindow; past it they get no reply (a forged sender cannot
	// make the controller text someone without limit, and an auto-responder
	// cannot loop with it).
	inboundReplies = 3
	// everyoneKey is inboundAll's one key (never a principal).
	everyoneKey = "*"
	// inboundFormMax bounds a provider's form (a few hundred bytes).
	inboundFormMax = 16 << 10
	// providerAnswerHeader asks the audit seam to journal the handler's
	// status and answer 200: a provider plays or sends a TwiML reply only
	// from a 2xx. The seam strips it.
	providerAnswerHeader = "X-Nautilus-Provider-Answer"
)

// AckCode is the reply code an SMS page to principal about alarmID's
// occurrence occ carries; "" when this controller takes no SMS replies.
//
// While SMS acknowledgements are suspended (SMSAckSuspended) none is
// made: the page then offers the link or the HMI, never a reply that would
// be refused.
func (s *Server) AckCode(alarmID, occ, principal string) string {
	if s.inbound == nil || !s.inbound.SMS || s.ackLinks == nil {
		return ""
	}
	if s.SMSAckSuspended() && !mutServer("sms_ack_offered_while_suspended") {
		return ""
	}
	return s.ackLinks.Code(alarmID, occ, canonPrincipal(principal))
}

// SMSAckSuspended reports whether this controller takes no SMS reply now:
// InboundTriesAll wrong codes within a day (someone may be guessing). It
// ends by itself as the oldest leaves the day; naut run raises a system
// alarm while it lasts.
func (s *Server) SMSAckSuspended() bool {
	return s.inbound != nil && s.inbound.SMS && s.inboundAll.blocked(everyoneKey, InboundTriesAll, time.Now())
}

// noteSuspended journals, once per suspension, that SMS acknowledgements
// were suspended, and until when.
func (s *Server) noteSuspended(now time.Time) {
	s.inboundAll.mu.Lock()
	w := s.inboundAll.recent(everyoneKey, now)
	first := len(w) >= InboundTriesAll && !s.inboundAll.noted
	if first {
		s.inboundAll.noted = true
	}
	var until time.Time
	if first {
		// It ends when the oldest wrong code that makes up the count
		// leaves the window.
		until = w[len(w)-InboundTriesAll].Add(s.inboundAll.span())
	}
	s.inboundAll.mu.Unlock()
	if first {
		slog.Warn("alarm: SMS acknowledgements suspended: too many wrong reply codes; links and the HMI still acknowledge", "until", until.UTC())
		s.RecordSystem("alarm.sms-ack-suspended", "", map[string]any{
			"wrongCodes": InboundTriesAll, "within": inboundAllWindow.String(), "until": until.UTC().Format(time.RFC3339),
		})
	}
}

// canonPrincipal is principal as the contacts file keys it: a local user
// name lowercase without user:, an oidc: name as it is. Codes and call
// tokens are minted over it, so a manifest's "Alice" matches "alice".
func canonPrincipal(p string) string {
	if strings.HasPrefix(p, "oidc:") {
		return p
	}
	return strings.ToLower(strings.TrimPrefix(p, "user:"))
}

// VoiceAckURL is the URL a call to principal about alarmID's occurrence
// occ, at the escalation's level, gives the provider for the callee's
// keypress; "" when this controller takes no keypresses. It holds a
// credential: it goes to the provider in the call's instructions only.
func (s *Server) VoiceAckURL(alarmID, occ, principal string, level int) string {
	if s.inbound == nil || !s.inbound.Voice || s.ackLinks == nil || occ == "" {
		return ""
	}
	tok, err := s.ackLinks.Mint(alarmID, occ, canonPrincipal(principal), "voice", level, acklink.MaxTTL)
	if err != nil {
		slog.Warn("alarm: no keypress URL for a call", "id", alarmID, "to", principal, "err", err)
		return ""
	}
	return strings.TrimRight(s.inbound.URL, "/") + InboundVoicePath + tok
}

// tries counts events per key (a person's wrong codes, everyone's, the
// refusals texted to a person) in a sliding window.
type tries struct {
	mu     sync.Mutex
	wrong  map[string][]time.Time
	window time.Duration // 0: inboundTriesWindow
	// noted: the suspension now under way was journalled (inboundAll).
	noted bool
}

func (t *tries) span() time.Duration {
	if t.window == 0 {
		return inboundTriesWindow
	}
	return t.window
}

// blocked reports whether key had limit events in the window.
func (t *tries) blocked(key string, limit int, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	b := len(t.recent(key, now)) >= limit && !mutServer("inbound_tries_unlimited")
	if !b {
		t.noted = false // a later suspension is a new one
	}
	return b
}

func (t *tries) fail(p string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.wrong == nil {
		t.wrong = map[string][]time.Time{}
	}
	for k := range t.wrong {
		if len(t.recent(k, now)) == 0 {
			delete(t.wrong, k)
		}
	}
	t.wrong[p] = append(t.recent(p, now), now)
}

// recent is p's wrong codes within the window (t.mu held).
func (t *tries) recent(p string, now time.Time) []time.Time {
	w := t.wrong[p]
	i := 0
	for i < len(w) && now.Sub(w[i]) >= t.span() {
		i++
	}
	return w[i:]
}

// viaKey carries how an inbound acknowledgement came (sms, voice).
type viaKey struct{}

// inboundAuditTarget is an inbound acknowledgement's audit target: the
// alarm and how (never the number, never the text).
func inboundAuditTarget(r *http.Request, _ []byte) auditTarget {
	c, _ := linkOf(r)
	t := linkTarget(c)
	t.detail["via"], _ = r.Context().Value(viaKey{}).(string)
	if c.Kind == "sms" {
		delete(t.detail, "level") // a code does not say the level it was sent at
	}
	return t
}

// readInboundForm reads a provider's form, bounded. The body is spent:
// the seam reads nothing after a preStep that read it.
func readInboundForm(r *http.Request) (url.Values, bool) {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/x-www-form-urlencoded" {
		return nil, false
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, inboundFormMax+1))
	r.Body.Close()
	r.Body = http.NoBody
	if err != nil || len(b) > inboundFormMax {
		return nil, false
	}
	f, err := url.ParseQuery(string(b))
	if err != nil {
		return nil, false
	}
	return f, true
}

// signedURL is the URL the provider signed r at: the configured inbound
// URL, r's path and query.
func (s *Server) signedURL(r *http.Request) string {
	u := strings.TrimRight(s.inbound.URL, "/") + r.URL.EscapedPath()
	if r.URL.RawQuery != "" {
		u += "?" + r.URL.RawQuery
	}
	return u
}

// checkInbound is the inbound routes' preStep: it checks the provider's
// signature, then the credential (the code, or the call's link), and
// fixes who acknowledges, before the seam authorizes. A refusal is
// answered here, through the seam: TwiML for a signed request.
func (s *Server) checkInbound(kind string) preStep {
	return func(route *auditedRoute, w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
		r = r.WithContext(context.WithValue(r.Context(), viaKey{}, kind))
		refuseWith := func(r *http.Request, t auditTarget, code int, reason, reply string, signed bool) (*http.Request, bool) {
			if t.detail == nil {
				t.detail = map[string]any{}
			}
			t.detail["via"] = kind
			route.answer(w, s.withActor(r), t, func(w http.ResponseWriter, _ *http.Request) {
				s.inboundRefused(w, kind, code, reason, reply, signed)
			})
			return r, false
		}
		on := s.inbound != nil && s.ackLinks != nil && (kind == "sms" && s.inbound.SMS || kind == "voice" && s.inbound.Voice)
		if !on {
			return refuseWith(r, auditTarget{}, http.StatusNotFound, "this controller takes no "+kind+" acknowledgements", "", false)
		}
		form, ok := readInboundForm(r)
		if !ok {
			return refuseWith(r, auditTarget{}, http.StatusBadRequest, "not a provider's form", "", false)
		}
		if !s.inbound.Verify(kind, s.signedURL(r), form, r.Header.Get("X-Twilio-Signature")) && !mutServer("inbound_unsigned") {
			return refuseWith(r, auditTarget{}, http.StatusForbidden, "the provider's signature does not check (alarms.oncall.inbound.url must be exactly the URL the provider calls)", "", false)
		}
		var c acklink.Claim
		var callers []string
		if kind == "sms" {
			callers = s.inbound.Callers("sms", form.Get("From"))
			switch {
			case len(callers) == 0:
				// Nothing is said back to a number nobody here has.
				return refuseWith(r, auditTarget{}, http.StatusForbidden, "the sender is not an SMS contact on this controller", "", true)
			case len(callers) > 1:
				reply, key, now := "Not acknowledged: this number belongs to more than one person on the controller. Acknowledge from the link or the HMI.", "shared:"+strings.Join(callers, ","), time.Now()
				if s.inboundReplies.blocked(key, inboundReplies, now) {
					reply = ""
				} else {
					s.inboundReplies.fail(key, now)
				}
				return refuseWith(r, auditTarget{}, http.StatusForbidden, "the sender's number is the SMS contact of more than one person here", reply, true)
			}
			p := canonPrincipal(callers[0])
			who := r.WithContext(context.WithValue(r.Context(), actorKey{}, actor{pr: linkPrincipal(p)}))
			// Until a code matches, only the (forgeable) number says who:
			// the entry says so, and the person is texted a refusal at most
			// inboundReplies times an hour.
			byNumber := func() auditTarget {
				return auditTarget{detail: map[string]any{"sender": "named by the sender's number only"}}
			}
			now := time.Now()
			refuseSender := func(code int, reason, reply string) (*http.Request, bool) {
				if s.inboundReplies.blocked(p, inboundReplies, now) && !mutServer("inbound_replies_unlimited") {
					reply = ""
				} else {
					s.inboundReplies.fail(p, now)
				}
				return refuseWith(who, byNumber(), code, reason, reply, true)
			}
			if s.inboundTries.blocked(p, InboundTries, now) {
				return refuseSender(http.StatusTooManyRequests, "too many wrong codes from this person in the last hour",
					"Not acknowledged: too many wrong codes. Acknowledge from the link or the HMI.")
			}
			if s.inboundAll.blocked(everyoneKey, InboundTriesAll, now) && !mutServer("inbound_no_global_budget") {
				return refuseSender(http.StatusTooManyRequests, "too many wrong codes to this controller in the last day",
					"Not acknowledged: replies are paused after too many wrong codes. Acknowledge from the link or the HMI.")
			}
			code, err := acklink.ParseReply(form.Get("Body"))
			if err != nil {
				return refuseSender(http.StatusBadRequest, "the reply holds no code",
					"Not acknowledged. Reply ACK and the "+strconv.Itoa(acklink.CodeDigits)+"-digit code from the page.")
			}
			c, err = s.ackLinks.MatchCode(code, p, s.standing(p))
			if err != nil {
				reason, reply := "the code matches no alarm waiting on this person", "Not acknowledged: that code matches no alarm waiting on you."
				if errors.Is(err, acklink.ErrAmbiguous) {
					reason, reply = "the code matches more than one alarm", "Not acknowledged: that code matches more than one alarm. Acknowledge from the link or the HMI."
				} else if _, done := s.ackLinks.MatchCode(code, p, s.answered(p)); done == nil && !mutServer("inbound_stale_code_counted") {
					// A good code for an occurrence acknowledged since (the
					// other person paged got there first): not a guess.
					reason, reply = "the code's alarm was already acknowledged", "That alarm was already acknowledged."
				} else {
					s.inboundTries.fail(p, now)
					s.inboundAll.fail(everyoneKey, now)
					s.noteSuspended(now)
				}
				return refuseSender(http.StatusForbidden, reason, reply)
			}
		} else {
			var err error
			c, err = s.ackLinks.Verify(r.PathValue("token"))
			if err != nil || c.Kind != "voice" {
				code, reason := http.StatusNotFound, "the call's link is not valid on this controller"
				if errors.Is(err, acklink.ErrExpired) && c.Kind == "voice" {
					code, reason = http.StatusGone, "the call's link has expired"
				}
				return refuseWith(r, auditTarget{}, code, reason, "Not acknowledged. Acknowledge it in the HMI.", true)
			}
			who := r.WithContext(context.WithValue(r.Context(), actorKey{}, actor{pr: linkPrincipal(c.Principal)}))
			callee := slices.ContainsFunc(s.inbound.Callers("voice", form.Get("To")), func(p string) bool { return canonPrincipal(p) == canonPrincipal(c.Principal) })
			if !callee && !mutServer("inbound_callee_unchecked") {
				return refuseWith(who, linkTarget(c), http.StatusForbidden, "the call's number is not the voice contact of "+c.Principal,
					"Not acknowledged. Acknowledge it in the HMI.", true)
			}
			if form.Get("Digits") != "1" && !mutServer("inbound_any_key") {
				return refuseWith(who, linkTarget(c), http.StatusBadRequest, "a key other than 1 was pressed",
					"Not acknowledged.", true)
			}
		}
		r = withLink(r, c)
		t := inboundAuditTarget(r, nil)
		a, code, msg, _ := s.linkActor(r, c)
		r = r.WithContext(context.WithValue(r.Context(), actorKey{}, a))
		if code != 0 {
			return refuseWith(r, t, code, msg, "Not acknowledged: "+msg, true)
		}
		if s.policy != nil && !s.policy.Holds(a.pr, PermAck) {
			msg := a.pr.String() + " may not acknowledge alarms on this controller now"
			return refuseWith(r, t, http.StatusForbidden, msg, "Not acknowledged: "+msg+".", true)
		}
		return r, true
	}
}

// standing is every occurrence waiting on a person now whose escalation
// can page p by SMS: what p's code is tried against.
func (s *Server) standing(p string) []acklink.Standing {
	if s.alarms == nil {
		return nil
	}
	var out []acklink.Standing
	for _, rec := range s.alarms.Records() {
		if rec.Occurrence == "" || (rec.State != alarm.UnackActive && rec.State != alarm.UnackRTN) {
			continue
		}
		if s.inbound != nil && s.inbound.MayPage != nil && !s.inbound.MayPage(rec, p, "sms") && !mutServer("inbound_any_alarm") {
			continue
		}
		st := acklink.Standing{Alarm: rec.ID, Occ: rec.Occurrence}
		if rec.ActiveMs > 0 {
			st.Since = time.UnixMilli(rec.ActiveMs)
		}
		out = append(out, st)
	}
	return out
}

// answered is every occurrence acknowledged now whose escalation can page
// p by SMS: a code for one is stale, not a guess.
func (s *Server) answered(p string) []acklink.Standing {
	if s.alarms == nil {
		return nil
	}
	var out []acklink.Standing
	for _, rec := range s.alarms.Records() {
		if rec.Occurrence == "" || rec.State == alarm.UnackActive || rec.State == alarm.UnackRTN {
			continue
		}
		if s.inbound != nil && s.inbound.MayPage != nil && !s.inbound.MayPage(rec, p, "sms") {
			continue
		}
		st := acklink.Standing{Alarm: rec.ID, Occ: rec.Occurrence}
		if rec.ActiveMs > 0 {
			st.Since = time.UnixMilli(rec.ActiveMs)
		}
		out = append(out, st)
	}
	return out
}

// handleInboundAck acknowledges by a checked reply or keypress, behind
// the audit seam, and answers the provider.
func (s *Server) handleInboundAck(w http.ResponseWriter, r *http.Request) {
	c, _ := linkOf(r)
	kind, _ := r.Context().Value(viaKey{}).(string)
	noun := "code"
	if kind == "voice" {
		noun = "call"
	}
	if status, msg := s.ackByClaim(w.Header(), r, c, noun); status != http.StatusOK {
		if w.Header().Get(outcomeHeader) == "unknown" && !mutServer("inbound_unknown_as_refused") {
			// Passed to the pair's log, not confirmed: it may still take
			// effect, so it is journalled as unknown, not refused.
			w.Header().Set(linkReasonHeader, msg)
			s.twiml(w, status, s.say(kind, "Could not confirm the acknowledgement in time: it may still take effect. Check the HMI."), true)
			return
		}
		s.inboundRefused(w, kind, status, msg, "Not acknowledged: "+msg, true)
		return
	}
	name := c.Alarm
	if rec, ok := s.alarms.Lookup(c.Alarm); ok && rec.Name != "" {
		name = rec.Name
	}
	if kind == "voice" {
		s.twiml(w, http.StatusOK, `<Say>Acknowledged. The escalation stops.</Say>`, true)
		return
	}
	s.twiml(w, http.StatusOK, "<Message>"+xmlEscape(truncateText("Acknowledged "+name+" as "+c.Principal+". The escalation stops.", 300))+"</Message>", true)
}

// inboundRefused answers a refused reply or keypress: for a signed
// request, TwiML saying reply (nothing when reply is "") at 200 on the
// wire, the refusal's status and reason in the journal; else the status.
func (s *Server) inboundRefused(w http.ResponseWriter, kind string, code int, reason, reply string, signed bool) {
	w.Header().Set(outcomeHeader, "refused")
	w.Header().Set(linkReasonHeader, reason)
	if !signed {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(code)
		io.WriteString(w, http.StatusText(code)+"\n")
		return
	}
	s.twiml(w, code, s.say(kind, reply), true)
}

// say is reply as TwiML for kind: spoken on a call, texted for an SMS;
// nothing for "".
func (s *Server) say(kind, reply string) string {
	switch {
	case reply == "":
		return ""
	case kind == "voice":
		return "<Say>" + xmlEscape(truncateText(reply, 300)) + "</Say>"
	}
	return "<Message>" + xmlEscape(truncateText(reply, 300)) + "</Message>"
}

// twiml answers with a TwiML document. provider marks it for the seam to
// answer 200 whatever status it journals.
func (s *Server) twiml(w http.ResponseWriter, status int, inner string, provider bool) {
	h := w.Header()
	h.Set("Content-Type", "text/xml; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	if provider && !mutServer("inbound_status_on_wire") {
		h.Set(providerAnswerHeader, "1")
	}
	w.WriteHeader(status)
	io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Response>`+inner+`</Response>`)
}

// xmlEscape is s as XML character data (or an attribute value).
func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// truncateText is s cut to n runes.
func truncateText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
