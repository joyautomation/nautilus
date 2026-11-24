package peertls

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dialName dials addr as m calling the witness (incOK as WrapWitness's)
// and returns the name the witness's certificate gave.
func dialName(t *testing.T, m *tm, addr string, incOK func(uint64) bool) (string, error) {
	t.Helper()
	raw, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.p.WrapWitness(context.Background(), raw, addr, incOK)
	if err != nil {
		return "", err
	}
	defer c.Close()
	return c.(*tls.Conn).ConnectionState().PeerCertificates[0].Subject.CommonName, nil
}

// The witness's certificate names the incarnation of the pair the call
// names in its handshake, each pair its own; a call naming none, or a
// pair before its first vote, meets witness@0.
func TestTheWitnessNamesThePairsIncarnation(t *testing.T) {
	w := newMember(t, tokenA, Witness)
	w.setInc(cluster, 7)
	w.setInc(other, 9)
	addr := w.serve()
	a := newMember(t, tokenA, "a")
	if n, err := dialName(t, a, addr, nil); err != nil || n != "witness@7" {
		t.Fatalf("a of cluster: %q %v, want witness@7", n, err)
	}
	oc := newMember(t, tokenA, "a")
	oc.p.o.Cluster = other
	if n, err := dialName(t, oc, addr, nil); err != nil || n != "witness@9" {
		t.Fatalf("a of the other cluster: %q %v, want witness@9", n, err)
	}
	if a.p.Pinned("")["witness@7"] != w.fp() {
		t.Fatalf("a's pins: %v", a.p.Pinned(""))
	}
	w.setInc(cluster, 0)
	if n, err := dialName(t, a, addr, nil); err != nil || n != "witness@0" {
		t.Fatalf("before the pair's first vote: %q %v, want witness@0", n, err)
	}
}

// witness@0 is never pinned: nothing it answers counts (a reply's
// incarnation must be the certificate's, and none is 0), so pinning it
// would only let the first token holder lock the real witness out.
func TestWitnessZeroIsNeverPinned(t *testing.T) {
	w := newMember(t, tokenA, Witness)
	addr := w.serve()
	a := newMember(t, tokenA, "a")
	if _, err := dialName(t, a, addr, nil); err != nil {
		t.Fatal(err)
	}
	if p := a.p.Pinned(""); len(p) != 0 {
		t.Fatalf("witness@0 pinned: %v", p)
	}
	// Another key answering as witness@0 is no refusal either.
	w2 := newMember(t, tokenA, Witness)
	w2.box.MachineID = "another-witness"
	if _, err := dialName(t, a, w2.serve(), nil); err != nil {
		t.Fatalf("another witness@0: %v", err)
	}
}

// Each incarnation's name is pinned on its own; one pinned name's key
// never changes; the adoption of one drops the others' pins (not
// refused: that incarnation may answer again, and counts for nothing).
func TestEachIncarnationIsPinnedAndAdoptionDropsTheOthers(t *testing.T) {
	w := newMember(t, tokenA, Witness)
	w.setInc(cluster, 7)
	wAddr := w.serve()
	w2 := newMember(t, tokenA, Witness)
	w2.box.MachineID = "rebuilt-witness-box"
	w2.setInc(cluster, 8)
	w2Addr := w2.serve()
	a := newMember(t, tokenA, "a")
	for _, addr := range []string{wAddr, w2Addr} {
		if _, err := dialName(t, a, addr, nil); err != nil {
			t.Fatal(err)
		}
	}
	p := a.p.Pinned("")
	if p["witness@7"] != w.fp() || p["witness@8"] != w2.fp() {
		t.Fatalf("pins: %v", p)
	}
	// w2 answering as incarnation 7: not the key pinned for it.
	w2.setInc(cluster, 7)
	var ie *IdentityError
	if _, err := dialName(t, a, w2Addr, nil); !errors.As(err, &ie) || !ie.PinMismatch() || !strings.Contains(err.Error(), "fresh state directory") {
		t.Fatalf("another key as witness@7: %v", err)
	}
	w2.setInc(cluster, 8)
	closed := 0
	a.p.OnChange(func(string, string) { closed++ })
	if err := a.p.Promote(8); err != nil {
		t.Fatal(err)
	}
	if p := a.p.Pinned(""); p["witness@7"] != "" || p["witness@8"] != w2.fp() {
		t.Fatalf("after the adoption of 8: %v", p)
	}
	if closed != 1 {
		t.Fatalf("%d pin changes heard, want 1", closed)
	}
	if len(a.p.Refused("")) != 0 {
		t.Fatalf("an incarnation the pair stopped counting is refused: %v", a.p.Refused(""))
	}
	// It survives a restart (the pins file).
	a2 := newMemberIn(t, tokenA, "a", a.dir)
	if p := a2.p.Pinned(""); len(p) != 1 || p["witness@8"] == "" {
		t.Fatalf("after a restart: %v", p)
	}
}

// Past MaxWitnessNames incarnations the pair does not count, a new one is
// refused, never one evicted; the counted incarnation always pins.
func TestUncountedWitnessNamesAreCapped(t *testing.T) {
	w := newMember(t, tokenA, Witness)
	addr := w.serve()
	a := newMember(t, tokenA, "a") // uncommissioned: B' applies to a commissioned one
	for i := uint64(1); i <= MaxWitnessNames; i++ {
		w.setInc(cluster, i)
		if _, err := dialName(t, a, addr, nil); err != nil {
			t.Fatalf("incarnation %d: %v", i, err)
		}
	}
	w.setInc(cluster, MaxWitnessNames+1)
	var ie *IdentityError
	if _, err := dialName(t, a, addr, nil); !errors.As(err, &ie) || !ie.Full {
		t.Fatalf("one past the cap: %v", err)
	}
	if n := len(a.p.Pinned("")); n != MaxWitnessNames {
		t.Fatalf("%d pins, want %d (none evicted)", n, MaxWitnessNames)
	}
	a.setCounted(100)
	w.setInc(cluster, 100)
	if _, err := dialName(t, a, addr, nil); err != nil {
		t.Fatalf("the counted incarnation past the cap: %v", err)
	}
}

// A call that is not a vote takes only the incarnation the pair counts:
// another is refused at the handshake, and never pinned.
func TestAWitnessCallTakesOnlyTheCountedIncarnation(t *testing.T) {
	w := newMember(t, tokenA, Witness)
	w.setInc(cluster, 8)
	addr := w.serve()
	a := newMember(t, tokenA, "a")
	only7 := func(inc uint64) bool { return inc == 7 }
	var ie *IncarnationError
	if _, err := dialName(t, a, addr, only7); !errors.As(err, &ie) || ie.Got != 8 {
		t.Fatalf("incarnation 8 on a call for 7: %v", err)
	}
	if len(a.p.Pinned("")) != 0 {
		t.Fatalf("pinned: %v", a.p.Pinned(""))
	}
	w.setInc(cluster, 7)
	if _, err := dialName(t, a, addr, only7); err != nil {
		t.Fatal(err)
	}
}

// A dropped pin's key stays refused across a restart (the pins file),
// until a second unpin lifts it; a drop says what it found.
func TestADroppedKeyStaysRefusedAcrossARestart(t *testing.T) {
	a, b := newMember(t, tokenA, "a"), newMember(t, tokenA, "b")
	addr := b.serve()
	if _, err := get(a.client("b"), addr); err != nil {
		t.Fatal(err)
	}
	if out, err := b.p.Drop("", "a", "test"); err != nil || out != Dropped {
		t.Fatalf("drop: %v %v", out, err)
	}
	if out, _ := b.p.Drop("", "a", "test"); out != AlreadyDropped {
		t.Fatalf("second drop: %v, want already-dropped", out)
	}
	if out, _ := b.p.Drop("", "nobody", "test"); out != NotPinned {
		t.Fatalf("drop of a name never pinned: %v", out)
	}
	b2 := newMemberIn(t, tokenA, "b", b.dir)
	addr2 := b2.serve()
	var he *httpErr
	if _, err := get(a.client("b"), addr2); !errors.As(err, &he) || !strings.Contains(he.body, "pin was dropped") {
		t.Fatalf("the dropped key after b's restart: %v", err)
	}
	if b2.p.Refused("")["a"] != a.fp() {
		t.Fatalf("refused: %v", b2.p.Refused(""))
	}
	if lifted, err := b2.p.Lift("", "a", "test: second unpin"); err != nil || !lifted {
		t.Fatalf("lift: %v %v", lifted, err)
	}
	if _, err := get(a.client("b"), addr2); err != nil {
		t.Fatalf("after the lift: %v", err)
	}
	if b2.p.Pinned("")["a"] != a.fp() || len(b2.p.Refused("")) != 0 {
		t.Fatalf("pins %v refused %v", b2.p.Pinned(""), b2.p.Refused(""))
	}
}

// A name pinned after a drop is newer than one pinned before it: the
// epoch the witness's unpin rule compares.
func TestPinEpochs(t *testing.T) {
	w := newMember(t, tokenA, Witness)
	w.setInc(cluster, 7)
	addr := w.serve()
	a, b := newMember(t, tokenA, "a"), newMember(t, tokenA, "b")
	for _, m := range []*tm{a, b} {
		if _, err := get(m.client(Witness), addr); err != nil { // the witness pins a caller at its first request
			t.Fatal(err)
		}
	}
	if _, err := w.p.Drop(cluster, "a", "test"); err != nil {
		t.Fatal(err)
	}
	a2 := newMember(t, tokenA, "a")
	if _, err := get(a2.client(Witness), addr); err != nil {
		t.Fatal(err)
	}
	ea, _ := w.p.PinEpoch(cluster, "a")
	eb, okb := w.p.PinEpoch(cluster, "b")
	if ea != 1 || eb != 0 || !okb {
		t.Fatalf("epochs a=%d b=%d (%v), want 1 and 0", ea, eb, okb)
	}
	if _, ok := w.p.PinEpoch(cluster, "nobody"); ok {
		t.Fatal("a name never pinned has an epoch")
	}
}

// A 2a pins file's one witness name is dropped on load; a certificate
// naming the bare witness is refused.
func TestTheBareWitnessNameIsGone(t *testing.T) {
	dir := t.TempDir()
	a := newMemberIn(t, tokenA, "a", dir)
	key := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	if err := os.WriteFile(filepath.Join(dir, "peer-pins.json"), []byte(`{"v":1,"pins":{"witness":"`+key+`","b":"`+key+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a2 := newMemberIn(t, tokenA, "a", dir)
	if p := a2.p.Pinned(""); len(p) != 1 || p["b"] == "" {
		t.Fatalf("pins: %v", p)
	}
	if _, ok := parseName(Witness); ok {
		t.Fatal("the bare witness name parses")
	}
	for _, n := range []string{"witness@", "witness@07", "witness@-1", "witness@x"} {
		if _, ok := parseName(n); ok {
			t.Fatalf("%q parses", n)
		}
	}
	_ = a
}

// A commissioned controller takes a new witness incarnation by itself
// only under the key of the one it counts (a rebuilt witness keeps its
// box key). Another key (a new witness box, or a token holder at the
// witness's address) is refused, named and kept waiting until an
// operator allows the next one, once (#710 2b B').
func TestANewWitnessKeyWaitsForTheOperator(t *testing.T) {
	w := newMember(t, tokenA, Witness)
	w.setInc(cluster, 7)
	addr := w.serve()
	a := newMember(t, tokenA, "a")
	if _, err := dialName(t, a, addr, nil); err != nil {
		t.Fatal(err)
	}
	a.setCounted(7)
	w.setInc(cluster, 8) // rebuilt: the same box key
	if _, err := dialName(t, a, addr, nil); err != nil {
		t.Fatalf("a rebuilt witness under the counted key: %v", err)
	}
	other := newMember(t, tokenA, Witness)
	other.box.MachineID = "another-witness-box"
	other.setInc(cluster, 9)
	oAddr := other.serve()
	var ie *IdentityError
	_, err := dialName(t, a, oAddr, nil)
	if !errors.As(err, &ie) || !ie.NewWitness || !strings.Contains(err.Error(), "naut redundancy unpin witness` on BOTH controllers") {
		t.Fatalf("a new witness key: %v", err)
	}
	if a.p.WitnessWaiting() != other.fp() || a.p.Pinned("")["witness@9"] != "" {
		t.Fatalf("waiting %q, pins %v", a.p.WitnessWaiting(), a.p.Pinned(""))
	}
	if waiting, err := a.p.AllowWitnessKey("test"); err != nil || waiting != other.fp() {
		t.Fatalf("allow: %q %v", waiting, err)
	}
	// The allowance is of that very key: another arriving first stays
	// refused.
	third := newMember(t, tokenA, Witness)
	third.box.MachineID = "a-third-witness-box"
	third.setInc(cluster, 10)
	tAddr := third.serve()
	if _, err := dialName(t, a, tAddr, nil); !errors.As(err, &ie) || !ie.NewWitness {
		t.Fatalf("another key than the one allowed (row allowance_any_key): %v", err)
	}
	// Durable: a restart keeps the allowance.
	a2 := newMemberIn(t, tokenA, "a", a.dir)
	a2.setCounted(7)
	if a2.p.WitnessAllowed() != other.fp() {
		t.Fatal("the allowance did not survive a restart")
	}
	if _, err := dialName(t, a2, oAddr, nil); err != nil {
		t.Fatalf("the allowed new key: %v", err)
	}
	if a2.p.Pinned("")["witness@9"] != other.fp() || a2.p.WitnessAllowed() != "" {
		t.Fatalf("after the allowed pin: pins %v, allowed %q", a2.p.Pinned(""), a2.p.WitnessAllowed())
	}
	// Once: the third key is refused again.
	if _, err := dialName(t, a2, tAddr, nil); !errors.As(err, &ie) || !ie.NewWitness {
		t.Fatalf("a third key after the one allowed: %v", err)
	}
}

// naut redundancy unpin witness allows only a key that is waiting.
func TestAnAllowanceNeedsAWaitingKey(t *testing.T) {
	a := newMember(t, tokenA, "a")
	if _, err := a.p.AllowWitnessKey("test"); !errors.Is(err, ErrNoWitnessWaiting) {
		t.Fatalf("an allowance with nothing waiting: %v", err)
	}
}

// The key of the counted incarnation outlives its name (Fable, 2b
// re-check): an incarnation adopted on the peer's proposal, never heard
// here, pins only under it, so one operator allowance on one controller
// is not a way around the other; an allowance ends at an adoption; and a
// controller that lost only its voter state (it counts nothing) keeps the
// key and its gate.
func TestTheCountedWitnessKeyOutlivesItsName(t *testing.T) {
	w := newMember(t, tokenA, Witness)
	w.setInc(cluster, 7)
	addr := w.serve()
	a := newMember(t, tokenA, "a")
	if _, err := dialName(t, a, addr, nil); err != nil {
		t.Fatal(err)
	}
	a.setCounted(7)
	if err := a.p.Promote(7); err != nil {
		t.Fatal(err)
	}
	// The pair adopts 8 on the peer's proposal; a never heard it.
	a.setCounted(8)
	if err := a.p.Promote(8); err != nil {
		t.Fatal(err)
	}
	imp := newMember(t, tokenA, Witness)
	imp.box.MachineID = "a-token-holder"
	imp.setInc(cluster, 8)
	impAddr := imp.serve()
	var ie *IdentityError
	if _, err := dialName(t, a, impAddr, nil); !errors.As(err, &ie) || !ie.NewWitness {
		t.Fatalf("another key as the counted name, never heard here (row witness_key_forgotten): %v", err)
	}
	w.setInc(cluster, 8)
	if _, err := dialName(t, a, addr, nil); err != nil {
		t.Fatalf("the counted key as the counted name: %v", err)
	}
	// An allowance ends at an adoption of an incarnation pinned here.
	imp.setInc(cluster, 9)
	if _, err := dialName(t, a, impAddr, nil); err == nil {
		t.Fatal("another key as a new incarnation")
	}
	if _, err := a.p.AllowWitnessKey("test"); err != nil {
		t.Fatal(err)
	}
	if err := a.p.Promote(8); err != nil {
		t.Fatal(err)
	}
	if a.p.WitnessAllowed() != "" {
		t.Fatal("an allowance outlived an adoption (row allowance_kept_at_adoption)")
	}
	// Its voter state lost (counting nothing), a keeps the key: still gated.
	a2 := newMemberIn(t, tokenA, "a", a.dir)
	if err := a2.p.PromoteAtStart(0); err != nil {
		t.Fatal(err)
	}
	if a2.p.Pinned("")["witness@8"] == "" {
		t.Fatalf("the witness pins were dropped at a start that knows the key: %v", a2.p.Pinned(""))
	}
	imp.setInc(cluster, 11)
	if _, err := dialName(t, a2, impAddr, nil); !errors.As(err, &ie) || !ie.NewWitness {
		t.Fatalf("another key, uncommissioned but knowing the witness's: %v", err)
	}
}

// With the counted incarnation's key not known here (a reset pins file),
// only the counted name itself pins at first contact; another incarnation
// waits for the operator.
func TestWithoutTheCountedKeyOnlyTheCountedNamePins(t *testing.T) {
	w := newMember(t, tokenA, Witness)
	addr := w.serve()
	a := newMember(t, tokenA, "a")
	a.setCounted(7)
	w.setInc(cluster, 8)
	var ie *IdentityError
	if _, err := dialName(t, a, addr, nil); !errors.As(err, &ie) || !ie.NewWitness {
		t.Fatalf("another incarnation with no counted key: %v", err)
	}
	w.setInc(cluster, 7)
	if _, err := dialName(t, a, addr, nil); err != nil {
		t.Fatalf("the counted name: %v", err)
	}
}

// An allowance survives an adoption of an incarnation not pinned here
// yet: the operator runs unpin witness on b, then the pair adopts the
// new witness on a's proposal before b has called it. b must still take
// that key (the replaced-witness rig test's race, under load).
func TestAnAllowanceOutlivesAnAdoptionNotHeardHere(t *testing.T) {
	w := newMember(t, tokenA, Witness)
	w.setInc(cluster, 7)
	addr := w.serve()
	b := newMember(t, tokenA, "b")
	if _, err := dialName(t, b, addr, nil); err != nil {
		t.Fatal(err)
	}
	b.setCounted(7)
	if err := b.p.Promote(7); err != nil {
		t.Fatal(err)
	}
	nw := newMember(t, tokenA, Witness)
	nw.box.MachineID = "the-new-witness-box"
	nw.setInc(cluster, 9)
	nAddr := nw.serve()
	if _, err := dialName(t, b, nAddr, nil); err == nil {
		t.Fatal("the new key pinned before the operator allowed it")
	}
	if _, err := b.p.AllowWitnessKey("test"); err != nil {
		t.Fatal(err)
	}
	b.setCounted(9) // adopted on a's proposal
	if err := b.p.Promote(9); err != nil {
		t.Fatal(err)
	}
	if b.p.WitnessAllowed() != nw.fp() {
		t.Fatal("an adoption of an incarnation never pinned here ended the allowance (row allowance_cleared_unheard)")
	}
	if _, err := dialName(t, b, nAddr, nil); err != nil {
		t.Fatalf("the allowed key as the adopted name: %v", err)
	}
}
