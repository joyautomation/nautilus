package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/quorum/node"
)

// #1029 3c-3c: naut redundancy new-pair, commission, recommission and
// recommission --from-spare, end to end.

// new-pair writes each box's env lines, mode 0600, and prints the paths and
// what is not secret, never the token; it never overwrites a file. With
// --print-secrets it prints them all and writes nothing.
func TestNewPairWritesEachBoxsEnvFile(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if code := redundancyMain([]string{"new-pair", "--dir", dir}, func(string) string { return "" }, &out, &out); code != 0 {
		t.Fatalf("new-pair: exit %d\n%s", code, out.String())
	}
	read := func(f string) map[string]string {
		t.Helper()
		p := filepath.Join(dir, f)
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v, want 0600", f, fi.Mode().Perm())
		}
		b, _ := os.ReadFile(p)
		env := map[string]string{}
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			k, v, _ := strings.Cut(l, "=")
			env[k] = v
		}
		return env
	}
	a, b, w := read("a.env"), read("b.env"), read("witness.env")
	tok := a["NAUTILUS_PEER_TOKEN"]
	switch {
	case a["NAUTILUS_NODE"] != "a" || b["NAUTILUS_NODE"] != "b":
		t.Fatalf("NAUTILUS_NODE: a %q, b %q", a["NAUTILUS_NODE"], b["NAUTILUS_NODE"])
	case !node.ValidCluster(a["NAUTILUS_CLUSTER"]) || b["NAUTILUS_CLUSTER"] != a["NAUTILUS_CLUSTER"]:
		t.Fatalf("NAUTILUS_CLUSTER: a %q, b %q", a["NAUTILUS_CLUSTER"], b["NAUTILUS_CLUSTER"])
	case !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(tok) || b["NAUTILUS_PEER_TOKEN"] != tok || w["NAUTILUS_PEER_TOKEN"] != tok || len(w) != 1:
		t.Fatalf("the token: a %q, b %q, witness %v", tok, b["NAUTILUS_PEER_TOKEN"], w)
	case strings.Contains(out.String(), tok):
		t.Fatalf("the token was printed:\n%s", out.String())
	case !strings.Contains(out.String(), a["NAUTILUS_CLUSTER"]) || !strings.Contains(out.String(), "naut redundancy commission"):
		t.Fatalf("the output doesn't give the cluster ID and the next step:\n%s", out.String())
	}
	before, _ := os.ReadFile(filepath.Join(dir, "a.env"))
	out.Reset()
	if code := redundancyMain([]string{"new-pair", "--dir", dir}, func(string) string { return "" }, &out, &out); code != 1 || !strings.Contains(out.String(), "never overwrites") {
		t.Fatalf("new-pair over existing files: exit %d\n%s", code, out.String())
	}
	if after, _ := os.ReadFile(filepath.Join(dir, "a.env")); !bytes.Equal(after, before) {
		t.Fatal("a.env was overwritten")
	}
	other := t.TempDir()
	out.Reset()
	if code := redundancyMain([]string{"new-pair", "--dir", other, "--print-secrets"}, func(string) string { return "" }, &out, &out); code != 0 ||
		!regexp.MustCompile(`NAUTILUS_PEER_TOKEN=[0-9a-f]{64}`).MatchString(out.String()) {
		t.Fatalf("--print-secrets: exit %d\n%s", code, out.String())
	}
	if files, _ := os.ReadDir(other); len(files) != 0 {
		t.Fatalf("--print-secrets wrote %v", files)
	}
}

// commission through the CLI on a new pair: once every member has
// settled, it commissions, prints the cluster and the leader; a second
// run, from the other controller, is refused (b is commissioned now).
func TestCommissionThroughTheCLI(t *testing.T) {
	r := newBoxRigBare(t, true)
	var out string
	pairWait(t, "commission to go through", 15*time.Second, func() bool {
		var code int
		code, out = r.cli("a", "commission")
		return code == 0
	})
	if !strings.Contains(out, "commissioned cluster "+r.env["a"]["NAUTILUS_CLUSTER"]) {
		t.Fatalf("commission:\n%s", out)
	}
	pairWait(t, "both commissioned and a leader", 15*time.Second, func() bool {
		sa, sb := r.el("a").Status(), r.el("b").Status()
		return sa.Commissioned && sb.Commissioned && (sa.Leading || sb.Leading)
	})
	if code, out := r.cli("b", "commission"); code != 1 || !strings.Contains(out, "REFUSED, nothing changed") || !strings.Contains(out, "is commissioned") {
		t.Fatalf("commission again: exit %d\n%s", code, out)
	}
}

// wipeBox empties member's state directory: its voter state and its
// operator log, a disk replaced or reinstalled.
func (r *boxRig) wipeBox(member string) {
	r.t.Helper()
	files, _ := filepath.Glob(filepath.Join(r.env[member]["STATE_DIRECTORY"], "*"))
	for _, f := range files {
		if err := os.RemoveAll(f); err != nil {
			r.t.Fatal(err)
		}
	}
	// Its peer key went with the disk (#710 per-box keys): the survivors
	// drop its pin, as an operator's unpin does.
	unpinIn(r.t, r.env[member]["NAUTILUS_CLUSTER"], member, r.nodes, r.env, r.wdir)
}

// restartOn starts member again on its peer address, with a tag API.
func (r *boxRig) restartOn(member, box string, env map[string]string, i int) {
	r.t.Helper()
	ln, err := net.Listen("tcp", r.lns[i].Addr().String())
	if err != nil {
		r.t.Fatal(err)
	}
	r.withAPI(member)
	r.start(member, box, env, ln)
}

var floorAbove = regexp.MustCompile(`a floor above it: (\d+)`)

// Both controllers lost their disks: recommission without --floor prints
// each member's terms and a floor above them, and changes nothing; with
// that floor it re-commissions, and the pair leads again.
func TestRecommissionThroughTheCLIPrintsTheEvidence(t *testing.T) {
	r := newBoxRigAPI(t)
	// Both down before either comes back: a wiped a restarted while b
	// still runs re-adopts b's incarnation (#976) within milliseconds, so
	// the disks are not both lost (a CI flake, #1199's run).
	for _, n := range []string{"a", "b"} {
		r.nodes[n].crash()
	}
	for i, n := range []string{"a", "b"} {
		r.wipeBox(n)
		r.restartOn(n, "box-"+n, r.env[n], i)
	}
	var out string
	pairWait(t, "recommission to print the evidence", 15*time.Second, func() bool {
		_, out = r.cli("a", "recommission")
		return strings.Contains(out, "give a floor above")
	})
	m := floorAbove.FindStringSubmatch(out)
	if m == nil || !strings.Contains(out, "the floor's evidence:") || !strings.Contains(out, "witness") {
		t.Fatalf("no evidence:\n%s", out)
	}
	if r.el("a").Status().Commissioned || r.el("a").Status().Floor != 0 {
		t.Fatal("recommission without a floor changed a")
	}
	if code, out := r.cli("a", "recommission", "--floor", m[1]); code != 0 || !strings.Contains(out, "re-commissioned") {
		t.Fatalf("recommission --floor %s: exit %d\n%s", m[1], code, out)
	}
	pairWait(t, "the pair to lead again", 20*time.Second, func() bool {
		sa, sb := r.el("a").Status(), r.el("b").Status()
		return sa.Commissioned && sb.Commissioned && (sa.Leading || sb.Leading)
	})
}

// The real box made a spare (its copy ran first) comes back with
// recommission --from-spare (#1029 3c-3c), in place of the guide's manual
// steps: refused without --copy-stopped, refused while the copy runs
// (both other voters must hear no process named a), then, the copy
// stopped, done; restarted, it re-adopts from b, unheld, its operator log
// synced, and leads. The copy, switched back on with its old disk, carries
// a state from before the command: fenced, it never leads and is told so
// (#1029 3c-3c-1b).
func TestTheRealBoxMadeASpareComesBackWithFromSpare(t *testing.T) {
	r := newBoxRigAPI(t)
	r.handLeadTo("b")
	r.nodes["a"].crash()
	copyEnv := r.cloneDiskEnv("a", "box-a")
	time.Sleep(2 * time.Second) // four leases: the copy is the a everyone hears
	r.restartOn("a", "box-a", r.env["a"], 0)
	pairWait(t, "the real a to go spare beside the copy heard first", 10*time.Second, func() bool { return r.el("a").Spare() })

	if code, out := r.cli("a", "recommission", "--from-spare"); code != 1 || !strings.Contains(out, "--copy-stopped") {
		t.Fatalf("without --copy-stopped: exit %d\n%s", code, out)
	}
	if code, out := r.cli("a", "recommission", "--from-spare", "--copy-stopped"); code != 1 || !strings.Contains(out, "REFUSED, nothing changed") || !strings.Contains(out, "a process named a") {
		t.Fatalf("with the copy running: exit %d\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(r.env["a"]["STATE_DIRECTORY"], pairStateFile)); err != nil {
		t.Fatalf("a refused from-spare moved the state file: %v", err)
	}
	r.nodes["clone"].crash()
	var out string
	pairWait(t, "from-spare to go through", 10*time.Second, func() bool {
		var code int
		code, out = r.cli("a", "recommission", "--from-spare", "--copy-stopped")
		return code == 0
	})
	if !strings.Contains(out, "NOW RESTART naut") || !strings.Contains(out, "set aside") {
		t.Fatalf("from-spare:\n%s", out)
	}
	if !r.el("a").Spare() {
		t.Fatal("the spare spoke before its restart")
	}
	r.nodes["a"].stop()
	r.restartOn("a", "box-a", r.env["a"], 0)
	pairWait(t, "a to re-adopt from b", 15*time.Second, func() bool {
		s := r.el("a").Status()
		return s.Commissioned && !s.Spare && !s.Held
	})
	r.handLeadTo("a")
	// The log is left as it was (the real box's own, silent since it went
	// spare): synced, and level with b's once a leads.
	pairWait(t, "a's operator log to be level with b's", 15*time.Second, func() bool {
		st := r.nodes["a"].m.log.Status()
		return !st.Unsynced && st.LastSeq == r.nodes["b"].m.log.Status().LastSeq
	})

	// The copy switched back on, from its old disk: a fresh boot.
	ln, err := net.Listen("tcp", r.lns[2].Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	r.start("clone", "box-a", copyEnv, ln)
	led := watchOneLeader(t, 6*time.Second, map[string]*node.Elector{"a": r.el("a"), "b": r.el("b"), "clone": r.el("clone")})
	if led["clone"] {
		t.Fatal("the copy, back on, led")
	}
	// Its state is from before a was replaced: b and the witness refuse
	// it (the fence, #1029 3c-3c-1b), so it is told so and campaigns no
	// more; it is never heard beside a, so it is not marked a copy.
	pairWait(t, "the copy to learn it is fenced", 10*time.Second, func() bool { return r.el("clone").Status().Superseded != "" })
	if !r.el("a").IsLeader() {
		t.Fatal("the member stopped leading when the copy came back")
	}
}

// recommission prints what its live-copy check could not check (a member
// that did not answer), done or refused (#1029 3c-3c-2b, naut-lead's
// ruling: tell the person exactly what wasn't checked).
func TestRecommissionPrintsWhatTheLiveCopyCheckMissed(t *testing.T) {
	for _, outcome := range []string{"done", "refused"} {
		body := `{"outcome":"` + outcome + `","error":"x","report":{"members":[{"member":"a"}],"liveUnchecked":["b did not answer: a live copy of a reachable only through b can't be ruled out; check that the old box is off"]}}`
		var out bytes.Buffer
		printRecommissionAnswer(&out, 200, []byte(body))
		if !strings.Contains(out.String(), "not checked: b did not answer") {
			t.Errorf("%s: the unchecked peer is not printed:\n%s", outcome, out.String())
		}
	}
}
