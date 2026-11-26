package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
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

// unpinOn is naut redundancy unpin <name> on the surviving controller
// (#710 2b), through its Quorum as the tag API runs it: after <name>'s
// box was replaced or its state directory lost. The command end to end
// (the CLI, the tag API, the audit seam) is TestUnpinThroughTheCLI.
func (r *pairRig) unpinOn(survivor, name string) unpinReport {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rep, outcome, reason := r.nodes[survivor].m.q.Unpin(ctx, name, "op")
	if outcome != "done" {
		r.t.Fatalf("naut redundancy unpin %s on %s: %s: %s (%+v)", name, survivor, outcome, reason, rep)
	}
	return rep.(unpinReport)
}

// witnessUnpinIn is naut witness unpin <names> on the witness box, the
// witness stopped (the caller stops and starts it): the way out when no
// controller can ask, such as both controllers' disks lost.
func witnessUnpinIn(t *testing.T, witDir, cluster string, names ...string) {
	t.Helper()
	for _, n := range names {
		var out bytes.Buffer
		if code := witnessMain(context.Background(), []string{"unpin", "--state", witDir, "--cluster", cluster, n}, func(string) string { return "" }, &out, &out, nil); code != 0 {
			t.Fatalf("naut witness unpin %s: exit %d\n%s", n, code, out.String())
		}
	}
}

// witnessUnpinOffline is witnessUnpinIn with the rig's witness stopped and
// started again around it.
func (r *pairRig) witnessUnpinOffline(names ...string) {
	r.t.Helper()
	r.stopWitness()
	witnessUnpinIn(r.t, r.witDir, r.cluster(), names...)
	r.startWitness(r.wit)
}

// witnessRefused is the witness's refused (unpinned) keys of cluster.
func witnessRefused(t *testing.T, witDir, cluster string) map[string]string {
	t.Helper()
	return witnessPinsField(t, witDir, cluster, "dropped")
}

// witnessPinned is the witness's pins of cluster, read from its pins file
// (name to fingerprint).
func witnessPinned(t *testing.T, witDir, cluster string) map[string]string {
	t.Helper()
	return witnessPinsField(t, witDir, cluster, "pins")
}

// witnessPinsField is one name-to-key map of the witness's pins file of
// cluster, as fingerprints.
func witnessPinsField(t *testing.T, witDir, cluster, field string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(witDir, cluster+"."+peerPinsFile))
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]json.RawMessage
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	if raw, ok := f[field]; ok {
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
	}
	out := map[string]string{}
	for n, k := range m {
		raw, err := base64.StdEncoding.DecodeString(k)
		if err != nil {
			t.Fatal(err)
		}
		out[n] = peertls.Fingerprint(ed25519.PublicKey(raw))
	}
	return out
}
