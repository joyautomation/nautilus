package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/peertls"
	"github.com/joyautomation/nautilus/quorum/node"
)

// An unpin by mistake (#710 2b): a's box did not change, yet b unpins it.
// a's key stays refused, at b and at the witness, across b's restart (the
// pins file), so a stays out; the second unpin lifts the refusal at both,
// says the lead's sentence, and a is pinned again and rejoins.
func TestPairASecondUnpinLiftsTheRefusal(t *testing.T) {
	r := newPairRig(t)
	r.leader()
	aKey := r.nodes["a"].m.q.Status().PeerKeys.Self
	r.nodes["a"].crash()
	first := r.unpinOn("b", "a")
	if first.Here != string(peertls.Dropped) || first.Witness != string(peertls.Dropped) || first.Lifted {
		t.Fatalf("the first unpin: %+v", first)
	}
	if !strings.Contains(first.Message, "Start the replacement now") {
		t.Fatalf("the first unpin's text: %s", first.Message)
	}
	r.nodes["b"].crash()
	r.restart("b") // the refusal is in b's pins file
	r.restart("a") // the same box, the same key
	pairWait(t, "b to refuse a's dropped key", 15*time.Second, func() bool {
		return strings.Contains(strings.Join(r.nodes["b"].m.q.Status().Warnings, "\n"), "pin was dropped")
	})
	if got := r.nodes["b"].m.peerTLS.Refused("")["a"]; got != aKey {
		t.Fatalf("b refuses %q for a, want a's key %s", got, aKey)
	}
	if w := witnessPinned(t, r.witDir, r.cluster()); w["a"] != "" {
		t.Fatalf("the witness pinned a again: %v", w)
	}
	// A second unpin whose lift fails at the witness (here: its pins file
	// can't be written; its "already dropped" answer needs no write) lifts
	// nothing here either: the witness's refusal goes first, so a rerun
	// finds this one still here.
	if err := os.Chmod(r.witDir, 0o500); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, outcome, _ := r.nodes["b"].m.q.Unpin(ctx, "a", "op")
	if err := os.Chmod(r.witDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if outcome != "incomplete" {
		t.Fatalf("a second unpin whose witness lift fails: %s", outcome)
	}
	if r.nodes["b"].m.peerTLS.Refused("")["a"] != aKey {
		t.Fatal("the refusal here was lifted while the witness's stands (row lift_here_first)")
	}
	second := r.unpinOn("b", "a")
	if !second.Lifted || !strings.Contains(second.Message, unpinLiftedText) {
		t.Fatalf("the second unpin: %+v", second)
	}
	pairWait(t, "a pinned again at b and the witness", 15*time.Second, func() bool {
		return r.nodes["b"].m.peerTLS.Pinned("")["a"] == aKey && witnessPinned(t, r.witDir, r.cluster())["a"] == aKey
	})
}

// A retry after a witness that was not reached never lifts (#710 2b): the
// first unpin drops b's pin of a and fails at the witness; the second
// drops it at the witness and keeps the refusal at b.
func TestPairAnUnpinRetryNeverLifts(t *testing.T) {
	r := newPairRig(t)
	r.leader()
	r.nodes["a"].crash()
	r.stopWitness()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rep, outcome, _ := r.nodes["b"].m.q.Unpin(ctx, "a", "op")
	if ur := rep.(unpinReport); outcome != "incomplete" || ur.Here != string(peertls.Dropped) || ur.WitnessErr == "" {
		t.Fatalf("the unpin without the witness: %s %+v", outcome, ur)
	}
	r.startWitness(r.wit)
	var retry unpinReport
	pairWait(t, "the retry to reach the witness", 15*time.Second, func() bool {
		rep, outcome, _ := r.nodes["b"].m.q.Unpin(ctx, "a", "op")
		retry = rep.(unpinReport)
		return outcome == "done"
	})
	if retry.Lifted || retry.Here != string(peertls.AlreadyDropped) || retry.Witness != string(peertls.Dropped) {
		t.Fatalf("the retry: %+v, want the witness's pin dropped and the refusal kept", retry)
	}
	if len(r.nodes["b"].m.peerTLS.Refused("")) != 1 {
		t.Fatalf("b's refusal was lifted by a retry: %v", r.nodes["b"].m.peerTLS.Refused(""))
	}
}

// The witness's side of an unpin (#710 2b): only from a controller of the
// cluster by its certificate, about the other controller, from one pinned
// no later than the one it drops, and only on the asking controller's own
// key of it.
func TestTheWitnessTakesAnUnpinOnlyByItsRules(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cluster, other := node.NewClusterID(), node.NewClusterID()
	dir := t.TempDir()
	wp, err := peertls.New(peertls.Options{Token: token, Self: peertls.Witness, KeyFile: filepath.Join(dir, peerKeyFile),
		Box:         func() (peertls.Box, error) { return testKeyBox(nil) },
		PinsFile:    func(c string) string { return filepath.Join(dir, c+"."+peerPinsFile) },
		Accept:      func(c, n string) bool { return node.ValidCluster(c) && (n == "a" || n == "b") },
		Incarnation: func(string) uint64 { return 7 }})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("POST "+node.Prefix+"{cluster}"+node.UnpinPath, witnessUnpin(wp, func(string) (string, bool) { return "a,b", true }))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: wp.Bind(mux, node.WithPeer)}
	go func() { _ = srv.Serve(tls.NewListener(ln, wp.Server())) }()
	t.Cleanup(func() { srv.Close() })
	type member struct {
		p *peertls.Peer
		c *http.Client
	}
	newM := func(cl, self string) member {
		p := memberPeer(t, token, cl, self)
		return member{p, wrapClient(p, peertls.Witness)}
	}
	fp := func(m member) string { f, _ := m.p.Fingerprint(); return f }
	ask := func(m member, cl, op, name, key string) (int, string) {
		t.Helper()
		b, _ := json.Marshal(node.UnpinAsk{Op: op, Name: name, Key: key})
		resp, err := m.c.Post("https://"+ln.Addr().String()+node.Prefix+cl+node.UnpinPath, "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return resp.StatusCode, buf.String()
	}
	a, b := newM(cluster, "a"), newM(cluster, "b")
	// a's first request pins it (and asks about a name never pinned).
	if code, body := ask(a, cluster, "drop", "b", "x"); code != http.StatusOK || !strings.Contains(body, string(peertls.NotPinned)) {
		t.Fatalf("a's ask about b, pinned nowhere: %d %s", code, body)
	}
	if code, body := ask(b, cluster, "drop", "b", fp(b)); code != http.StatusForbidden || !strings.Contains(body, "can't unpin itself") {
		t.Fatalf("b unpinning itself (row witness_unpin_self): %d %s", code, body)
	}
	oc := newM(other, "a")
	if code, body := ask(oc, cluster, "drop", "b", fp(b)); code != http.StatusForbidden {
		t.Fatalf("another cluster's a unpinning this cluster's b (row unpin_other_cluster): %d %s", code, body)
	}
	if code, body := ask(a, cluster, "drop", "b", "SHA256:not-bs-key"); code != http.StatusOK || !strings.Contains(body, node.UnpinOther) || wp.Pinned(cluster)["b"] != fp(b) {
		t.Fatalf("a dropping b with another key than b's (row unpin_ignores_key): %d %s", code, body)
	}
	// b, the survivor, unpins a: dropped.
	if code, body := ask(b, cluster, "drop", "a", fp(a)); code != http.StatusOK || !strings.Contains(body, string(peertls.Dropped)) {
		t.Fatalf("b unpinning a: %d %s", code, body)
	}
	// A token holder wins a's first contact, then asks to unpin b.
	imp := newM(cluster, "a")
	if code, body := ask(imp, cluster, "drop", "b", fp(b)); code != http.StatusForbidden || !strings.Contains(body, "naut witness unpin") {
		t.Fatalf("a re-pinned after the unpin, unpinning b (row unpin_newer_caller): %d %s", code, body)
	}
	if wp.Pinned(cluster)["b"] != fp(b) {
		t.Fatal("b's pin was dropped")
	}
}

// naut witness unpin (#710 2b): refused while the witness runs; with it
// stopped, it drops the pin keeping the key refused, and a second run
// lifts that refusal.
func TestWitnessUnpinOnTheWitnessBox(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cluster := node.NewClusterID()
	dir := t.TempDir()
	wp, err := peertls.New(peertls.Options{Token: token, Self: peertls.Witness, KeyFile: filepath.Join(dir, peerKeyFile),
		Box:         func() (peertls.Box, error) { return testKeyBox(nil) },
		PinsFile:    func(c string) string { return filepath.Join(dir, c+"."+peerPinsFile) },
		Accept:      func(c, n string) bool { return node.ValidCluster(c) && (n == "a" || n == "b") },
		Incarnation: func(string) uint64 { return 7 }})
	if err != nil {
		t.Fatal(err)
	}
	srvLn, _ := net.Listen("tcp", "127.0.0.1:0")
	srv := &http.Server{Handler: wp.Bind(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), node.WithPeer)}
	go func() { _ = srv.Serve(tls.NewListener(srvLn, wp.Server())) }()
	if _, err := memberClient(t, token, cluster, "a", peertls.Witness).Get("https://" + srvLn.Addr().String() + "/"); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	run := func() (int, string) {
		var out bytes.Buffer
		code := witnessMain(context.Background(), []string{"unpin", "--state", dir, "--cluster", cluster, "a"}, func(string) string { return "" }, &out, &out, nil)
		return code, out.String()
	}
	free, err := node.LockWitnessDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if code, out := run(); code != 1 || !strings.Contains(out, "stop it first") {
		t.Fatalf("while the witness runs: exit %d\n%s", code, out)
	}
	free()
	if code, out := run(); code != 0 || !strings.Contains(out, "stays refused") || !strings.Contains(out, "no third vote") {
		t.Fatalf("the unpin: exit %d\n%s", code, out)
	}
	if code, out := run(); code != 0 || !strings.Contains(out, unpinLiftedText) {
		t.Fatalf("the second unpin: exit %d\n%s", code, out)
	}
}

// The operator-intent log reaches the witness only under the certificate
// of the incarnation the pair adopted (#710 2b): the log counts the
// witness's copy by the incarnation the witness states, and the
// certificate makes it the witness's own word.
func TestTheLogReachesOnlyTheAdoptedWitness(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cluster := node.NewClusterID()
	dir := t.TempDir()
	var inc atomic.Uint64
	inc.Store(8)
	wp, err := peertls.New(peertls.Options{Token: token, Self: peertls.Witness, KeyFile: filepath.Join(dir, peerKeyFile),
		Box:         func() (peertls.Box, error) { return testKeyBox(nil) },
		PinsFile:    func(c string) string { return filepath.Join(dir, c+"."+peerPinsFile) },
		Accept:      func(c, n string) bool { return node.ValidCluster(c) && (n == "a" || n == "b") },
		Incarnation: func(string) uint64 { return inc.Load() }})
	if err != nil {
		t.Fatal(err)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	srv := &http.Server{Handler: wp.Bind(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), node.WithPeer)}
	go func() { _ = srv.Serve(tls.NewListener(ln, wp.Server())) }()
	t.Cleanup(func() { srv.Close() })
	a := memberPeer(t, token, cluster, "a")
	addr := ln.Addr().String()
	dial := func(adopted uint64) error {
		raw, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		wrap := logWrap(a, func(context.Context, net.Conn, string) (net.Conn, error) {
			t.Fatal("not the witness's wrap")
			return nil, nil
		}, addr, func() uint64 { return adopted })
		c, err := wrap(context.Background(), raw, addr)
		if err == nil {
			c.Close()
		}
		return err
	}
	if err := dial(7); err == nil {
		t.Fatal("the log reached incarnation 8 while the pair counts 7 (row log_witness_any_inc)")
	}
	if err := dial(8); err != nil {
		t.Fatalf("the adopted incarnation: %v", err)
	}
	// A connection kept across an adoption of another incarnation ends at
	// its next use, not only at the next dial.
	adopted := atomic.Uint64{}
	adopted.Store(8)
	raw, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	kept, err := logWrap(a, nil, addr, adopted.Load)(context.Background(), raw, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer kept.Close()
	adopted.Store(9)
	if _, err := kept.Write([]byte("GET / HTTP/1.1\r\n\r\n")); err == nil {
		t.Fatal("a log connection to incarnation 8 still writes after the pair adopted 9 (row log_witness_checked_at_dial)")
	}
	inc.Store(0)
	if err := dial(0); err == nil {
		t.Fatal("the log reached witness@0")
	}
	inc.Store(9)
	if err := dial(0); err != nil {
		t.Fatalf("an uncommissioned controller to incarnation 9: %v", err)
	}
}

// naut redundancy unpin end to end (#710 2b): through the CLI, the tag
// API and the audit seam. b's disk is replaced, so its new key is
// refused at a and the witness, and a's condition names the command; the
// unpin on a, the survivor, drops b's pin at both, and b's new key is
// pinned at its first contact. The command refuses a's own name and the
// witness's.
func TestUnpinThroughTheCLI(t *testing.T) {
	r := newBoxRigAPI(t)
	pairWait(t, "a pair", 15*time.Second, func() bool { return r.el("a").IsLeader() || r.el("b").IsLeader() })
	if code, out := r.cli("a", "unpin", "a"); code != 1 || !strings.Contains(out, "on the other controller") {
		t.Fatalf("unpin of a on a: exit %d\n%s", code, out)
	}
	if code, out := r.cli("a", "unpin", "c"); code != 1 || !strings.Contains(out, "the other controller (b)") {
		t.Fatalf("unpin of no member: exit %d\n%s", code, out)
	}
	r.nodes["b"].crash()
	r.wipeBox("b")
	r.restartOn("b", "box-b", r.env["b"], 1)
	pairWait(t, "a to refuse b's new key, naming the command", 15*time.Second, func() bool {
		return r.hasWarning("b", "naut redundancy unpin b") && r.hasWarning("a", "refused b's peer TLS key")
	})
	code, out := r.cli("a", "unpin", "b")
	if code != 0 || !strings.Contains(out, "dropped here (dropped) and at the witness (dropped)") {
		t.Fatalf("unpin of b on a: exit %d\n%s", code, out)
	}
	newKey := ""
	pairWait(t, "b's new key pinned at a", 15*time.Second, func() bool {
		k := r.nodes["b"].m.q.Status().PeerKeys
		if k == nil {
			return false
		}
		newKey = k.Self
		return newKey != "" && r.nodes["a"].m.peerTLS.Pinned("")["b"] == newKey
	})
	// The witness pins b's new key at b's own next contact with it, not
	// when a does: the two first contacts race, so wait for this one too.
	pairWait(t, "b's new key pinned at the witness", 15*time.Second, func() bool {
		return witnessPinned(t, r.wdir, r.env["a"]["NAUTILUS_CLUSTER"])["b"] == newKey
	})
}

// A lost witness answer never lifts (#710 2b, Fable): the witness applied
// the drop but this controller never heard it, so its unpin stays
// pending. Meanwhile a's replacement was pinned here. The retry only
// finishes the witness's leg, about the old key: it neither drops the
// replacement here nor lifts the old key's refusal at the witness.
func TestPairALostWitnessAnswerNeverLifts(t *testing.T) {
	r := newPairRig(t)
	r.leader()
	oldKey := r.nodes["a"].m.q.Status().PeerKeys.Self
	r.nodes["a"].crash()
	r.unpinOn("b", "a")
	b := r.nodes["b"].m.peerTLS
	// The answer lost: the leg still pending, as the first run left it.
	if err := b.SetUnpinPending("a", oldKey); err != nil {
		t.Fatal(err)
	}
	// a's replacement reaches b first.
	repl := memberPeer(t, r.token, r.cluster(), "a")
	replKey, _ := repl.Fingerprint()
	req, _ := http.NewRequest(http.MethodGet, "https://"+r.peer["b"]+node.Prefix+r.cluster()+"/hello?c=0123456789abcdef&from=a", nil)
	req.Header.Set(node.TokenHeader, r.token)
	if resp, err := wrapClient(repl, "b").Do(req); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
	}
	if b.Pinned("")["a"] != replKey {
		t.Fatalf("b pinned a as %s, want the replacement's %s", b.Pinned("")["a"], replKey)
	}
	retry := r.unpinOn("b", "a")
	if retry.Lifted || retry.Witness != string(peertls.AlreadyDropped) {
		t.Fatalf("the retry: %+v, want only the witness's leg (already dropped there)", retry)
	}
	if b.Pinned("")["a"] != replKey || b.UnpinPending("a") != "" {
		t.Fatalf("after the retry: b pins a as %s (want the replacement's), pending %q", b.Pinned("")["a"], b.UnpinPending("a"))
	}
	if w := witnessRefused(t, r.witDir, r.cluster()); w["a"] != oldKey {
		t.Fatalf("the witness refuses %v for a, want the old key %s", w, oldKey)
	}
}

// The witness box replaced (#710 2b B'): its new key is refused by both
// controllers, named, and shown waiting in naut redundancy status, until
// naut redundancy unpin witness runs on each; then the pair adopts it and
// drops the old incarnation's pin.
func TestPairAReplacedWitnessBoxWaitsForTheOperator(t *testing.T) {
	r := newPairRig(t)
	r.leader()
	inc := r.elector("a").Status().Incarnation
	r.stopWitness()
	files, _ := filepath.Glob(filepath.Join(r.witDir, "*"))
	for _, f := range files {
		if err := os.RemoveAll(f); err != nil { // the box replaced: its key too
			t.Fatal(err)
		}
	}
	r.startWitness(r.wit)
	waiting := map[string]string{}
	for _, n := range []string{"a", "b"} {
		pairWait(t, n+" to refuse the new witness key, naming the command", 15*time.Second, func() bool {
			k := r.nodes[n].m.q.Status().PeerKeys
			waiting[n] = k.WitnessWaiting
			return k.WitnessWaiting != "" && r.hasWarning(n, "naut redundancy unpin witness` on BOTH controllers")
		})
	}
	var out bytes.Buffer
	b, _ := json.Marshal(r.nodes["a"].m.q.Status())
	printClusterStatus(&out, b)
	if !strings.Contains(out.String(), "new witness key waiting: "+waiting["a"]) {
		t.Fatalf("naut redundancy status:\n%s", out.String())
	}
	time.Sleep(2 * time.Second)
	if r.elector("a").Status().Incarnation != inc {
		t.Fatal("the pair adopted the new witness box without an operator")
	}
	for _, n := range []string{"a", "b"} {
		if rep := r.unpinOn(n, "witness"); !strings.Contains(rep.Message, waiting[n]) {
			t.Fatalf("unpin witness on %s: %+v", n, rep)
		}
	}
	pairWait(t, "the pair to adopt the new witness", 60*time.Second, func() bool {
		a, b := r.elector("a").Status(), r.elector("b").Status()
		return a.Incarnation != inc && a.Incarnation == b.Incarnation && a.Commissioned && b.Commissioned
	})
	newInc := r.elector("a").Status().Incarnation
	for _, n := range []string{"a", "b"} {
		pairWait(t, n+" to pin only the new witness", 10*time.Second, func() bool {
			p := r.nodes[n].m.peerTLS.Pinned("")
			return p[peertls.WitnessName(newInc)] == waiting[n] && p[peertls.WitnessName(inc)] == ""
		})
	}
}

// An unpin that stopped after recording its witness leg as pending, but
// before its local drop (a crash, a write error), is finished by the
// retry here too: the pin dropped, the key refused (Fable, 2b re-check).
func TestPairAnUnpinStoppedBeforeItsDropIsFinished(t *testing.T) {
	r := newPairRig(t)
	r.leader()
	oldKey := r.nodes["a"].m.q.Status().PeerKeys.Self
	r.nodes["a"].crash()
	b := r.nodes["b"].m.peerTLS
	if err := b.SetUnpinPending("a", oldKey); err != nil {
		t.Fatal(err)
	}
	rep := r.unpinOn("b", "a")
	if rep.Here != string(peertls.Dropped) || rep.Witness != string(peertls.Dropped) {
		t.Fatalf("the retry: %+v, want the drop done here and at the witness", rep)
	}
	if b.Pinned("")["a"] != "" || b.Refused("")["a"] != oldKey || b.UnpinPending("a") != "" {
		t.Fatalf("after the retry: pinned %v, refused %v, pending %q (row retry_skips_local_drop)", b.Pinned(""), b.Refused(""), b.UnpinPending("a"))
	}
}
