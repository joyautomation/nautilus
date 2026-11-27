package facade

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/siemens/webapi"
	"github.com/joyautomation/nautilus/siemens/webapi/webapitest"
)

// A download can change the CPU's data layout under a running serve. A
// write must never go out as the type serve bound before (#789 A1).

// TestAWriteToARetypedNameIsRefusedUntilReBound: Level was bound as REAL;
// the CPU now holds an LREAL. The write is refused (the number may mean
// something else now) and nothing is sent; once the set is re-bound with
// Level as LREAL, a retry writes it: 1e39, which the old REAL check would
// refuse.
func TestAWriteToARetypedNameIsRefusedUntilReBound(t *testing.T) {
	s, f, base := startWith(t, Options{Rediscover: -1, Settle: 10 * time.Millisecond})
	s.Put(`"Tags".Level`, &webapitest.Var{Datatype: "lreal", Value: 12.5})
	code, body := post(t, base, "/api/tags", `{"name":"Level","value":40}`)
	if code != http.StatusBadRequest || !strings.Contains(body, "is LREAL on the CPU now, not REAL") {
		t.Fatalf("write to the retyped Level: %d %s", code, body)
	}
	if v := s.Value(`"Tags".Level`); v != 12.5 {
		t.Fatalf("the CPU holds %v: the retyped write was sent", v)
	}
	waitFor(t, "Level re-bound as LREAL", func() bool {
		for _, b := range f.Bindings() {
			if b.Name == "Level" {
				return b.Type == "LREAL"
			}
		}
		return false
	})
	if code, body := post(t, base, "/api/tags", `{"name":"Level","value":1e39}`); code/100 != 2 || s.Value(`"Tags".Level`) != 1e39 {
		t.Fatalf("retry after the re-binding: %d %s, CPU %v", code, body, s.Value(`"Tags".Level`))
	}
}

// TestAHalfBrowsedCPUIsNotAdopted: a re-discovery that cannot browse a
// member (a CPU in the middle of a download) keeps the running set, even
// when two browses agree.
func TestAHalfBrowsedCPUIsNotAdopted(t *testing.T) {
	s, f, base := startWith(t, Options{Rediscover: 100 * time.Millisecond, Settle: 10 * time.Millisecond})
	waitFor(t, "first values", func() bool { return state(t, base)["Main.count"] != nil })
	s.Put(`"Main_DB".count`, nil)
	// Listed as a structure, but its browse fails (the fake answers "not a
	// structure" for a datatype spelled other than "struct").
	s.Put(`"Main_DB".half`, &webapitest.Var{Datatype: "STRUCT"})
	time.Sleep(600 * time.Millisecond)
	if n := f.Rebinds(); n != 0 {
		t.Fatalf("rebinds = %d: a half-browsed CPU was adopted", n)
	}
	s.Put(`"Main_DB".half`, nil)
	waitFor(t, "re-bound once the browse is whole", func() bool { return f.Rebinds() == 1 })
}

// TestAWriteToANameGoneOrNoLongerBindableIsRefused: refused before
// anything is sent, naming the fix and when serve catches up by itself.
func TestAWriteToANameGoneOrNoLongerBindableIsRefused(t *testing.T) {
	s, _, base := startWith(t, Options{Rediscover: time.Hour, Settle: time.Hour})
	s.Put(`"Tags".Speed`, nil)
	code, body := post(t, base, "/api/tags", `{"name":"Speed","value":5}`)
	if code != http.StatusBadRequest || !strings.Contains(body, "no longer on the CPU") ||
		!strings.Contains(body, "the CPU's program changed") || !strings.Contains(body, "within 1h0m0s") {
		t.Fatalf("write to a removed Speed: %d %s", code, body)
	}
	// Run is now a struct on the CPU: nothing is sent to it.
	s.Put(`"Tags".Run`, &webapitest.Var{Datatype: "struct"})
	s.Put(`"Tags".Run.x`, &webapitest.Var{Datatype: "bool", Value: false})
	code, body = post(t, base, "/api/tags", `{"name":"Run","value":true}`)
	if code != http.StatusBadRequest || !strings.Contains(body, "is struct on the CPU now") {
		t.Fatalf("write to Run, now a struct: %d %s", code, body)
	}
	if v := s.Value(`"Tags".Run.x`); v != false {
		t.Fatalf("Run.x = %v: something was written", v)
	}
}

// TestWithoutTheTimerTheFixIsARestart: with the timer off, the refusal
// promises no time, and names a restart as the way out.
func TestWithoutTheTimerTheFixIsARestart(t *testing.T) {
	s, _, base := startWith(t, Options{Rediscover: -1, Settle: time.Hour})
	s.Put(`"Tags".Speed`, nil)
	if code, body := post(t, base, "/api/tags", `{"name":"Speed","value":5}`); code != http.StatusBadRequest ||
		!strings.Contains(body, "or restart naut siemens serve") || strings.Contains(body, "within") {
		t.Fatalf("%d %s", code, body)
	}
}

// TestAWriteTheFacadeCannotCheckIsNotSent: the check browses the parent;
// when that browse fails the write fails closed.
func TestAWriteTheFacadeCannotCheckIsNotSent(t *testing.T) {
	s, _, base := startWith(t, Options{Rediscover: -1})
	s.FailBrowse(webapi.CodeSystemIsBusy)
	code, body := post(t, base, "/api/tags", `{"name":"Level","value":40}`)
	if code < 400 || !strings.Contains(body, "could not check it on the CPU first") {
		t.Fatalf("write while the browse fails: %d %s", code, body)
	}
	if v := s.Value(`"Tags".Level`); v != 12.5 {
		t.Fatalf("the CPU holds %v: the write went out unchecked", v)
	}
	s.FailBrowse(0)
	if code, body := post(t, base, "/api/tags", `{"name":"Level","value":40}`); code/100 != 2 || s.Value(`"Tags".Level`) != 40.0 {
		t.Fatalf("write once the browse answers: %d %s, CPU %v", code, body, s.Value(`"Tags".Level`))
	}
}

// TestReDiscoveryBindsNewNamesAndEndsOpenStreams: a download added a
// local and removed another; the timer's re-discovery re-binds without a
// restart, and an open /api/stream ends so the client reconnects to it.
func TestReDiscoveryBindsNewNamesAndEndsOpenStreams(t *testing.T) {
	s, f, base := startWith(t, Options{Rediscover: 100 * time.Millisecond})
	waitFor(t, "first values", func() bool { return state(t, base)["Main.count"] != nil })

	res, err := http.Get(base + "/api/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	ended := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
		}
		close(ended)
	}()

	s.Put(`"Main_DB".wasRunning`, &webapitest.Var{Datatype: "bool", Value: true})
	s.Put(`"Main_DB".count`, nil)
	waitFor(t, "Main.wasRunning bound", func() bool {
		st := state(t, base)
		_, gone := st["Main.count"]
		return st["Main.wasRunning"] == true && !gone
	})
	if f.Rebinds() < 1 {
		t.Fatalf("rebinds = %d", f.Rebinds())
	}
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the open /api/stream did not end at the re-binding")
	}
	// The new set answers writes.
	s.Put(`"Main_DB".wasRunning`, &webapitest.Var{Datatype: "bool", Value: true})
	if code, body := post(t, base, "/api/tags", `{"name":"Main.wasRunning","value":false}`); code/100 != 2 || s.Value(`"Main_DB".wasRunning`) != false {
		t.Fatalf("write to the new name: %d %s", code, body)
	}
}

// TestAReadOfAGoneNameAsksForARediscovery: no timer, the read error does.
func TestAReadOfAGoneNameAsksForARediscovery(t *testing.T) {
	s, f, base := startWith(t, Options{Rediscover: -1, Settle: 50 * time.Millisecond})
	waitFor(t, "first values", func() bool { return state(t, base)["Main.count"] != nil })
	s.Put(`"Main_DB".count`, nil)
	waitFor(t, "Main.count unbound", func() bool { _, ok := state(t, base)["Main.count"]; return !ok && f.Rebinds() == 1 })
}

// TestKnownLeavesOutTheWritersOwnMembers: the writer's bookkeeping beside
// a timer (dwell_last) is not a nautilus name, and is not bound.
func TestKnownLeavesOutTheWritersOwnMembers(t *testing.T) {
	s, c := cpu(t)
	s.Put(`"Main_DB".dwell_last`, &webapitest.Var{Datatype: "struct"})
	s.Put(`"Main_DB".dwell_last.Q`, &webapitest.Var{Datatype: "bool", Value: true})
	known := func(n string) bool { return !strings.Contains(n, "_last") }
	f, err := New(context.Background(), Options{Client: c, Roots: roots, Known: known})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range f.Bindings() {
		if strings.Contains(b.Name, "_last") {
			t.Fatalf("bound %s", b.Name)
		}
	}
	if len(f.Bindings()) != 7 {
		t.Fatalf("bound %v", f.Bindings())
	}
}

// TestReloadPicksUpTheProjectsNewRoots: the roots come from the project
// again at each re-discovery (a program added since serve started).
func TestReloadPicksUpTheProjectsNewRoots(t *testing.T) {
	calls := 0
	reload := func() (Plan, error) {
		calls++
		return Plan{Roots: roots}, nil
	}
	_, f, base := startWith(t, Options{Roots: []Root{{DB: "Tags"}}, Reload: reload, Rediscover: 100 * time.Millisecond})
	if _, ok := state(t, base)["Main.count"]; ok {
		t.Fatal("Main bound before the reload")
	}
	waitFor(t, "Main.count after the reload", func() bool { return state(t, base)["Main.count"] != nil })
	if calls == 0 || f.Rebinds() == 0 {
		t.Fatalf("reload calls %d, rebinds %d", calls, f.Rebinds())
	}
}

// TestTimeReadsAsMilliseconds: a TIME reads as naut run shows it (ms);
// writing one is refused until the CPU's encoding is measured.
func TestTimeReadsAsMilliseconds(t *testing.T) {
	_, _, base := start(t)
	waitFor(t, "dwell.ET", func() bool { return state(t, base)["Main.dwell.ET"] == float64(1500) })
	if code, body := post(t, base, "/api/tags", `{"name":"Main.dwell.ET","value":10}`); code != http.StatusBadRequest || !strings.Contains(body, "TIME is not written") {
		t.Fatalf("TIME write: %d %s", code, body)
	}
	for in, want := range map[string]int64{"T#2s": 2000, "t#1m_30s_5ms": 90005, "TIME#-1h": -3600000, "T#1d2h": 93600000, "T#250MS": 250} {
		if got, ok := timeLiteralMs(in); !ok || got != want {
			t.Errorf("timeLiteralMs(%q) = %d %v, want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"2s", "T#", "T#5x", "T#s"} {
		if _, ok := timeLiteralMs(in); ok {
			t.Errorf("timeLiteralMs(%q) accepted", in)
		}
	}
	if v, err := decodeTime([]byte(`"T#2s"`)); err != nil || v != int64(2000) {
		t.Errorf("decodeTime string: %v %v", v, err)
	}
}

// TestAKickSettlesFirst: a name gone for a moment (a CPU mid-download)
// and back within the settle time is not re-bound away.
func TestAKickSettlesFirst(t *testing.T) {
	s, f, base := startWith(t, Options{Rediscover: -1, Settle: 600 * time.Millisecond})
	waitFor(t, "first values", func() bool { return state(t, base)["Main.count"] != nil })
	s.Put(`"Main_DB".count`, nil)
	time.Sleep(150 * time.Millisecond)
	s.Put(`"Main_DB".count`, &webapitest.Var{Datatype: "int", Value: 9})
	time.Sleep(900 * time.Millisecond)
	if n := f.Rebinds(); n != 0 {
		t.Fatalf("rebinds = %d: the kick did not settle", n)
	}
	if state(t, base)["Main.count"] != float64(9) {
		t.Fatalf("Main.count = %v", state(t, base)["Main.count"])
	}
}

// TestTheSwapWindowAnswers503: between stopping a set and serving its
// replacement, requests are told to retry, not served by a stopped set.
func TestTheSwapWindowAnswers503(t *testing.T) {
	_, f, base := startWith(t, Options{Rediscover: -1})
	f.mu.Lock()
	f.swapping = true
	f.mu.Unlock()
	res, err := http.Get(base + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable || res.Header.Get("Retry-After") == "" {
		t.Fatalf("during the swap: %d, Retry-After %q", res.StatusCode, res.Header.Get("Retry-After"))
	}
	f.mu.Lock()
	f.swapping = false
	f.mu.Unlock()
	if st := state(t, base); st == nil {
		t.Fatal("no state after the swap")
	}
}

// TestKicksThatFindNothingAreRateLimited: a member listed but not readable
// kicks on every poll; after one re-discovery found nothing changed, the
// kicks wait KickGap.
func TestKicksThatFindNothingAreRateLimited(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	reload := func() (Plan, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return Plan{Roots: roots}, nil
	}
	_, f, _ := startWith(t, Options{Rediscover: -1, Settle: 10 * time.Millisecond, KickGap: time.Hour, Reload: reload})
	for i := 0; i < 25; i++ {
		f.Kick()
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("%d re-discoveries for 25 kicks that found nothing, want 1", calls)
	}
}

// TestADifferentBrowseIsConfirmedFirst: a browse that differs (a CPU half
// loaded mid-download) is not adopted unless a second browse a Settle
// later finds the same set.
func TestADifferentBrowseIsConfirmedFirst(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	reload := func() (Plan, error) { // odd: half loaded; even: whole
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls%2 == 1 {
			return Plan{Roots: []Root{{DB: "Tags"}}}, nil
		}
		return Plan{Roots: roots}, nil
	}
	_, f, _ := startWith(t, Options{Rediscover: 100 * time.Millisecond, Settle: 20 * time.Millisecond, Reload: reload})
	time.Sleep(700 * time.Millisecond)
	mu.Lock()
	n := calls
	mu.Unlock()
	if n < 2 || f.Rebinds() != 0 {
		t.Fatalf("reloads %d, rebinds %d: a half-loaded browse was adopted", n, f.Rebinds())
	}
}

// TestAReadTheBoundTypeCannotHoldAsksForARediscovery: Run was a BOOL and
// the CPU now holds an INT there: the read's decode fails and re-binds.
func TestAReadTheBoundTypeCannotHoldAsksForARediscovery(t *testing.T) {
	s, f, base := startWith(t, Options{Rediscover: -1, Settle: 20 * time.Millisecond})
	waitFor(t, "first values", func() bool { return state(t, base)["Run"] != nil })
	s.Put(`"Tags".Run`, &webapitest.Var{Datatype: "int", Value: 5})
	waitFor(t, "Run re-bound as INT", func() bool { return state(t, base)["Run"] == float64(5) && f.Rebinds() == 1 })
}

// TestAWriteInFlightAndOneInTheSwapWindowBothFinish: write 1 is held at
// the CPU (it holds the write lock shared) when a re-binding starts; the
// swap waits for it, and write 2, which got past the Handler before the
// swap began (called as its handler would call it), waits for the swap
// and then goes to the CPU against the new set. stop() waits for the old
// set's driver, runtime and server Run loops, never for HTTP handlers, so
// nothing deadlocks.
func TestAWriteInFlightAndOneInTheSwapWindowBothFinish(t *testing.T) {
	s, f, base := startWith(t, Options{Rediscover: -1, Settle: 10 * time.Millisecond})
	waitFor(t, "first values", func() bool { return state(t, base)["Main.count"] != nil })

	held, release := s.HoldWrites()
	defer release()
	w1 := make(chan int, 1)
	go func() { code, _ := post(t, base, "/api/tags", `{"name":"Level","value":40}`); w1 <- code }()
	<-held // write 1 is at the CPU, holding the write lock shared

	s.Put(`"Main_DB".extra`, &webapitest.Var{Datatype: "bool", Value: true}) // a download added a local
	f.Kick()
	waitFor(t, "the swap waiting on write 1", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.swapping })

	w2 := make(chan error, 1)
	go func() { w2 <- f.write("Speed", 7.0) }()
	time.Sleep(100 * time.Millisecond)
	select {
	case err := <-w2:
		t.Fatalf("write 2 finished during the swap (%v): it should wait for it", err)
	default:
	}
	if f.Rebinds() != 0 {
		t.Fatal("the swap finished while write 1 was still in flight")
	}

	release()
	select {
	case code := <-w1:
		if code/100 != 2 || s.Value(`"Tags".Level`) != 40.0 {
			t.Fatalf("write 1: %d, CPU Level %v", code, s.Value(`"Tags".Level`))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("write 1 never finished: deadlock with the swap")
	}
	select {
	case err := <-w2:
		if err != nil || s.Value(`"Tags".Speed`) != 7.0 {
			t.Fatalf("write 2: %v, CPU Speed %v", err, s.Value(`"Tags".Speed`))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("write 2 never finished: deadlock with the swap")
	}
	if f.Rebinds() != 1 {
		t.Fatalf("rebinds = %d", f.Rebinds())
	}
	waitFor(t, "the new set serving", func() bool { return state(t, base)["Main.extra"] == true })
}

// TestAnEqualProgramIsNoChange: the project is read again at every
// re-discovery, so each Reload builds a new Program; an equal one does
// not re-bind.
func TestAnEqualProgramIsNoChange(t *testing.T) {
	reload := func() (Plan, error) {
		return Plan{Roots: roots, Program: &Program{
			TagTypes:   map[string]*ir.Type{"Level": {Kind: ir.TypeReal}},
			LocalTypes: map[string]*ir.Type{"count": {Kind: ir.TypeInt}},
			Order:      []string{"Main"},
		}}, nil
	}
	p, _ := reload()
	_, f, _ := startWith(t, Options{Program: p.Program, Reload: reload, Rediscover: 50 * time.Millisecond, Settle: 10 * time.Millisecond})
	time.Sleep(500 * time.Millisecond)
	if n := f.Rebinds(); n != 0 {
		t.Fatalf("rebinds = %d for an equal Program", n)
	}
}

// TestALocalThatIsNotCurrentIsLeftOut: locals carry no quality in a
// frame, so one whose read fails, or all of them while the CPU cannot be
// reached, leave the frame (no pill) instead of showing a held value as
// if live, and come back after a good read. The tags go Bad / Stale.
func TestALocalThatIsNotCurrentIsLeftOut(t *testing.T) {
	s, _, base := startWith(t, Options{Rediscover: -1, Settle: time.Hour})
	locals := func() map[string]any {
		res, err := http.Get(base + "/api/state")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var st struct{ Locals map[string]any }
		if err := json.NewDecoder(res.Body).Decode(&st); err != nil {
			t.Fatal(err)
		}
		return st.Locals
	}
	waitFor(t, "count in the locals", func() bool { return locals()["count"] == float64(4) })

	// One local's read fails: it leaves; the others stay.
	s.Put(`"Main_DB".count`, &webapitest.Var{Datatype: "struct"}) // reads answer "unsupported address"
	waitFor(t, "count gone", func() bool { _, ok := locals()["count"]; return !ok })
	if _, ok := locals()["dwell"]; !ok {
		t.Fatal("dwell left too: only the failing local leaves")
	}
	s.Put(`"Main_DB".count`, &webapitest.Var{Datatype: "int", Value: 6})
	waitFor(t, "count back", func() bool { return locals()["count"] == float64(6) })

	// The CPU drops off: every local leaves while the tags go stale.
	s.StopAnswering()
	waitFor(t, "all locals gone, tags stale", func() bool {
		_, q := stateQ(t, base)
		return len(locals()) == 0 && q["Level"] == "stale"
	})
}
