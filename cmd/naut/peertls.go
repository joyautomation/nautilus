package main

import (
	"errors"
	"fmt"

	"github.com/joyautomation/nautilus/internal/peertls"
	"github.com/joyautomation/nautilus/internal/project"
)

// peerTLSOff is the standing warning of a pair member or witness running
// with peer TLS off (#710): shown at every start, on /api/cluster and the
// banner.
const peerTLSOff = "peer TLS is off: votes, the operator-intent log and the peer token cross the network as plain HTTP, readable and replayable by anyone on the path. Run it like this only on a lab bench or a link nothing else reaches; drop the setting to turn it back on"

// peerTLSIsOff reads a peer TLS setting ("" or "on" = on, "off"): with
// the peer_tls_default_off mutation, only an explicit "on" is on.
func peerTLSIsOff(setting string) bool {
	if mutNaut("peer_tls_default_off") {
		return setting != "on"
	}
	return setting == "off"
}

// peerTLSWarnings are naut check's findings about a pair's peer TLS
// (#710): off (the network then carries the token in the clear), or on
// with a peer token in this environment that naut run would refuse, so
// an upgrade is not where it shows first. Warnings: check runs on laptops
// that hold no token, and on boxes that do.
func peerTLSWarnings(proj *project.Project, getenv func(string) string) []string {
	r := proj.Redundancy
	if !r.Pair() {
		return nil
	}
	if peerTLSIsOff(r.PeerTLS) {
		return []string{"redundancy.peer-tls: off: " + peerTLSOff}
	}
	if tok := getenv("NAUTILUS_PEER_TOKEN"); tok != "" && len(tok) < peertls.MinToken {
		return []string{fmt.Sprintf("NAUTILUS_PEER_TOKEN here is %d characters: naut run refuses to start a pair member with it, since peer TLS (on by default) needs at least %d. Draw a new one with `naut redundancy new-pair` and give it to both controllers and the witness", len(tok), peertls.MinToken)}
	}
	return nil
}

// buildPeerTLS is a pair member's or the witness's peer TLS (#710): nil
// when off. offHint is how this program turns it off, for the refusal of
// a short token.
func buildPeerTLS(token string, off bool, offHint string) (*peertls.Pair, error) {
	if off {
		return nil, nil
	}
	p, err := peertls.New(token)
	var short *peertls.ShortTokenError
	if errors.As(err, &short) {
		return nil, fmt.Errorf("NAUTILUS_PEER_TOKEN is %d characters, and peer TLS (on by default) needs at least %d: its key is derived from the token, and anyone who reaches a peer port could test guesses at a short one offline. Draw a new one with `naut redundancy new-pair` and give it to both controllers and the witness (or, on a lab bench only, set %s)", short.Len, peertls.MinToken, offHint)
	}
	return p, err
}
