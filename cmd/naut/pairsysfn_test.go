package main

import (
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// The nautilus system functions on a real pair (#710 (b)): PEER_READY()
// on the leader, TAKEOVER_SCAN(), HOT_TAKEOVER() and TAKEOVERS() on the
// controller that takes over.

// sysFnPairProject is the hot pair's project with Main counting the
// takeover flags into tags the program writes (so a hot image carries
// them) and copying TAKEOVERS() and PEER_READY() out.
func sysFnPairProject(a, b, w string) fstest.MapFS {
	fs := hotPairProject(a, b, w)
	y := string(fs["nautilus.yaml"].Data)
	y = strings.Replace(y, "  - { name: Count, role: state, init: 0 }\n", `  - { name: Count, role: state, init: 0 }
  - { name: Took, role: state, init: 0 }
  - { name: HotTook, role: state, init: 0 }
  - { name: Firsts, role: state, init: 0 }
  - { name: TookCount, role: state, init: 0 }
  - { name: Ready, role: state, init: false }
`, 1)
	fs["nautilus.yaml"] = &fstest.MapFile{Data: []byte(y)}
	fs["main.st"] = &fstest.MapFile{Data: []byte(`PROGRAM Main
VAR_EXTERNAL SP : REAL; Go : BOOL; TonDone : BOOL; Count : INT; Took : INT; HotTook : INT; Firsts : INT; TookCount : DINT; Ready : BOOL; END_VAR
VAR tmr : TON; cnt : CTU; tog : BOOL; END_VAR
tmr(IN := Go, PT := T#4s);
TonDone := tmr.Q;
tog := NOT tog;
cnt(CU := tog, R := FALSE, PV := 30000);
Count := cnt.CV;
IF TAKEOVER_SCAN() THEN Took := Took + 1; END_IF;
IF HOT_TAKEOVER() THEN HotTook := HotTook + 1; END_IF;
IF FIRST_SCAN() THEN Firsts := Firsts + 1; END_IF;
TookCount := TAKEOVERS();
Ready := PEER_READY();
END_PROGRAM
`)}
	return fs
}

// The leader's PEER_READY() is TRUE while its standby is current and
// FALSE while it holds a force (a takeover drops forces: not bumpless) or
// its standby stalls. A hard kill of the leader is a hot takeover:
// TAKEOVER_SCAN() and HOT_TAKEOVER() TRUE for one scan, FIRST_SCAN() not
// (the image's count goes on), TAKEOVERS() 1, and PEER_READY() FALSE with
// no standby. peer_ready_ignores_forces turns it red.
func TestPairSystemFunctions(t *testing.T) {
	testPairFiles = sysFnPairProject
	t.Cleanup(func() { testPairFiles = nil })
	r := newPairRigFull(t, nil, nil)
	lead := r.leader()
	stand := other(lead)
	r.inSync(t, stand)
	pairWait(t, "PEER_READY on the leader", 10*time.Second, func() bool { return r.get(lead, "Ready") == "true" })
	if err := r.nodes[lead].rt.Tags().Force("Go", true); err != nil {
		t.Fatal(err)
	}
	pairWait(t, "PEER_READY FALSE while a force is held", 5*time.Second, func() bool { return r.get(lead, "Ready") == "false" })
	r.nodes[lead].rt.Tags().UnforceAll()
	pairWait(t, "PEER_READY again", 10*time.Second, func() bool { return r.get(lead, "Ready") == "true" })
	r.nodes[lead].m.stream.hold.Store(true) // the standby stalls
	pairWait(t, "PEER_READY FALSE with a stalled standby", 10*time.Second, func() bool { return r.get(lead, "Ready") == "false" })
	r.nodes[lead].m.stream.hold.Store(false)
	pairWait(t, "PEER_READY again", 10*time.Second, func() bool { return r.get(lead, "Ready") == "true" })
	// The voters learn the standby is current again from the leader's
	// next renewals (up to lease/4): a kill before that is cold, as
	// documented.
	time.Sleep(time.Second)
	if got := r.get(lead, "Took"); got != "0" {
		t.Fatalf("Took on the first leader = %s: a new pair's first leadership is a start", got)
	}
	r.shadowed(t, lead)
	r.nodes[lead].crash()
	if got := r.leader(); got != stand {
		t.Fatalf("after the kill: leader %q, want %s", got, stand)
	}
	pairWait(t, "the takeover report", 5*time.Second, func() bool {
		s := r.streamStatus(stand)
		return s != nil && s.Takeover != ""
	})
	if s := r.streamStatus(stand); s.Takeover != "hot" {
		t.Fatalf("takeover %q (%s), want hot", s.Takeover, s.TakeoverCold)
	}
	time.Sleep(300 * time.Millisecond) // a few scans on
	for path, want := range map[string]string{"Took": "1", "HotTook": "1", "Firsts": "1", "TookCount": "1", "Ready": "false"} {
		if got := r.get(stand, path); got != want {
			t.Errorf("%s on the new leader = %s, want %s", path, got, want)
		}
	}
}
