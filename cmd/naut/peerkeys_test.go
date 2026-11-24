package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/internal/peertls"
	"github.com/joyautomation/nautilus/leader"
	"github.com/joyautomation/nautilus/quorum/node"
)

// fakeNet builds a /sys/class/net tree: name -> MAC, physical when it has
// a device link; perm, a bond slave's permanent address.
func fakeNet(t *testing.T, root string, ifs map[string]struct {
	mac      string
	physical bool
	perm     string
}) {
	t.Helper()
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	for name, i := range ifs {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(dir, "address"), []byte(i.mac+"\n"), 0o644)
		if i.physical {
			_ = os.MkdirAll(filepath.Join(dir, "device"), 0o755)
		}
		if i.perm != "" {
			_ = os.MkdirAll(filepath.Join(dir, "bonding_slave"), 0o755)
			_ = os.WriteFile(filepath.Join(dir, "bonding_slave", "perm_hwaddr"), []byte(i.perm+"\n"), 0o644)
		}
	}
}

type nic = struct {
	mac      string
	physical bool
	perm     string
}

// The peer key's box is the machine-id and the physical NICs only (#710
// per-box keys): installing docker, a bridge with eth0 moved into it, a
// VPN tun, a veth, a USB NIC for maintenance keeps the key; the box cloned
// onto other hardware makes a new one.
func TestThePeerKeyIgnoresVirtualInterfaces(t *testing.T) {
	root := filepath.Join(t.TempDir(), "net")
	fakeNet(t, root, map[string]nic{
		"lo":    {mac: "00:00:00:00:00:00"},
		"eth0":  {mac: "02:aa:00:00:00:01", physical: true},
		"eth1":  {mac: "02:aa:00:00:00:01", physical: true, perm: "02:aa:00:00:00:02"}, // a bond slave
		"bond0": {mac: "02:aa:00:00:00:01"},
	})
	before, err := physicalNICs(root)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(before)
	if want := []string{"02:aa:00:00:00:01", "02:aa:00:00:00:02"}; !slices.Equal(before, want) {
		t.Fatalf("physical NICs %v, want %v", before, want)
	}
	dir := t.TempDir()
	box := peertls.Box{MachineID: "mid", NICs: before}
	key := func() string {
		t.Helper()
		p, err := peertls.New(peertls.Options{Token: testPeerToken, Cluster: node.NewClusterID(), Self: "a",
			KeyFile: filepath.Join(dir, peerKeyFile), PinsFile: func(string) string { return filepath.Join(dir, peerPinsFile) },
			Box: func() (peertls.Box, error) { return box, nil }, Accept: func(string, string) bool { return false }})
		if err != nil {
			t.Fatal(err)
		}
		fp, err := p.Fingerprint()
		if err != nil {
			t.Fatal(err)
		}
		return fp
	}
	first := key()
	// docker, eth0 moved into a bridge (br0 takes eth0's MAC), a VPN, a
	// veth, and a USB NIC plugged in for maintenance.
	fakeNet(t, root, map[string]nic{
		"lo":      {mac: "00:00:00:00:00:00"},
		"eth0":    {mac: "02:aa:00:00:00:01", physical: true},
		"eth1":    {mac: "02:aa:00:00:00:01", physical: true, perm: "02:aa:00:00:00:02"},
		"bond0":   {mac: "02:aa:00:00:00:01"},
		"br0":     {mac: "02:aa:00:00:00:01"},
		"docker0": {mac: "02:42:ac:11:00:01"},
		"veth9":   {mac: "06:11:22:33:44:55"},
		"tun0":    {},
		"usb0":    {mac: "02:bb:00:00:00:09", physical: true},
	})
	after, err := physicalNICs(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range after {
		if m == "02:42:ac:11:00:01" || m == "06:11:22:33:44:55" {
			t.Fatalf("a virtual interface's MAC counts: %v", after)
		}
	}
	box = peertls.Box{MachineID: "mid", NICs: after}
	if key() != first {
		t.Fatal("adding docker, a bridge, a VPN and a USB NIC made a new peer key")
	}
	// The disk cloned onto other hardware: the same machine-id, other NICs.
	box = peertls.Box{MachineID: "mid", NICs: []string{"02:cc:00:00:00:01"}}
	if key() == first {
		t.Fatal("a clone on other hardware kept the peer key")
	}
}

// memJournal is an audit journal in memory.
type memJournal struct {
	mu sync.Mutex
	es []audit.Entry
}

func (j *memJournal) Record(e audit.Entry) (audit.Entry, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.es = append(j.es, e)
	return e, nil
}

func (j *memJournal) entries(action string) []audit.Entry {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []audit.Entry
	for _, e := range j.es {
		if e.Action == action {
			out = append(out, e)
		}
	}
	return out
}

// A member's first contacts come before naut run attaches the journal:
// those events wait for it, in order, and later ones go straight in.
func TestPinEventsWaitForTheJournal(t *testing.T) {
	l := newPinLog("redundancy")
	at := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	l.event(peertls.Event{Kind: peertls.EventNewKey, Name: "a", Fingerprint: "SHA256:self", Time: at})
	l.event(peertls.Event{Kind: peertls.EventPinned, Cluster: "c", Name: "b", Fingerprint: "SHA256:b", Addr: "10.0.0.2:9450", Time: at})
	j := &memJournal{}
	l.attachJournal(j)
	l.event(peertls.Event{Kind: peertls.EventPinned, Name: "witness", Fingerprint: "SHA256:w", Time: at})
	es := j.entries("redundancy.peer-pin")
	if len(es) != 3 || es[0].Target != "a" || es[1].Target != "b" || es[2].Target != "witness" {
		t.Fatalf("journal: %+v", es)
	}
	d := es[1].Detail
	if d["fingerprint"] != "SHA256:b" || d["addr"] != "10.0.0.2:9450" || d["kind"] != "pinned" || d["time"] == "" || es[1].Result != audit.ResultOK {
		t.Fatalf("the pin's entry: %+v", es[1])
	}
}

// A refusal raises a condition naming the member, its address and the
// unpin; it clears once that name stops being refused. An unreadable pins
// file's condition stands until every name is pinned again.
func TestPinConditions(t *testing.T) {
	defer func(d time.Duration) { refusedQuiet = d }(refusedQuiet)
	refusedQuiet = 50 * time.Millisecond
	var mu sync.Mutex
	conds := map[string]string{}
	set := func(k, m string) {
		mu.Lock()
		defer mu.Unlock()
		if m == "" {
			delete(conds, k)
		} else {
			conds[k] = m
		}
	}
	get := func(k string) string { mu.Lock(); defer mu.Unlock(); return conds[k] }
	settled := false
	l := newPinLog("redundancy")
	l.event(peertls.Event{Kind: peertls.EventPinsReset, Detail: "the pins file could not be read"})
	l.attachConditions(set, func() bool { return settled })
	if !strings.Contains(get("peer-pins-reset"), "could not be read") {
		t.Fatalf("the reset's condition: %v", conds)
	}
	l.event(peertls.Event{Kind: peertls.EventRefused, Name: "a", Addr: "10.0.0.1:5000", Time: time.Now(),
		Detail: "run `naut redundancy unpin a` on the surviving controller"})
	if c := get("peer-key-refused/a"); !strings.Contains(c, "10.0.0.1:5000") || !strings.Contains(c, "naut redundancy unpin a") {
		t.Fatalf("the refusal's condition: %q", c)
	}
	l.event(peertls.Event{Kind: peertls.EventPinned, Name: "witness"})
	settled = true
	l.event(peertls.Event{Kind: peertls.EventPinned, Name: "b"})
	// It stands past the re-pinning, saying so: an operator checks the
	// fingerprints (a token holder may have answered first).
	if c := get("peer-pins-reset"); !strings.Contains(c, "pinned again") || !strings.Contains(c, "naut redundancy status") {
		t.Fatalf("the reset's condition after the re-pinning: %q", c)
	}
	// A failed pin write stands until that name pins.
	l.event(peertls.Event{Kind: peertls.EventPinFailed, Name: "b", Detail: "the first contact's pin could not be written (disk full)"})
	if !strings.Contains(get("peer-pin-write/b"), "disk full") {
		t.Fatalf("the failed pin's condition: %v", conds)
	}
	l.event(peertls.Event{Kind: peertls.EventPinned, Name: "b"})
	if get("peer-pin-write/b") != "" {
		t.Fatal("the failed pin's condition stood after the pin")
	}
	// A refusal just before the quiet time ends keeps its condition.
	time.Sleep(refusedQuiet * 3 / 4)
	l.event(peertls.Event{Kind: peertls.EventRefused, Name: "a", Addr: "10.0.0.1:5001", Time: time.Now(), Detail: "again"})
	time.Sleep(refusedQuiet / 2)
	if get("peer-key-refused/a") == "" {
		t.Fatal("the first refusal's timer cleared the second refusal's condition")
	}
	pairWait(t, "the refusal to clear", 2*time.Second, func() bool { return get("peer-key-refused/a") == "" })
}

// The state stream, its acknowledgement, the hand-back and the
// switch-ask name no sender: they are taken from the peer only, by its
// certificate (#710 per-box keys); without TLS (the lab opt-out) there is
// nothing to bind.
func TestStreamRequestsComeFromThePeer(t *testing.T) {
	cluster := node.NewClusterID()
	h := peerOnly(cluster, "a", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	for _, c := range []struct {
		name          string
		tls           bool
		clust, sender string
		want          int
	}{
		{"the peer", true, cluster, "a", http.StatusTeapot},
		{"this controller's own name", true, cluster, "b", http.StatusForbidden},
		{"another cluster's peer", true, node.NewClusterID(), "a", http.StatusForbidden},
		{"TLS without a verified member", true, "", "", http.StatusForbidden},
		{"peer TLS off", false, "", "", http.StatusTeapot},
	} {
		r := httptest.NewRequest(http.MethodGet, "/x", nil)
		if c.tls {
			r.TLS = &tls.ConnectionState{}
			if c.sender != "" {
				r = node.WithPeer(r, c.clust, c.sender)
			}
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s: %d, want %d", c.name, w.Code, c.want)
		}
	}
}

// Each member shows its own key's fingerprint, and the ones it pinned for
// the others, on /api/cluster and in naut redundancy status: on each box,
// the fingerprint the others pinned is that box's own, and every first
// contact is journalled with it.
func TestPairPeerKeysShowInStatus(t *testing.T) {
	r := newPairRig(t)
	r.leader()
	j := &memJournal{}
	r.nodes["a"].m.SetJournal(j)
	keys := func(n string) *leader.PeerKeys {
		k := r.nodes[n].m.q.Status().PeerKeys
		if k == nil {
			t.Fatalf("%s's status has no peer keys", n)
		}
		return k
	}
	pairWait(t, "every member pinned at both controllers", 10*time.Second, func() bool {
		return len(keys("a").Pinned) == 2 && len(keys("b").Pinned) == 2
	})
	a, b := keys("a"), keys("b")
	// The witness is pinned under the name of the incarnation the pair
	// counts (#710 2b).
	wn := peertls.WitnessName(r.elector("a").Status().Incarnation)
	if a.Self == "" || a.Self != b.Pinned["a"] || b.Self != a.Pinned["b"] || a.Pinned[wn] == "" || a.Pinned[wn] != b.Pinned[wn] {
		t.Fatalf("a %+v, b %+v: each box's own key must be what the other pinned", a, b)
	}
	// The witness pinned the controllers' own keys too.
	if w := witnessPinned(t, r.witDir, r.cluster()); w["a"] != a.Self || w["b"] != b.Self {
		t.Fatalf("the witness pinned %v; a is %s, b %s", w, a.Self, b.Self)
	}
	// The events from before the journal, now in it: a's new key and its
	// pins of b and the witness.
	var kinds []string
	for _, e := range j.entries("redundancy.peer-pin") {
		kinds = append(kinds, fmt.Sprint(e.Detail["kind"], ":", e.Target))
	}
	for _, want := range []string{"new-key:a", "pinned:b", "pinned:" + wn} {
		if !slices.Contains(kinds, want) {
			t.Fatalf("a's journal: %v, want %s", kinds, want)
		}
	}
	code, body, _ := r.call("a", "GET", "/api/cluster", "")
	if code != http.StatusOK {
		t.Fatalf("/api/cluster: %d %s", code, body)
	}
	var st leader.Status
	if err := json.Unmarshal([]byte(body), &st); err != nil || st.PeerKeys == nil || st.PeerKeys.Self != a.Self {
		t.Fatalf("/api/cluster peer keys: %v %+v", err, st.PeerKeys)
	}
	var out bytes.Buffer
	printClusterStatus(&out, []byte(body))
	if want := "peer keys: this box " + a.Self + "; pinned: b " + a.Pinned["b"] + ", " + wn + " " + a.Pinned[wn]; !strings.Contains(out.String(), want) {
		t.Fatalf("naut redundancy status:\n%s\nwant the line %q", out.String(), want)
	}
}

// naut-lead's race: a token holder reaches a name's first contact before
// the real member (here, a's pin dropped while a is down, as after an
// unpin). It is pinned; the real member's later contact then fails
// loudly, never silently: refused at b and the witness by name, a
// condition on both ends, and a journal entry at b with both
// fingerprints.
func TestPairATokenHolderFirstLocksOutTheRealMemberLoudly(t *testing.T) {
	r := newPairRig(t)
	r.leader()
	j := &memJournal{}
	r.nodes["b"].m.SetJournal(j)
	realKey := r.nodes["a"].m.q.Status().PeerKeys.Self
	r.nodes["a"].crash()
	r.unpinOn("b", "a")
	imp := memberPeer(t, r.token, r.cluster(), "a")
	impKey, _ := imp.Fingerprint()
	ask := func(c *http.Client, url string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		req.Header.Set(node.TokenHeader, r.token)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("the token holder's first contact: %v", err)
		}
		resp.Body.Close()
	}
	ask(wrapClient(imp, "b"), "https://"+r.peer["b"]+node.Prefix+r.cluster()+"/hello?c=0123456789abcdef&from=a")
	ask(wrapClient(imp, peertls.Witness), "https://"+r.wit+node.Prefix+r.cluster()+"/log/api/quorum")
	if got := r.nodes["b"].m.peerTLS.Pinned("")["a"]; got != impKey {
		t.Fatalf("b pinned a as %s, want the token holder's %s", got, impKey)
	}
	r.restart("a")
	pairWait(t, "a's refusal to stand on both ends", 15*time.Second, func() bool {
		aw := strings.Join(r.nodes["a"].m.q.Status().Warnings, "\n")
		bw := strings.Join(r.nodes["b"].m.q.Status().Warnings, "\n")
		return strings.Contains(aw, "naut redundancy unpin a") && strings.Contains(bw, "refused a's peer TLS key")
	})
	var refused []audit.Entry
	for _, e := range j.entries("redundancy.peer-pin") {
		if e.Detail["kind"] == "refused" && e.Target == "a" {
			refused = append(refused, e)
		}
	}
	if len(refused) == 0 || refused[0].Result != audit.ResultRefused || refused[0].Detail["fingerprint"] != realKey || refused[0].Detail["pinned"] != impKey || refused[0].Detail["addr"] == nil {
		t.Fatalf("b's journal of the refusal: %+v", refused)
	}
	if s := r.elector("a").Status(); s.Leading {
		t.Fatal("the refused member leads")
	}
}
