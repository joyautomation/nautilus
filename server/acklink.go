package server

import (
	"context"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/alarm/acklink"
	"github.com/joyautomation/nautilus/auth"
)

// Ack links (#715): acknowledging an alarm from the page that told a
// person about it.
//
// A page by email or SMS (and a push, #722) carries a link minted for one
// alarm's occurrence, one person, the channel and the level it was sent
// at (alarm/acklink). GET shows a confirm page and changes nothing, so a
// mail scanner that opens every link acknowledges nothing. POST
// acknowledges, through the same path as an HMI acknowledgement: the
// audit seam (alarm.ack, detail via: link), the target's permission, the
// operator-intent log on a pair (the entry names the occurrence and the
// link's ID), the engine, the alarm journal under the person's name.
//
// Who acknowledges: by default (alarms.oncall.ack-from-notification:
// link) the link alone, as the person it was sent to, provided that person
// still stands here (an enabled user in the users file, or an OIDC name
// still pinned) and holds ack for the alarm now. An OIDC person's groups
// are known only from a sign-in, so without one only grants by name
// count; a browser signed in as that person acts with its session
// instead. With ack-from-notification: sign-in, only a browser signed in
// as the person the link was sent to may use it.
//
// A link is spent once. On a pair every replica records the link IDs of
// the log's acks (AlarmLog.spent) and refuses a second ack naming one, so
// a link used before a takeover is refused after it. A controller with no
// pair keeps them in memory (linkBook): after a restart a replay finds
// the occurrence already acknowledged, and acknowledges nothing.

// linkBook is a controller's own record of ack links: those spent here
// without a pair, and those being used right now (two presses at once:
// the second waits for nothing and is refused).
type linkBook struct {
	mu    sync.Mutex
	spent map[string]time.Time // link ID → the link's expiry
	busy  map[string]bool
}

// begin marks id in use and reports false when it already is.
func (b *linkBook) begin(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.busy[id] {
		return false
	}
	if b.busy == nil {
		b.busy = map[string]bool{}
	}
	b.busy[id] = true
	return true
}

func (b *linkBook) end(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.busy, id)
}

// spend records id as spent until exp, pruning what has expired.
func (b *linkBook) spend(id string, exp, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.spent == nil {
		b.spent = map[string]time.Time{}
	}
	for k, e := range b.spent {
		if now.After(e) {
			delete(b.spent, k)
		}
	}
	b.spent[id] = exp
}

func (b *linkBook) isSpent(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.spent[id]
	return ok
}

// linkSpent reports whether the link with id was used: on a pair, by the
// log every replica holds; else by this controller.
func (s *Server) linkSpent(id string) bool {
	if mutServer("link_spent_unchecked") {
		return false
	}
	if s.alarmLog != nil {
		return s.alarmLog.LinkSpent(id)
	}
	return s.links.isSpent(id)
}

// AckLinkURL is the link a page to principal by channel kind, at the
// escalation's level, carries to acknowledge alarmID's occurrence occ:
// valid for acklink.MaxTTL, single use. "" when this controller sends no
// links (no alarms.oncall.ack-url, or no key). The URL is a credential:
// put it in the page and nowhere else (no log, no journal row).
func (s *Server) AckLinkURL(alarmID, occ, principal, kind string, level int) string {
	if s.ackLinks == nil || s.ackURL == "" || occ == "" {
		return ""
	}
	tok, err := s.ackLinks.Mint(alarmID, occ, principal, kind, level, acklink.MaxTTL)
	if err != nil {
		slog.Warn("alarm: no ack link for a page", "id", alarmID, "to", principal, "via", kind, "err", err)
		return ""
	}
	return acklink.URL(s.ackURL, tok)
}

// linkKey carries a checked link's claim on a request.
type linkKey struct{}

func withLink(r *http.Request, c acklink.Claim) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), linkKey{}, c))
}

// linkOf is the checked link r acts by, if any. Only ackLinkRoute sets it,
// after the link's signature and expiry checked.
func linkOf(r *http.Request) (acklink.Claim, bool) {
	c, ok := r.Context().Value(linkKey{}).(acklink.Claim)
	return c, ok
}

// linkPrincipal is the principal a link names ("alice", "user:alice",
// "oidc:bob"); an OIDC one carries no groups.
func linkPrincipal(p string) auth.Principal {
	if n, ok := strings.CutPrefix(p, "oidc:"); ok {
		return auth.OIDC(strings.ToLower(n), nil)
	}
	return auth.User(strings.TrimPrefix(p, "user:"))
}

// linkStands reports whether the person a link names may act by it here
// with no session: a local user still in the users file and enabled, an
// OIDC name still pinned to an account; with no auth: block, the people
// the manifest names. why says what is wrong otherwise.
func (s *Server) linkStands(pr auth.Principal) (ok bool, why string) {
	if s.policy == nil {
		return true, ""
	}
	if pr.Kind == auth.KindOIDC {
		if s.oidc == nil || !s.policy.OIDC() || s.users == nil {
			return false, "this controller does not take OIDC sign-ins now, so " + pr.String() + " cannot act here"
		}
		if _, pinned, err := s.users.Pin(pr.Name); err != nil || !pinned {
			return false, pr.String() + " has not signed in on this controller (or was unpinned): sign in once, then use the link"
		}
		return true, ""
	}
	if s.users == nil {
		return false, "this controller has no users file, so " + pr.String() + " cannot act here"
	}
	u, known := s.users.Current().Users[pr.Name]
	if (!known || u.Disabled || !s.policy.HasUser(pr.Name)) && !mutServer("link_ignores_standing") {
		return false, pr.String() + " is not an enabled user on this controller"
	}
	return true, ""
}

// linkActor is who a checked link acts as on r, or a refusal (status and
// message) when nobody may:
//   - a browser signed in as the person the link names acts with its
//     session (an OIDC person's groups count);
//   - with ack-from-notification: sign-in, nobody else;
//   - else the person the link names, if they still stand here.
//
// A matching session that fails the own-origin rule (cookieRefused: a TLS
// proxy not in server.trusted-proxies, a press forwarded by a standby over
// another scheme) is, by default, ignored: the link alone acts, as for a
// browser not signed in, and ignored says why for the journal. Under
// sign-in it is refused with the fix.
func (s *Server) linkActor(r *http.Request, c acklink.Claim) (a actor, code int, msg, ignored string) {
	pr := linkPrincipal(c.Principal)
	if se := s.resolveActor(r); se.session != nil && se.pr.Kind == pr.Kind && strings.EqualFold(se.pr.Name, pr.Name) {
		refused, why := s.cookieRefused(r, se)
		switch {
		case refused == 0:
			return se, 0, "", ""
		case s.ackLinkSignIn:
			return se, refused, "your browser is signed in as " + c.Principal + ", but this request did not come from this controller's own page: " +
				"if the HMI is reached through a TLS proxy, add the proxy to server.trusted-proxies (" + why + ")", ""
		case mutServer("link_session_cookie_refused"):
			return se, refused, why, ""
		}
		ignored = "the browser's sign-in was ignored: the request did not come from this controller's own page (a proxy not in server.trusted-proxies?), so the link alone acted"
	}
	if s.ackLinkSignIn && !mutServer("link_sign_in_ignored") {
		return actor{pr: auth.Anonymous}, http.StatusUnauthorized, "this controller acknowledges from a link only for a browser signed in as the person it was sent to: sign in as " + c.Principal + ", then open the link again", ""
	}
	if ok, why := s.linkStands(pr); !ok {
		return actor{pr: pr}, http.StatusForbidden, why, ignored
	}
	return actor{pr: pr, at: time.Now()}, 0, "", ignored
}

// sessionIgnoredKey carries, on a link's request, why the browser's
// sign-in was ignored (linkActor), for the journal.
type sessionIgnoredKey struct{}

// linkTargetFor is linkTarget for r: it notes a sign-in linkActor ignored.
func linkTargetFor(r *http.Request, c acklink.Claim) auditTarget {
	t := linkTarget(c)
	if why, ok := r.Context().Value(sessionIgnoredKey{}).(string); ok && why != "" {
		t.detail["session"] = why
	}
	return t
}

// linkTarget is an ack link's audit target: the alarm, and how it was
// acknowledged (the occurrence, the level and channel it was paged at,
// the link's ID; never the token).
func linkTarget(c acklink.Claim) auditTarget {
	return auditTarget{target: c.Alarm, detail: map[string]any{
		"via": "link", "occurrence": c.Occ, "level": c.Level, "channel": c.Kind, "link": c.ID,
	}}
}

// linkAuditTarget is an ack link's audit target, from the link checkLink
// checked (none: the seam is answering a refusal checkLink made itself).
func linkAuditTarget(r *http.Request, _ []byte) auditTarget {
	c, _ := linkOf(r)
	return linkTargetFor(r, c)
}

// checkLink is POST /alarm-ack/{token}'s preStep: it checks the link and
// fixes who it acts as, before the seam authorizes; a link that does not
// check, or a person who may not use it, is answered here with the page,
// journalled through the seam the same way.
func (s *Server) checkLink(route *auditedRoute, w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	refuseWith := func(r *http.Request, t auditTarget, code int, msg string) (*http.Request, bool) {
		route.answer(w, s.withActor(r), t, func(w http.ResponseWriter, _ *http.Request) {
			s.linkRefused(w, code, msg)
		})
		return r, false
	}
	if s.ackLinks == nil {
		return refuseWith(r, auditTarget{}, http.StatusNotFound, "This controller sends no acknowledgement links. Open the HMI to acknowledge.")
	}
	c, err := s.ackLinks.Verify(r.PathValue("token"))
	if err == nil && c.Kind == "voice" && !mutServer("voice_link_acks_here") {
		// A call's keypress URL (inbound.go): it acts only on the
		// provider's signed keypress, never as a link.
		err = acklink.ErrSignature
	}
	if err != nil {
		code, msg, t := linkRefusal(err)
		if errors.Is(err, acklink.ErrExpired) {
			// Genuine but late: journalled under the person it named.
			t = linkTarget(c)
			r = r.WithContext(context.WithValue(r.Context(), actorKey{}, actor{pr: linkPrincipal(c.Principal)}))
		}
		return refuseWith(r, t, code, msg)
	}
	r = withLink(r, c)
	a, code, msg, ignored := s.linkActor(r, c)
	r = r.WithContext(context.WithValue(context.WithValue(r.Context(), actorKey{}, a), sessionIgnoredKey{}, ignored))
	if code != 0 {
		return refuseWith(r, linkTargetFor(r, c), code, msg)
	}
	if s.policy != nil && !s.policy.Holds(a.pr, PermAck) {
		return refuseWith(r, linkTargetFor(r, c), http.StatusForbidden, a.pr.String()+" may not acknowledge alarms on this controller now")
	}
	return r, true
}

// linkRefusal is the answer to a link that did not check.
func linkRefusal(err error) (int, string, auditTarget) {
	if errors.Is(err, acklink.ErrExpired) {
		return http.StatusGone, "This link has expired. Open the HMI to acknowledge.", auditTarget{}
	}
	return http.StatusNotFound, "This link is not valid on this controller. Open the HMI to acknowledge.", auditTarget{detail: map[string]any{"via": "link"}}
}

// handleAckLink acknowledges by a checked link, behind the audit seam.
func (s *Server) handleAckLink(w http.ResponseWriter, r *http.Request) {
	c, _ := linkOf(r)
	if status, msg := s.ackByClaim(w.Header(), r, c, "link"); status != http.StatusOK {
		w.Header().Set(linkReasonHeader, msg)
		s.linkPage(w, status, linkPageData{Title: "Not acknowledged", Error: msg})
		return
	}
	d := s.linkPageFor(c)
	d.Title, d.Done = "Acknowledged", "Acknowledged as "+c.Principal+". The escalation for this alarm stops."
	s.linkPage(w, http.StatusOK, d)
}

// ackByClaim acknowledges c's occurrence as r's actor, by the path an
// HMI acknowledgement takes (authorize, the pair's log or the engine, the
// journal), spending c's ID once. It answers nothing: it reports 200, or
// the status and a message for the person ("this <noun> was already
// used"). The outcome headers it sets on h are the audit seam's.
func (s *Server) ackByClaim(h http.Header, r *http.Request, c acklink.Claim, noun string) (int, string) {
	refused := func(code int, msg string) (int, string) {
		h.Set(outcomeHeader, "refused")
		return code, msg
	}
	if s.alarms == nil {
		return refused(http.StatusNotFound, noAlarms)
	}
	if s.cluster != nil && !s.cluster.Status().IsLeader && !mutServer("link_acks_on_standby") {
		// A replica that does not lead never acts on its own engine (the
		// standby proxy forwards a link; this is a replica with no
		// leader's address to forward to).
		return refused(http.StatusServiceUnavailable, "This controller is not leading, so it cannot acknowledge the alarm. Try again in a minute, or open the HMI.")
	}
	if code, msg := s.authorize(r, PermAck, s.alarmTag(c.Alarm)); code != 0 {
		return refused(code, msg)
	}
	if !s.links.begin(c.ID) {
		return refused(http.StatusConflict, "This "+noun+" is being used right now.")
	}
	defer s.links.end(c.ID)
	if s.linkSpent(c.ID) {
		return refused(http.StatusConflict, "This "+noun+" was already used. Open the HMI to see the alarm.")
	}
	by := s.recordBy(r, c.Principal)
	ids, occ := []string{c.Alarm}, map[string]string{c.Alarm: c.Occ}
	buf := &bufferedResponse{header: h}
	if s.alarmLog != nil {
		s.alarmAckThroughLog(buf, r, ids, occ, by, c.ID)
	} else {
		acked, err := s.alarms.AckIDsAt(ids, occ, by)
		switch {
		case err != nil:
			http.Error(buf, err.Error(), alarmErrStatus(err))
		case len(acked) == 0:
			refuse(buf, "nothing to acknowledge: the alarm is not waiting on anyone", http.StatusConflict)
		default:
			s.links.spend(c.ID, c.Expires, time.Now())
			buf.WriteHeader(http.StatusOK)
		}
	}
	status := buf.status
	if status == 0 {
		status = http.StatusOK
	}
	if status == http.StatusOK {
		return status, ""
	}
	msg := reasonOf(buf.body.Bytes())
	if h.Get(outcomeHeader) == "unknown" {
		// Passed to the pair's log but not confirmed: it may still take
		// effect. The API's text names an API; a person needs this.
		msg = "The controller could not confirm the acknowledgement in time: it may still take effect. Open the HMI to see whether the alarm is acknowledged."
	}
	if strings.Contains(msg, alarm.ErrOtherOccurrence.Error()) {
		msg = "The alarm returned to normal and activated again since this page was sent, so this " + noun + " no longer applies. Open the HMI to see it."
	}
	return status, msg
}

// handleAckLinkPage is GET /alarm-ack/{token}: the confirm page. It
// changes nothing.
func (s *Server) handleAckLinkPage(w http.ResponseWriter, r *http.Request) {
	if s.ackLinks == nil {
		s.linkPage(w, http.StatusNotFound, linkPageData{Title: "Not acknowledged", Error: "This controller sends no acknowledgement links. Open the HMI to acknowledge."})
		return
	}
	c, err := s.ackLinks.Verify(r.PathValue("token"))
	if err == nil && c.Kind == "voice" {
		err = acklink.ErrSignature // a call's keypress URL, not a link
	}
	if err != nil {
		code, msg, _ := linkRefusal(err)
		s.linkPage(w, code, linkPageData{Title: "Not acknowledged", Error: msg})
		return
	}
	d := s.linkPageFor(c)
	if s.alarms == nil {
		d.Error = noAlarms
		s.linkPage(w, http.StatusNotFound, d)
		return
	}
	rec, ok := s.alarms.Lookup(c.Alarm)
	switch {
	case !ok:
		d.Error = "There is no alarm " + c.Alarm + " on this controller now."
	case s.linkSpent(c.ID):
		d.Error = "This link was already used."
	case rec.Occurrence != c.Occ:
		d.Error = "The alarm returned to normal and activated again since this page was sent, so this link no longer applies. Open the HMI to see it."
	case rec.State != alarm.UnackActive && rec.State != alarm.UnackRTN:
		d.Error = "This alarm is not waiting on anyone now"
		if rec.AckBy != "" {
			d.Error += ": " + rec.AckBy + " acknowledged it"
		}
		d.Error += "."
	}
	if d.Error != "" {
		s.linkPage(w, http.StatusOK, d)
		return
	}
	d.State = rec.State.String()
	if s.ackLinkSignIn {
		pr := linkPrincipal(c.Principal)
		if a := s.resolveActor(r); a.session == nil || a.pr.Kind != pr.Kind || !strings.EqualFold(a.pr.Name, pr.Name) {
			d.SignIn = "/signin?next=" + url.QueryEscape(r.URL.Path)
		}
	}
	// The form posts back to the page's own URL (no action): the same
	// link, wherever a proxy put the controller.
	d.Action = r.URL.Path
	s.linkPage(w, http.StatusOK, d)
}

// linkPageData is the confirm page's content.
type linkPageData struct {
	Title, Error, Done            string
	Alarm, Priority, Where, State string
	For, Expires, Action, SignIn  string
	// Home is the HMI (alarms.oncall.ack-url): a page opened from a phone's
	// notification may sit in a window with no way back (#715 PR 6).
	Home string
}

func (s *Server) linkPageFor(c acklink.Claim) linkPageData {
	d := linkPageData{Title: "Acknowledge alarm", Alarm: c.Alarm, For: c.Principal, Expires: c.Expires.UTC().Format("2006-01-02 15:04 UTC")}
	if s.alarms != nil {
		if rec, ok := s.alarms.Lookup(c.Alarm); ok {
			if rec.Name != "" {
				d.Alarm = rec.Name + " (" + rec.ID + ")"
			}
			d.Priority = rec.Priority.String()
			d.Where = strings.Trim(rec.Site+"/"+rec.Area, "/")
		}
	}
	return d
}

// linkReasonHeader carries a refused link page's message from the
// handler to the audit seam, which journals it as the reason (the body is
// HTML) and strips the header before answering.
const linkReasonHeader = "X-Nautilus-Link-Reason"

// linkRefused answers a refused link press: the page with msg, marked
// refused for the seam, msg as the journal's reason.
func (s *Server) linkRefused(w http.ResponseWriter, code int, msg string) {
	w.Header().Set(outcomeHeader, "refused")
	w.Header().Set(linkReasonHeader, msg)
	s.linkPage(w, code, linkPageData{Title: "Not acknowledged", Error: msg})
}

// linkPage answers with the confirm (or result) page.
func (s *Server) linkPage(w http.ResponseWriter, status int, d linkPageData) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	// No script, never framed (no clickjacking of the button), posts only
	// here, not indexed. Referrer same-origin, as the sign-in page: under
	// no-referrer a browser sends "Origin: null" with the form's POST,
	// which a signed-in browser's write is refused for; the link's token
	// then goes only back to this controller.
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	w.WriteHeader(status)
	if d.Home == "" {
		d.Home = s.ackURL
	}
	if err := linkTemplate.Execute(w, d); err != nil {
		slog.Debug("ack link page", "err", err)
	}
}

var linkTemplate = template.Must(template.New("acklink").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="referrer" content="same-origin">
<title>{{.Title}} · nautilus</title>
<style>
:root{color-scheme:light dark;font-family:system-ui,sans-serif}
body{margin:0;min-height:100vh;display:grid;place-items:center;background:Canvas;color:CanvasText}
main{width:min(24rem,100% - 2rem)}
h1{font-size:1.25rem;margin:0 0 1rem}
dl{display:grid;grid-template-columns:auto 1fr;gap:.25rem .75rem;margin:0 0 1rem;font-size:.95rem}
dt{opacity:.7}
dd{margin:0;overflow-wrap:anywhere}
button,.btn{display:block;box-sizing:border-box;margin-top:1rem;width:100%;padding:.75rem;font:inherit;font-size:1.05rem;cursor:pointer;text-align:center;border:1px solid currentColor;border-radius:2px;color:inherit;text-decoration:none}
p{margin:.75rem 0;font-size:.9rem}
.err{color:#c62828}
.ok{font-weight:600}
</style></head>
<body><main>
<h1>{{.Title}}</h1>
{{if .Alarm}}<dl>
<dt>Alarm</dt><dd>{{.Alarm}}</dd>
{{if .Priority}}<dt>Priority</dt><dd>{{.Priority}}</dd>{{end}}
{{if .Where}}<dt>Where</dt><dd>{{.Where}}</dd>{{end}}
{{if .State}}<dt>State</dt><dd>{{.State}}</dd>{{end}}
{{if .For}}<dt>As</dt><dd>{{.For}}</dd>{{end}}
</dl>{{end}}
{{if .Error}}<p class="err" role="alert">{{.Error}}</p>{{end}}
{{if .Done}}<p class="ok" role="status">{{.Done}}</p>{{end}}
{{if and .Action (not .Error) (not .Done)}}{{if .SignIn}}<p>Sign in as {{.For}} to acknowledge.</p><a class="btn" href="{{.SignIn}}">Sign in</a>{{else}}<form method="post">
<button type="submit">Acknowledge</button>
</form>
<p>The link works once, until {{.Expires}}.</p>{{end}}{{end}}
{{if .Home}}<p><a href="{{.Home}}">Open the HMI</a></p>{{end}}
</main></body></html>
`))
