package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/retain"
)

// hotMain has what a hot takeover must carry on (#710 S2b): a running
// timer, a counter, a plain local, a FIRST_SCAN count, a tag the program
// latches, and a READ that is parked mid-request (never stepped while
// Talk is FALSE, so a test can leave a ticket in it).
const hotMain = `PROGRAM Main
VAR_EXTERNAL Run : BOOL; Setpoint : REAL; Out : REAL; Latch : INT; Mode : INT; Talk : BOOL; END_VAR
VAR
    tmr : TON;
    cnt : CTU;
    tog : BOOL;
    n : DINT;
    firsts : INT;
    rd : READ;
    got : REAL;
END_VAR
IF FIRST_SCAN() THEN firsts := firsts + 1; END_IF;
tmr(IN := Run, PT := T#1h);
tog := NOT tog;
cnt(CU := tog, R := FALSE, PV := 100000);
n := n + 1;
IF n = 3 THEN Latch := 7; END_IF;
IF Talk THEN rd(REQ := TRUE, ID := 'peer', VAR_1 := 'x', RD_1 := got); END_IF;
Out := Setpoint;
END_PROGRAM`

const hotAux = `PROGRAM Aux
VAR_EXTERNAL Setpoint : REAL; END_VAR
VAR k : DINT; END_VAR
k := k + 1;
END_PROGRAM`

// failStore is a memStore whose Load and Save fail while fail is set.
type failStore struct {
	memStore
	mu2        sync.Mutex
	loadFails  bool
	saveFails  bool
	savedAfter int
}

func (f *failStore) Load() (retain.State, error) {
	f.mu2.Lock()
	bad := f.loadFails
	f.mu2.Unlock()
	if bad {
		return retain.State{}, errors.New("retain file unreadable")
	}
	return f.memStore.Load()
}

func (f *failStore) Save(s retain.State) error {
	f.mu2.Lock()
	bad := f.saveFails
	if !bad {
		f.savedAfter++
	}
	f.mu2.Unlock()
	if bad {
		return errors.New("disk full")
	}
	return f.memStore.Save(s)
}

func (f *failStore) set(load, save bool) {
	f.mu2.Lock()
	f.loadFails, f.saveFails = load, save
	f.mu2.Unlock()
}

func hotOptions(clk Clock, main string, store retain.Store, coord Coordinator) Options {
	return Options{
		Program: main,
		Tags: []TagDef{
			State("Run", true),
			Setpoint("Setpoint", 4.5, Retained()),
			State("Out", 0.0),
			State("Latch", int64(0)),
			State("Mode", int64(0)),
			State("Talk", false),
			Input("Level", Init(0.0)),
		},
		Tasks:       []Task{{Name: "aux", Program: hotAux, Scan: time.Hour}},
		Scan:        time.Hour,
		Clock:       clk,
		Retain:      store,
		Coordinator: coord,
	}
}

// hotRig is a leader runtime and its standby, each on its own clock (the
// standby's an hour ahead: never compare the two), the standby's shadow,
// and the standby's takeover flush, which hands over what flush returns.
type hotRig struct {
	lead, stby *Runtime
	lc, sc     *imgClock
	coord      *keepTerm
	sh         *Shadow
	store      *failStore
	img        *Image
	// flush is the standby's takeover flush; by default it takes the
	// shadow's candidate with a 10 s hot-max-age and the image's retain.
	flush func() (*HotImage, error)
}

func newHotRig(t *testing.T, leadMain, stbyMain string) *hotRig {
	t.Helper()
	r := &hotRig{
		lc:    &imgClock{t: time.Unix(1_700_000_000, 0)},
		sc:    &imgClock{t: time.Unix(1_700_003_600, 0)},
		coord: &keepTerm{},
		store: &failStore{},
	}
	var err error
	if r.lead, err = New(hotOptions(r.lc, leadMain, &memStore{}, nil)); err != nil {
		t.Fatal(err)
	}
	if r.stby, err = New(hotOptions(r.sc, stbyMain, r.store, r.coord)); err != nil {
		t.Fatal(err)
	}
	r.sh = r.stby.NewShadow()
	r.flush = r.take(10*time.Second, nil)
	r.stby.SetTakeoverFlush(func() (*HotImage, error) { return r.flush() })
	return r
}

// take is a flush: the shadow's candidate with maxAge, then err.
func (r *hotRig) take(maxAge time.Duration, err error) func() (*HotImage, error) {
	return func() (*HotImage, error) {
		h := r.sh.Take()
		r.sh.Reset()
		if h != nil {
			h.MaxAge = maxAge
			h.Retain = r.img.Retain
		}
		return h, err
	}
}

// lead runs the leader n scans of 100 ms and streams one image (term 4)
// to the standby's shadow, which must apply it whole.
func (r *hotRig) stream(t *testing.T, n int) {
	t.Helper()
	scanBoth(t, r.lead, r.lc, n)
	r.img = takeOne(t, r.lead, r.lc)
	r.img.Term, r.img.Build = 4, "b"
	ret, err := r.lead.RetainImage()
	if err != nil {
		t.Fatal(err)
	}
	r.img.Retain = ret
	rep, err := r.sh.Apply(r.img)
	if err != nil || !rep.InSync() {
		t.Fatalf("shadow apply: %+v, %v", rep, err)
	}
}

// takeOver makes the standby lead in term 4 and scans it until its
// takeover is done and both its tasks scanned once.
func (r *hotRig) takeOver(t *testing.T) TakeoverReport {
	t.Helper()
	r.coord.term.Store(4)
	r.coord.lead.Store(true)
	deadline := time.Now().Add(10 * time.Second)
	for {
		r.stby.Scan()
		if _, lead := r.stby.LeadingTerm(); lead {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the standby never took over")
		}
		time.Sleep(time.Millisecond)
	}
	if err := r.stby.ScanTask("aux"); err != nil {
		t.Fatal(err)
	}
	return r.stby.LastTakeover()
}

// fbSlot is a slot of a function-block instance in a program's frame.
func fbSlot(t *testing.T, p *Program, inst, slot string) ir.Value {
	t.Helper()
	i := slotOf(t, p, inst)
	p.mu.Lock()
	defer p.mu.Unlock()
	fb := p.frame.Slots[i].FB
	for j, s := range fb.Def.AllSlots() {
		if s.Name == slot {
			return fb.Slots[j]
		}
	}
	t.Fatalf("%s has no slot %s", inst, slot)
	return ir.Value{}
}

func local(t *testing.T, p *Program, name string) ir.Value {
	t.Helper()
	i := slotOf(t, p, name)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.frame.Slots[i]
}

func globalI(t *testing.T, rt *Runtime, name string) int64 {
	t.Helper()
	v, err := rt.Tags().ReadGlobal(name)
	if err != nil {
		t.Fatal(err)
	}
	return v.I
}

// T-S2b, the hot path (#710 S2b, design §4.2): the new leader goes on
// from the image, on its own clock an hour off the old leader's. Its
// timer counts the image's age as elapsed (never the clocks' offset), its
// counter and locals go on, a tag the program latched and one an operator
// wrote come with it, FIRST_SCAN() is TRUE once more (until S2 (b)), and
// a READ parked mid-request holds no ticket of the old controller's.
func TestHotTakeoverGoesOnFromTheImage(t *testing.T) {
	r := newHotRig(t, hotMain, hotMain)
	if err := r.lead.Tags().WriteGlobal("Mode", ir.IntVal(3)); err != nil { // an operator's
		t.Fatal(err)
	}
	// A field input, as each controller's own driver reads it: never
	// carried, the new leader keeps its own reading.
	if err := r.lead.Tags().WriteGlobal("Level", ir.RealVal(9)); err != nil {
		t.Fatal(err)
	}
	if err := r.stby.Tags().WriteGlobal("Level", ir.RealVal(1)); err != nil {
		t.Fatal(err)
	}
	// A READ the old leader left BUSY with its ticket 7.
	mp := r.lead.TaskProgram(MainTaskName)
	scanBoth(t, r.lead, r.lc, 1)
	ri := slotOf(t, mp, "rd")
	mp.mu.Lock()
	rd := mp.frame.Slots[ri].FB
	for j, s := range rd.Def.AllSlots() {
		switch s.Name {
		case "BUSY":
			rd.Slots[j] = ir.BoolVal(true)
		case "_ticket":
			rd.Slots[j] = ir.IntVal(7)
		}
	}
	mp.mu.Unlock()
	r.stream(t, 28) // 30 scans after the first: the image's timer has run 3 s
	leadET := fbSlot(t, mp, "tmr", "ET").I
	leadCV := fbSlot(t, mp, "cnt", "CV").I
	leadN := local(t, mp, "n").I
	if leadET != 3000 || leadN != 31 {
		t.Fatalf("leader at the image: ET %d n %d, want 3000 and 31", leadET, leadN)
	}
	r.sc.step(700 * time.Millisecond) // the image is 700 ms old when the standby scans
	// A box that led before: its programs have scanned, FIRST_SCAN() is
	// FALSE there until a takeover makes it TRUE again.
	for _, np := range r.stby.namedPrograms() {
		np.prog.mu.Lock()
		np.prog.ran = true
		np.prog.mu.Unlock()
	}

	rep := r.takeOver(t)
	if !rep.Hot || rep.Reason != "" || !rep.Image {
		t.Fatalf("takeover: %+v, want hot", rep)
	}
	sp := r.stby.TaskProgram(MainTaskName)
	if et := fbSlot(t, sp, "tmr", "ET").I; et != leadET+700 {
		t.Errorf("TON ET %d on the first hot scan, want %d: the image's %d plus its 700 ms age, on the new leader's clock", et, leadET+700, leadET)
	}
	if cv := fbSlot(t, sp, "cnt", "CV").I; cv != leadCV+1 && cv != leadCV {
		t.Errorf("CTU CV %d, want it to go on from the image's %d", cv, leadCV)
	}
	if n := local(t, sp, "n").I; n != leadN+1 {
		t.Errorf("local n %d, want the image's %d plus one scan", n, leadN)
	}
	if f := local(t, sp, "firsts").I; f != 2 {
		t.Errorf("FIRST_SCAN count %d, want 2: the image's 1, and TRUE again on the first hot scan", f)
	}
	if k := local(t, r.stby.TaskProgram("aux"), "k").I; k != 31 {
		t.Errorf("aux's k %d, want the image's 30 plus one scan", k)
	}
	if v := globalI(t, r.stby, "Latch"); v != 7 {
		t.Errorf("Latch %d, want the 7 the old leader's program latched", v)
	}
	if v := globalI(t, r.stby, "Mode"); v != 3 {
		t.Errorf("Mode %d, want the 3 an operator wrote on the old leader", v)
	}
	if v, _ := r.stby.Tags().ReadGlobal("Level"); v.F != 1 {
		t.Errorf("input Level %v after the hot takeover, want the new leader's own reading 1: a field input is never carried", v.F)
	}
	if tk := fbSlot(t, sp, "rd", "_ticket").I; tk != 0 {
		t.Errorf("READ ticket %d after the hot takeover, want 0: the old controller's ticket names nothing here", tk)
	}
	// Run on: the timer keeps the new clock.
	r.sc.step(time.Second)
	r.stby.Scan()
	if et := fbSlot(t, sp, "tmr", "ET").I; et != leadET+1700 {
		t.Errorf("TON ET %d a second on, want %d", et, leadET+1700)
	}
	if f := local(t, sp, "firsts").I; f != 2 {
		t.Errorf("FIRST_SCAN count %d on the second scan, want still 2", f)
	}
}

// Every case that must go cold does, with its reason, and resets every
// task's frame (all tasks or none), keeping what the retain load gave.
func TestHotTakeoverColdCases(t *testing.T) {
	edited := strings.Replace(hotAux, "k := k + 1;", "k := k + 1; (* edited *)", 1)
	for _, c := range []struct {
		name   string
		stby   [2]string // the standby's main and aux, when not the leader's
		setup  func(t *testing.T, r *hotRig)
		reason string
		image  bool
	}{
		{name: "no image", setup: func(t *testing.T, r *hotRig) { r.sh.Reset() }, reason: "no state image"},
		{name: "older than hot-max-age", setup: func(t *testing.T, r *hotRig) {
			r.flush = r.take(500*time.Millisecond, nil)
			r.sc.step(600 * time.Millisecond)
		}, reason: "over hot-max-age", image: true},
		{name: "hot-max-age 0", setup: func(t *testing.T, r *hotRig) { r.flush = r.take(0, nil) }, reason: "hot-max-age is 0", image: true},
		{name: "aux runs another source", stby: [2]string{"", edited}, reason: "runs another source", image: true},
		{name: "the flush failed", setup: func(t *testing.T, r *hotRig) {
			r.flush = r.take(10*time.Second, errors.New("disk full"))
		}, reason: "flush of the newest image failed", image: true},
		{name: "the shadow is not in sync", setup: func(t *testing.T, r *hotRig) {
			img := *r.img
			img.Seq++
			img.Format = 1 // a leader of an older build: no tags
			if _, err := r.sh.Apply(&img); err != nil {
				t.Fatal(err)
			}
		}, reason: "carries no tags", image: true},
		{name: "the pair said cold", setup: func(t *testing.T, r *hotRig) {
			take := r.take(10*time.Second, nil)
			r.flush = func() (*HotImage, error) {
				h, err := take()
				h.Cold = "the image is of another nautilus build"
				return h, err
			}
		}, reason: "another nautilus build", image: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newHotRig(t, hotMain, hotMain)
			if c.stby[1] != "" {
				aux := r.stby.TaskProgram("aux")
				if _, err := aux.SwapWarm(c.stby[1]); err != nil {
					t.Fatal(err)
				}
			}
			r.stream(t, 30)
			if c.setup != nil {
				c.setup(t, r)
			}
			rep := r.takeOver(t)
			if rep.Hot || !strings.Contains(rep.Reason, c.reason) || rep.Image != c.image {
				t.Fatalf("takeover: %+v, want cold for %q (image %v)", rep, c.reason, c.image)
			}
			sp := r.stby.TaskProgram(MainTaskName)
			if n := local(t, sp, "n").I; n != 1 {
				t.Errorf("main's n %d, want 1: a cold takeover resets every task's frame", n)
			}
			if v := globalI(t, r.stby, "Latch"); v != 0 {
				t.Errorf("Latch %d, want 0: a cold takeover carries no tags", v)
			}
		})
	}
}

// A flush that ends after its wait hands over nothing: the takeover went
// on cold, and the late candidate is never installed.
func TestHotTakeoverNeverInstallsALateFlush(t *testing.T) {
	saved := takeoverFlushWait
	takeoverFlushWait = 50 * time.Millisecond
	t.Cleanup(func() { takeoverFlushWait = saved })
	r := newHotRig(t, hotMain, hotMain)
	r.stream(t, 30)
	take := r.take(10*time.Second, nil)
	r.flush = func() (*HotImage, error) {
		time.Sleep(200 * time.Millisecond)
		return take()
	}
	rep := r.takeOver(t)
	if rep.Hot || !strings.Contains(rep.Reason, "did not finish") || !rep.Image {
		t.Fatalf("takeover: %+v, want cold for a late flush, warned of as an image refused", rep)
	}
	time.Sleep(300 * time.Millisecond)
	r.stby.Scan()
	if n := local(t, r.stby.TaskProgram(MainTaskName), "n").I; n != 2 {
		t.Fatalf("main's n %d two scans after a cold takeover, want 2: the late image was installed", n)
	}
}

// The frames are installed by value into the program running after the
// retain load: an online edit the image carries (an FB's body changed,
// the layout not) swaps in first, so the check sees the edited source and
// the new body runs on the carried instance, its count going on.
func TestHotTakeoverRunsTheCarriedOnlineEdit(t *testing.T) {
	const withGain = `FUNCTION_BLOCK Gain
VAR_INPUT x : REAL; END_VAR
VAR_OUTPUT y : REAL; END_VAR
VAR calls : DINT; END_VAR
calls := calls + 1;
y := x * 2.0;
END_FUNCTION_BLOCK
PROGRAM Main
VAR_EXTERNAL Setpoint : REAL; Out : REAL; END_VAR
VAR g : Gain; END_VAR
g(x := Setpoint);
Out := g.y;
END_PROGRAM`
	r := newHotRig(t, withGain, withGain)
	edited := strings.Replace(withGain, "y := x * 2.0;", "y := x * 3.0;", 1)
	if _, err := r.lead.TaskProgram(MainTaskName).SwapWarm(edited); err != nil {
		t.Fatal(err)
	}
	r.stream(t, 3)
	// The standby's file took the image's retain section (the flush).
	var lead retain.State
	if err := json.Unmarshal(r.img.Retain, &lead); err != nil {
		t.Fatal(err)
	}
	if lead.Programs[MainTaskName] != edited {
		t.Fatalf("the image's retain carries no online edit: %v", lead.Programs)
	}
	if err := r.store.memStore.Save(retain.Carry(retain.State{}, lead)); err != nil {
		t.Fatal(err)
	}
	rep := r.takeOver(t)
	if !rep.Hot {
		t.Fatalf("takeover: %+v, want hot", rep)
	}
	if got := r.stby.TaskProgram(MainTaskName).Source(); got != edited {
		t.Fatalf("the standby runs %q, want the edit", got)
	}
	if v, _ := r.stby.Tags().ReadGlobal("Out"); v.F != 4.5*3 {
		t.Fatalf("Out %v after the hot takeover, want %v: the edited FB body runs", v.F, 4.5*3)
	}
	if c := fbSlot(t, r.stby.TaskProgram(MainTaskName), "g", "calls").I; c != 6 {
		t.Fatalf("g.calls %d after the hot takeover, want the image's 5 plus one scan", c)
	}
}

// An own retain store that cannot be read does not stop a hot takeover
// (#710 S2 Q3): the retained values come from the image, held only in
// memory, which RetainOnlyInMemory says until a save succeeds; a save
// that fails keeps saying it (naut-lead, S2b).
func TestHotTakeoverOverAnUnreadableStore(t *testing.T) {
	r := newHotRig(t, hotMain, hotMain)
	if err := r.lead.Tags().WriteGlobal("Setpoint", ir.RealVal(61.5)); err != nil {
		t.Fatal(err)
	}
	r.stream(t, 30)
	r.store.set(true, true)
	r.flush = r.take(10*time.Second, fmt.Errorf("carry: %w", ErrRetainUnreadable))
	rep := r.takeOver(t)
	if !rep.Hot || !rep.RetainFromImage {
		t.Fatalf("takeover: %+v, want hot with the image's retained values", rep)
	}
	if v, _ := r.stby.Tags().ReadGlobal("Setpoint"); v.F != 61.5 {
		t.Fatalf("Setpoint %v, want the image's 61.5", v.F)
	}
	if r.stby.RetainLoadFailed() {
		t.Fatal("RetainLoadFailed after a hot takeover from the image: its leader would stream nothing")
	}
	if !r.stby.RetainOnlyInMemory() {
		t.Fatal("RetainOnlyInMemory false: the retained values are in no store")
	}
	if _, err := r.stby.saveRetainedErr(nil); err == nil {
		t.Fatal("a save to the failing store succeeded")
	}
	if !r.stby.RetainOnlyInMemory() {
		t.Fatal("RetainOnlyInMemory cleared by a save that failed")
	}
	r.store.set(false, false)
	if _, err := r.stby.saveRetainedErr(nil); err != nil {
		t.Fatal(err)
	}
	if r.stby.RetainOnlyInMemory() {
		t.Fatal("RetainOnlyInMemory still set after a save succeeded")
	}
}

// Over an unreadable own store, the image's retained values are for a hot
// takeover only (naut-lead): when a check fails (here aux runs another
// source) the takeover is the plain cold one, on initial values, held
// (RetainLoadFailed: it streams nothing over the standby's good file),
// with nothing of the image's loaded.
func TestHotTakeoverOverAnUnreadableStoreGoesColdOnAFailedCheck(t *testing.T) {
	r := newHotRig(t, hotMain, hotMain)
	edited := strings.Replace(hotAux, "k := k + 1;", "k := k + 1; (* edited *)", 1)
	if _, err := r.stby.TaskProgram("aux").SwapWarm(edited); err != nil {
		t.Fatal(err)
	}
	if err := r.lead.Tags().WriteGlobal("Setpoint", ir.RealVal(61.5)); err != nil {
		t.Fatal(err)
	}
	r.stream(t, 30)
	r.store.set(true, true)
	r.flush = r.take(10*time.Second, fmt.Errorf("carry: %w", ErrRetainUnreadable))
	rep := r.takeOver(t)
	if rep.Hot || rep.RetainFromImage || !strings.Contains(rep.Reason, "another source") {
		t.Fatalf("takeover: %+v, want cold on the source check, with nothing of the image's", rep)
	}
	if v, _ := r.stby.Tags().ReadGlobal("Setpoint"); v.F != 4.5 {
		t.Errorf("Setpoint %v, want its initial 4.5: the image's 61.5 is a hot takeover's only", v.F)
	}
	if !r.stby.RetainLoadFailed() {
		t.Error("RetainLoadFailed false: a cold leader on initial values must hold its stream")
	}
	if r.stby.RetainOnlyInMemory() {
		t.Error("RetainOnlyInMemory set on a cold takeover that loaded nothing of the image's")
	}
}

// A multi-rate image (Fable, #1342, #710 S2c): main copied its frame, then
// ran on for 5 s on the leader before the slow aux task scanned. The image
// is not taken yet: main copies again, after aux's copy (a task's state is
// at most one of its own periods older than the image). Every task is
// carried from the image's one leader reference, not its own copy time, so
// main's timer counts the time since its second copy, and the oldest task
// state installed is aux's (naut-lead's ruling).
func TestHotTakeoverOfAMultiRateImage(t *testing.T) {
	r := newHotRig(t, hotMain, hotMain)
	scanBoth(t, r.lead, r.lc, 30)
	mp := r.lead.TaskProgram(MainTaskName)
	r.lead.RequestImage()
	r.lc.step(100 * time.Millisecond)
	r.lead.Scan() // main copies its frame
	r.lc.step(5 * time.Second)
	if err := r.lead.ScanTask("aux"); err != nil { // the slow task, 5 s on
		t.Fatal(err)
	}
	if r.lead.TakeImage(&Image{}) {
		t.Fatal("an image from main's copy 5 s before aux's")
	}
	r.lc.step(100 * time.Millisecond)
	r.lead.Scan() // main copies again, after aux's copy
	copyET := fbSlot(t, mp, "tmr", "ET").I
	r.lc.step(7 * time.Millisecond) // the image is taken a moment after the last copy
	img := &Image{}
	if !r.lead.TakeImage(img) {
		t.Fatal("no image")
	}
	img.Term, img.Build = 4, "b"
	ret, err := r.lead.RetainImage()
	if err != nil {
		t.Fatal(err)
	}
	img.Retain, r.img = ret, img
	if rep, err := r.sh.Apply(img); err != nil || !rep.InSync() {
		t.Fatalf("apply: %+v, %v", rep, err)
	}
	r.sc.step(300 * time.Millisecond)
	rep := r.takeOver(t)
	if !rep.Hot {
		t.Fatalf("takeover: %+v, want hot", rep)
	}
	if et := fbSlot(t, r.stby.TaskProgram(MainTaskName), "tmr", "ET").I; et != copyET+307 {
		t.Errorf("main's TON ET %d on the first hot scan, want %d: its copy's %d, plus the 7 ms before the image was taken, plus the image's 300 ms age", et, copyET+307, copyET)
	}
	if rep.OldestFrameAge != 407*time.Millisecond {
		t.Errorf("oldest task state %v at the install, want 407ms: aux's copy, 107 ms before the image was taken, plus its 300 ms age", rep.OldestFrameAge)
	}
}
