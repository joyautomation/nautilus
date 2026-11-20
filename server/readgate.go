package server

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/auth"
)

// The read gate (#708 PR 5b-2). With an auth: block, everything a request
// can read through the API needs the read permission: every GET (and HEAD)
// under /api/, the live stream included, which a browser opens with its
// sign-in cookie. Without an auth: block nothing changes.
//
// Exempt, because they carry no tag data:
//   - GET /api/me, which answers an anonymous request too, so a page can
//     tell it must sign in;
//   - the sign-in page and routes (POST is not a read);
//   - /healthz and /readyz (answered before the Host rule, below);
//   - static files fetched by a page (its scripts, styles, fonts: no tag
//     values, though an HMI build names its tags). A page
//     itself, opened by a browser, is redirected to /signin instead.
//
// It runs inside GuardHost, so naut run and both facades (naut logix serve,
// naut siemens serve) get it, the facades' own routes included.

// Liveness and readiness, for a load balancer, Kubernetes or a floating
// address (#716). Both answer without a credential, by any Host, and say
// nothing but the one word: no role, version or tag.
const (
	healthzPath = "/healthz"
	readyzPath  = "/readyz"
)

// probe answers /healthz and /readyz, and reports whether r was one.
//
//   - /healthz is liveness: 200 "ok" whenever this process serves HTTP, on
//     every replica. A standby answering 503 there would be restarted by a
//     liveness probe.
//   - /readyz is readiness: 200 "ok" on the controller that serves the
//     process (a controller without redundancy, or the leader), 503 "not
//     ready" on a standby or while no leader is known. It is what replaces
//     /api/cluster, which now needs read, as the way to find the leader.
func (s *Server) probe(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	code := 0
	switch r.URL.Path {
	case healthzPath:
		code = http.StatusOK
	case readyzPath:
		code = http.StatusOK
		if s.cluster != nil && !s.cluster.Status().IsLeader {
			code = http.StatusServiceUnavailable
		}
	default:
		return false
	}
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	if code == http.StatusOK {
		w.Write([]byte("ok"))
	} else {
		w.Write([]byte("not ready"))
	}
	return true
}

// gatedRead reports an API read: GET or HEAD under /api/, except /api/me.
func gatedRead(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	return strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/me"
}

// navigation reports a browser opening a page: Sec-Fetch-Mode navigate,
// or, from a browser that sends no Sec-Fetch headers (plain HTTP by IP is
// not a secure context, so none are sent), one that accepts HTML.
func navigation(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if m := r.Header.Get("Sec-Fetch-Mode"); m != "" {
		return m == "navigate"
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// readDecision is whether a may read: 0 when it may, else the status and
// message to refuse with. An unknown bearer credential is a 401 even where
// anonymous may read: it is never taken as anonymous.
func (s *Server) readDecision(a actor) (int, string) {
	if a.invalid {
		return http.StatusUnauthorized, "invalid credential"
	}
	if s.policy.Holds(a.pr, auth.Read) {
		return 0, ""
	}
	reason := s.policy.Can(a.pr, auth.Read, auth.All).Reason
	if a.pr.Kind == auth.KindAnonymous {
		return http.StatusUnauthorized, reason + signInHint
	}
	return http.StatusForbidden, reason
}

// readRefused applies the read gate to r and, when it refuses, answers it
// and reports true.
func (s *Server) readRefused(w http.ResponseWriter, r *http.Request) bool {
	if s.policy == nil {
		return false
	}
	api := gatedRead(r)
	page := !api && !strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/signin" && navigation(r)
	if !api && !page {
		return false
	}
	a := s.actorOf(r)
	code, msg := s.readDecision(a)
	if code == 0 {
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	// The refusal is answered here, before withCORS: it carries the same
	// CORS answer withCORS would have given, so an allowed cross-origin
	// page (a dev server) can read it.
	w.Header().Add("Vary", "Origin")
	s.allowOrigin(w, r)
	// A forwarding standby holds no sessions, so a signed-in browser's
	// cookie is unknown here: refer it to the leader (#708 decision (c)),
	// as proxyStandby does every cookie request, rather than refusing it.
	// The request is never let through: the standby answers /api/cluster
	// and /api/history itself, and a stale cookie must not read them.
	if api && hasSessionCookie(r) && presentedToken(r) == "" {
		if leader, standby := s.standbyLeader(); standby {
			s.referToLeader(w, leader)
			return true
		}
	}
	if api {
		writeJSON(w, code, map[string]string{"error": msg})
		return true
	}
	// A page. An anonymous browser goes to the sign-in page and comes back
	// here afterwards. A signed-in user without read is not sent there (it
	// would bring them straight back), and a controller without sign-in
	// (a facade, a project without users) has no page to send it to.
	if a.pr.Kind == auth.KindAnonymous && !a.invalid && s.sessions != nil {
		http.Redirect(w, r, "/signin?next="+url.QueryEscape(safeNext(r.URL.RequestURI())), http.StatusSeeOther)
		return true
	}
	http.Error(w, msg, code)
	return true
}

// streamRecheck is how often an open /api/stream re-checks that its
// request may still read: a session's absolute expiry, a revoked or
// disabled user's personal access token, a session ended while the
// stream was being registered. A sign-out or a revocation closes the
// session's streams at once (endSessionStreams).
var streamRecheck = time.Second

// streamMayRead reports whether r, a stream's request, may still read,
// resolved afresh (never from a value fixed on its context).
func (s *Server) streamMayRead(r *http.Request) bool {
	if s.policy == nil {
		return true
	}
	code, _ := s.readDecision(s.resolveActor(r))
	return code == 0
}

// endSessionStreams closes the open streams a session's cookie opened: it
// was signed out, its user was disabled, removed or given a new password,
// or the controller became a standby.
func (s *Server) endSessionStreams(se session) {
	s.mu.Lock()
	var ending []*client
	for c := range s.clients {
		if c.session != "" && c.session == se.hash {
			ending = append(ending, c)
		}
	}
	s.mu.Unlock()
	for _, c := range ending {
		c.end()
	}
}
