package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/peertls"
	"github.com/joyautomation/nautilus/quorum/node"
)

// testKeyBox is a known box for a test member's peer key.
func testKeyBox([]string) (peertls.Box, error) {
	return peertls.Box{MachineID: "test-box", NICs: []string{"02:00:00:00:00:01"}}, nil
}

// memberPeer is a fresh peer TLS identity for member self of cluster
// (its own box key in a temporary directory): a test that calls a peer
// port directly calls as a member, whose key is pinned at first contact.
func memberPeer(t *testing.T, token, cluster, self string) *peertls.Peer {
	t.Helper()
	dir := t.TempDir()
	p, err := peertls.New(peertls.Options{Token: token, Cluster: cluster, Self: self,
		KeyFile: filepath.Join(dir, peerKeyFile), Box: func() (peertls.Box, error) { return testKeyBox(nil) },
		PinsFile: func(string) string { return filepath.Join(dir, peerPinsFile) },
		Accept:   func(string, string) bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// wrapClient is an HTTP client calling, with p, the member want at
// whatever address it dials.
func wrapClient(p *peertls.Peer, want string) *http.Client {
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			raw, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return p.Wrap(ctx, raw, want, addr)
		}}}
}

// memberClient calls the member want as a fresh member self of cluster.
func memberClient(t *testing.T, token, cluster, self, want string) *http.Client {
	return wrapClient(memberPeer(t, token, cluster, self), want)
}

// peerHTTPAs is how the rig calls member to's peer port as member from:
// with from's own peer TLS (its pinned key), as from's own calls go.
// Plain HTTP on a plain rig.
func (r *pairRig) peerHTTPAs(from, to string) *http.Client {
	if r.plain {
		return http.DefaultClient
	}
	if to == "w" {
		to = peertls.Witness
	}
	return wrapClient(r.nodes[from].m.peerTLS, to)
}

var _ = node.WithPeer

// unpin stands in for `naut redundancy unpin <name>` on the survivors
// (#710 2b) after the rig wipes name's state directory or rebuilds the
// witness: the pin of name is dropped at every other member, running or
// not (in its pins file then), so name's new key pins at its next contact.
func (r *pairRig) unpin(name string) {
	r.t.Helper()
	unpinIn(r.t, r.cluster(), name, r.nodes, r.env, r.witDir)
}

// unpinIn drops name's pin at the controllers of nodes other than name
// and, for a controller's name, at the witness kept in witDir.
func unpinIn(t *testing.T, cluster, name string, nodes map[string]*pairNode, env map[string]map[string]string, witDir string) {
	t.Helper()
	if name == "w" {
		name = peertls.Witness
	}
	for _, n := range []string{"a", "b"} {
		if n == name {
			continue
		}
		if nd := nodes[n]; nd != nil && nd.m != nil && nd.m.peerTLS != nil {
			if err := nd.m.peerTLS.Drop("", name, "test: the rig's stand-in for naut redundancy unpin"); err != nil {
				t.Fatal(err)
			}
			continue
		}
		dropPinFile(t, filepath.Join(env[n]["STATE_DIRECTORY"], peerPinsFile), name)
	}
	if name == peertls.Witness {
		return
	}
	if p, ok := witnessPeers.Load(witDir); ok {
		if err := p.(*peertls.Peer).Drop(cluster, name, "test: the rig's stand-in for naut redundancy unpin"); err != nil {
			t.Fatal(err)
		}
		return
	}
	dropPinFile(t, filepath.Join(witDir, cluster+"."+peerPinsFile), name)
}

// dropPinFile removes name from a pins file of a member that is down.
func dropPinFile(t *testing.T, path, name string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		V    int               `json:"v"`
		Pins map[string]string `json:"pins"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	delete(f.Pins, name)
	out, _ := json.Marshal(f)
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
}
