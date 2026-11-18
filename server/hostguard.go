package server

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// The Host rule (#811, #819) a nautilus HTTP server answers by: a request
// is answered when it is addressed to a host no page on another site can
// take over by DNS rebinding (localhost, a *.localhost name, an IP address,
// or a name in NAUTILUS_ALLOWED_HOSTS / an allow-list), or when a token is
// configured and the request carries it. Server.GuardHost applies it to
// the controller; HostGuard applies it to a server that is not a Server
// (naut historian).

// allowedHostSet normalises an allow-list plus NAUTILUS_ALLOWED_HOSTS: an
// entry's port and root dot are dropped, as a request Host's are
// ("epic1.plant.lan:8086", "epic1.plant.lan.").
func allowedHostSet(configured []string) map[string]bool {
	set := map[string]bool{}
	for _, h := range append(append([]string(nil), configured...), EnvAllowedHosts()...) {
		h = strings.TrimSuffix(strings.ToLower(hostOnly(strings.TrimSpace(h))), ".")
		if h != "" {
			set[h] = true
		}
	}
	return set
}

// hostTrusted reports a Host no page can rebind: localhost, *.localhost,
// an IP address, or a name in allow.
func hostTrusted(allow map[string]bool, host string) bool {
	h := strings.TrimSuffix(strings.ToLower(hostOnly(host)), ".")
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	if net.ParseIP(h) != nil {
		return true
	}
	return allow[h]
}

// refuseHost writes the 403 for a request by an untrusted name, naming
// both settings. what is the server ("controller", "historian").
func refuseHost(w http.ResponseWriter, r *http.Request, what string, tokenConfigured bool) {
	w.Header().Add("Vary", "Origin")
	how := "add the name to NAUTILUS_ALLOWED_HOSTS, or set NAUTILUS_TOKEN"
	if tokenConfigured {
		how = "send the auth token, or add the name to NAUTILUS_ALLOWED_HOSTS (a browser page or a live stream cannot send the token)"
	}
	http.Error(w, fmt.Sprintf("this %s answers %q only with an auth token: it is reached without one by localhost, an IP address, or a name in NAUTILUS_ALLOWED_HOSTS (a page on another site could rebind %q to it); %s", what, r.Host, hostOnly(r.Host), how), http.StatusForbidden)
}

// HostGuard is the Host rule on its own, for an HTTP server that is not a
// Server: naut historian's /history API (#831).
type HostGuard struct {
	what  string
	token string
	allow map[string]bool
}

// NewHostGuard returns the rule for a server called what ("historian"):
// token, when not empty, lets a request by any name through; allowed and
// NAUTILUS_ALLOWED_HOSTS name the hosts answered without it.
func NewHostGuard(what, token string, allowed ...string) *HostGuard {
	return &HostGuard{what: what, token: token, allow: allowedHostSet(allowed)}
}

// Wrap answers a request only by a trusted Host or with the token; any
// other gets 403 before next sees it, whatever its method or path.
func (g *HostGuard) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostTrusted(g.allow, r.Host) && !(g.token != "" && tokenMatches(r, g.token)) {
			refuseHost(w, r, g.what, g.token != "")
			return
		}
		next.ServeHTTP(w, r)
	})
}
