package node

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/joyautomation/nautilus/internal/peertls"
	"github.com/joyautomation/nautilus/quorum"
)

// newTestPeer is member self's peer TLS (#710 per-box keys) as naut run
// builds it: its box key and pins in dir, its listener taking calls from
// its peer of cluster (a controller) or from a or b of any cluster (the
// witness).
func newTestPeer(t testing.TB, token, cluster string, self quorum.NodeID, dir string) *peertls.Peer {
	t.Helper()
	return newTestPeerInc(t, token, cluster, self, dir, nil)
}

// newTestPeerInc is newTestPeer whose witness names each pair's
// incarnation by inc (witness@0 for every pair when nil).
func newTestPeerInc(t testing.TB, token, cluster string, self quorum.NodeID, dir string, inc func(string) uint64) *peertls.Peer {
	t.Helper()
	if inc == nil {
		inc = func(string) uint64 { return 0 }
	}
	name, cl := string(self), cluster
	peer := map[quorum.NodeID]string{"a": "b", "b": "a"}[self]
	accept := func(c, n string) bool { return c == cluster && n == peer }
	pins := func(string) string { return filepath.Join(dir, "peer-pins.json") }
	if self == WitnessID {
		name, cl = peertls.Witness, ""
		accept = func(c, n string) bool { return ValidCluster(c) && (n == "a" || n == "b") }
		pins = func(c string) string { return filepath.Join(dir, c+".peer-pins.json") }
	}
	box := peertls.Box{MachineID: "box-" + string(self)}
	p, err := peertls.New(peertls.Options{Token: token, Cluster: cl, Self: name, KeyFile: filepath.Join(dir, "peer-key.json"),
		Box: func() (peertls.Box, error) { return box, nil }, PinsFile: pins, Accept: accept, Incarnation: inc})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// wrapAs is p's TLSWrap: the member at each dialed address by nameOf.
func wrapAs(p *peertls.Peer, nameOf func(addr string) (quorum.NodeID, bool)) TLSWrap {
	return func(ctx context.Context, raw net.Conn, addr string) (net.Conn, error) {
		id, ok := nameOf(addr)
		if !ok {
			raw.Close()
			return nil, fmt.Errorf("no member at %s", addr)
		}
		name := string(id)
		if id == WitnessID {
			name = peertls.Witness
		}
		return p.Wrap(ctx, raw, name, addr)
	}
}

// wrapTo is p's TLSWrap for calls to the one member id.
func wrapTo(p *peertls.Peer, id quorum.NodeID) TLSWrap {
	return wrapAs(p, func(string) (quorum.NodeID, bool) { return id, true })
}

// bindPeer serves h behind p's per-request check, as naut run does.
func bindPeer(p *peertls.Peer, h http.Handler) http.Handler { return p.Bind(h, WithPeer) }
