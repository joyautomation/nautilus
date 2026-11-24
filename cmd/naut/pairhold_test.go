package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/internal/project"
)

// The takeover hold on a real pair (#710 (b2), pairhold.go,
// runtime/hold.go).

// holdPairProject is the hot pair's project with a takeover hold: Main
// copies TAKEOVER_HOLD() into Held, and sets the release tag Release once
// the test sets Allow.
func holdPairProject(hold string) func(a, b, w string) fstest.MapFS {
	return func(a, b, w string) fstest.MapFS {
		fs := hotPairProject(a, b, w)
		y := string(fs["nautilus.yaml"].Data)
		y = strings.Replace(y, "  - { name: Count, role: state, init: 0 }\n", `  - { name: Count, role: state, init: 0 }
  - { name: Held, role: state, init: false }
  - { name: Allow, role: state, init: false }
  - { name: Release, role: state, init: false }
`, 1)
		y += hold
		fs["nautilus.yaml"] = &fstest.MapFile{Data: []byte(y)}
		fs["main.st"] = &fstest.MapFile{Data: []byte(`PROGRAM Main
VAR_EXTERNAL SP : REAL; Go : BOOL; TonDone : BOOL; Count : INT; Held : BOOL; Allow : BOOL; Release : BOOL; END_VAR
VAR tmr : TON; cnt : CTU; tog : BOOL; END_VAR
tmr(IN := Go, PT := T#4s);
TonDone := tmr.Q;
tog := NOT tog;
cnt(CU := tog, R := FALSE, PV := 30000);
Count := cnt.CV;
Held := TAKEOVER_HOLD();
IF Allow THEN Release := TRUE; END_IF;
END_PROGRAM
`)}
		return fs
	}
}

// holdEvents are the journal's takeover-hold entries' events, in order.
func holdEvents(jr *recJournal) []audit.Entry {
	var out []audit.Entry
	for _, e := range jr.entries() {
		if e.Action == "redundancy.takeover-hold" {
			out = append(out, e)
		}
	}
	return out
}

func waitHoldEvent(t *testing.T, jr *recJournal, event string) audit.Entry {
	t.Helper()
	var got audit.Entry
	pairWait(t, "the hold's "+event+" in the journal", 10*time.Second, func() bool {
		for _, e := range holdEvents(jr) {
			if e.Detail["event"] == event {
				got = e
				return true
			}
		}
		return false
	})
	return got
}

// A cold takeover holds: the program reads TAKEOVER_HOLD() TRUE, the
// leader's /api/cluster shows the hold and warns of it, the journal says
// when it started and when it passed its max, and it stays held past its
// max until an operator's audited release-hold on the leader's API. A new
// pair's first leader never holds (a start).
// hold_never_starts and hold_no_max turn it red.
func TestPairTakeoverHoldColdAndOperatorRelease(t *testing.T) {
	testPairFiles = holdPairProject("  takeover-hold:\n    release: Release\n    max: 1s\n")
	testPairAPIToken = "api-secret"
	t.Cleanup(func() { testPairFiles, testPairAPIToken = nil, "" })
	r := newPairRigFull(t, func(_ string, p *project.Project) {
		zero := project.Duration(0)
		p.Redundancy.HotMaxAge = &zero // every takeover cold
	}, nil)
	lead := r.leader()
	stand := other(lead)
	time.Sleep(200 * time.Millisecond)
	if got := r.get(lead, "Held"); got != "false" {
		t.Fatalf("the first leader holds (Held %s): a start never holds", got)
	}
	if st := r.streamStatus(lead).TakeoverHold; st == nil || st.Holding || st.Release != "Release" {
		t.Fatalf("the first leader's hold status %+v, want configured, not holding", st)
	}
	jr := &recJournal{}
	r.nodes[stand].m.SetJournal(jr)
	r.nodes[lead].crash()
	if got := r.leader(); got != stand {
		t.Fatalf("after the kill: leader %q, want %s", got, stand)
	}
	pairWait(t, "TAKEOVER_HOLD() on the new leader", 10*time.Second, func() bool { return r.get(stand, "Held") == "true" })
	if e := waitHoldEvent(t, jr, "start"); e.Detail["inherited"] != nil {
		t.Fatalf("start %v: a cold takeover's hold is its own", e.Detail)
	}
	st := r.streamStatus(stand).TakeoverHold
	if st == nil || !st.Holding {
		t.Fatalf("/api/cluster hold %+v, want holding", st)
	}
	pairWait(t, "the hold's warning", 5*time.Second, func() bool {
		for _, w := range r.nodes[stand].m.q.Status().Warnings {
			if strings.Contains(w, "takeover hold") && strings.Contains(w, "Release") {
				return true
			}
		}
		return false
	})
	waitHoldEvent(t, jr, "max")
	pairWait(t, "at max on /api/cluster", 5*time.Second, func() bool {
		s := r.streamStatus(stand).TakeoverHold
		return s != nil && s.Holding && s.AtMax
	})
	time.Sleep(500 * time.Millisecond)
	if got := r.get(stand, "Held"); got != "true" {
		t.Fatalf("Held %s past the max: the hold must stay held", got)
	}

	// The operator releases on the leader (the killed member is down).
	code, body := r.authed(stand, http.MethodPost, "/api/cluster/release-hold", "{}")
	if code != http.StatusOK {
		t.Fatalf("release-hold: %d %s", code, body)
	}
	pairWait(t, "TAKEOVER_HOLD() FALSE after the release", 5*time.Second, func() bool { return r.get(stand, "Held") == "false" })
	if e := waitHoldEvent(t, jr, "release"); e.Detail["by"] != "token" || e.User != "token" {
		t.Fatalf("release %+v, want by the token's principal", e)
	}
	if code, body := r.authed(stand, http.MethodPost, "/api/cluster/release-hold", "{}"); code != http.StatusConflict {
		t.Fatalf("a second release-hold: %d %s, want 409", code, body)
	}
}

// authed is call with the rig's API token.
func (r *pairRig) authed(n, method, path, body string) (int, string) {
	r.t.Helper()
	req, err := http.NewRequest(method, "http://"+r.host[n]+":"+r.apiPort+path, strings.NewReader(body))
	if err != nil {
		r.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testPairAPIToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// A hot takeover from a leader still holding inherits its hold, age and
// all (on: always makes the first takeover hold, hot as it is), and the
// program releases it with its release tag. hold_not_inherited and
// hold_not_imaged turn it red.
func TestPairTakeoverHoldInheritedAndProgramRelease(t *testing.T) {
	testPairFiles = holdPairProject("  takeover-hold:\n    release: Release\n    max: 1m\n    on: always\n")
	t.Cleanup(func() { testPairFiles = nil })
	r := newPairRigFull(t, nil, nil)
	first := r.leader()
	second := other(first)
	r.inSync(t, second)
	time.Sleep(time.Second) // the voters learn the standby is current
	r.shadowed(t, first)
	r.nodes[first].crash()
	if got := r.leader(); got != second {
		t.Fatalf("leader %q, want %s", got, second)
	}
	pairWait(t, "the first takeover's hold", 10*time.Second, func() bool { return r.get(second, "Held") == "true" })
	if s := r.streamStatus(second); s.Takeover != "hot" {
		t.Fatalf("the first takeover %q (%s), want hot", s.Takeover, s.TakeoverCold)
	}
	// The killed member comes back as the standby and follows the holder.
	r.restart(first)
	r.inSync(t, first)
	time.Sleep(time.Second)
	r.shadowed(t, second)
	heldFor := r.streamStatus(second).TakeoverHold.AgeMs
	jr := &recJournal{}
	r.nodes[first].m.SetJournal(jr)
	r.nodes[second].crash()
	if got := r.leader(); got != first {
		t.Fatalf("leader %q, want %s", got, first)
	}
	pairWait(t, "the inherited hold", 10*time.Second, func() bool { return r.get(first, "Held") == "true" })
	e := waitHoldEvent(t, jr, "start")
	if e.Detail["inherited"] != true {
		t.Fatalf("start %v, want inherited", e.Detail)
	}
	if age, _ := e.Detail["ageMs"].(int64); age < heldFor {
		t.Fatalf("inherited age %v ms, want at least the %d ms it had held on the old leader", e.Detail["ageMs"], heldFor)
	}
	if s := r.streamStatus(first); s.Takeover != "hot" || !s.TakeoverHold.Inherited {
		t.Fatalf("takeover %q, hold %+v; want hot and inherited", s.Takeover, s.TakeoverHold)
	}
	r.set(t, first, "Allow", true)
	pairWait(t, "the program's release", 5*time.Second, func() bool { return r.get(first, "Held") == "false" })
	if e := waitHoldEvent(t, jr, "release"); e.Detail["by"] != "program" {
		t.Fatalf("release %v, want by the program", e.Detail)
	}
}
