package peertls

import (
	"errors"
	"net/http"
)

// RefusedHeader and RefusedPeerKey mark Bind's refusal, so a caller tells
// a refused key from other errors (quorum/node's RefusedHeader).
const (
	RefusedHeader  = "X-Nautilus-Refused"
	RefusedPeerKey = "peer-key"
)

// Bind serves next on a peer listener behind the per-request check: the
// caller's certificate, which the handshake verified, is checked again
// against the current pin on every request (a pin changed since the
// connection was made counts at once, and a dropped pin's key is never
// pinned again by a connection kept from before the drop), and a name not pinned
// yet is pinned now, durably, before next sees the request: the
// handshake is complete, so the caller has proved it holds the key.
// bind then puts the verified member (cluster, "" for the witness, and
// name) in the request, for the handlers that check a sender against it.
// A request without a verified certificate is refused (403): Bind serves
// a TLS listener only.
func (p *Peer) Bind(next http.Handler, bind func(r *http.Request, cluster, name string) *http.Request) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			refuseKey(w, errors.New("peer TLS: no verified certificate on this request"))
			return
		}
		m, err := p.signed(*r.TLS)
		if err == nil && !mut("pin_checked_once") {
			err = p.pins.check(p.pinCluster(m), m.name, leafKey(*r.TLS), r.RemoteAddr, false)
		} else if err == nil {
			if _, pinned := p.pins.list(p.pinCluster(m))[m.name]; !pinned {
				err = p.pins.check(p.pinCluster(m), m.name, leafKey(*r.TLS), r.RemoteAddr, false)
			}
		}
		var ie *IdentityError
		switch {
		case errors.As(err, &ie):
			refuseKey(w, err)
			return
		case err != nil:
			// A pin that could not be written: not the caller's key.
			w.Header().Set(RefusedHeader, "peer-pin-write")
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, bind(r, m.cluster, m.name))
	})
}

func refuseKey(w http.ResponseWriter, err error) {
	w.Header().Set(RefusedHeader, RefusedPeerKey)
	http.Error(w, err.Error(), http.StatusForbidden)
}
