package runtime

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/retain"
)

// imgMain is a main program with every kind of state a frame holds: a
// timer, a counter, a STRUCT, an array, a VAR RETAIN local.
const imgMain = `TYPE Pt : STRUCT x : REAL; n : INT; END_STRUCT; END_TYPE
PROGRAM Main
VAR_EXTERNAL Run : BOOL; Setpoint : REAL; Out : REAL; END_VAR
VAR
    tmr : TON;
    cnt : CTU;
    edge : R_TRIG;
    p : Pt;
    hist : ARRAY[0..3] OF REAL;
    i : INT;
END_VAR
VAR RETAIN starts : DINT; END_VAR
tmr(IN := Run, PT := T#10s);
edge(CLK := tmr.Q);
cnt(CU := Run, R := FALSE, PV := 1000);
i := i + 1;
p.x := Setpoint; p.n := i;
hist[i MOD 4] := Setpoint * 2.0;
IF i = 1 THEN starts := starts + 1; END_IF;
Out := p.x;
END_PROGRAM`

const imgTask = `PROGRAM Aux
VAR_EXTERNAL Setpoint : REAL; END_VAR
VAR acc : REAL; t : TOF; END_VAR
acc := acc + Setpoint;
t(IN := acc > 10.0, PT := T#1s);
END_PROGRAM`

type imgClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *imgClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *imgClock) step(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func imgOptions(clk Clock, main string) Options {
	return Options{
		Program: main,
		Tags: []TagDef{
			State("Run", true),
			Setpoint("Setpoint", 4.5, Retained()),
			State("Out", 0.0),
		},
		Tasks:  []Task{{Name: "aux", Program: imgTask, Scan: time.Hour}},
		Scan:   time.Hour,
		Clock:  clk,
		Retain: &memStore{},
	}
}

// memStore is a retain store in memory.
type memStore struct {
	mu sync.Mutex
	st retain.State
}

func (m *memStore) Kind() string { return "memory" }
func (m *memStore) Load() (retain.State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st, nil
}
func (m *memStore) Save(s retain.State) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.st = s
	return nil
}

func newImgRuntime(t *testing.T, clk Clock, main string) *Runtime {
	t.Helper()
	rt, err := New(imgOptions(clk, main))
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

func scanBoth(t *testing.T, rt *Runtime, clk *imgClock, n int) {
	t.Helper()
	for range n {
		clk.step(100 * time.Millisecond)
		rt.Scan()
		if err := rt.ScanTask("aux"); err != nil {
			t.Fatal(err)
		}
	}
}

// An image is made of copies taken at the END of each task's own scan,
// once asked for: not before every task has scanned since the request,
// main first, each with its layout, its timer base and its scan count.
func TestImageTakenAtEachTasksScanEnd(t *testing.T) {
	clk := &imgClock{t: time.Unix(1_700_000_000, 0)}
	rt := newImgRuntime(t, clk, imgMain)
	scanBoth(t, rt, clk, 3)

	var img Image
	if rt.TakeImage(&img) {
		t.Fatal("an image before it was asked for")
	}
	rt.RequestImage()
	if rt.TakeImage(&img) {
		t.Fatal("an image before any scan since the request")
	}
	clk.step(100 * time.Millisecond)
	rt.Scan()
	if rt.TakeImage(&img) {
		t.Fatal("an image before the second task scanned")
	}
	if err := rt.ScanTask("aux"); err != nil {
		t.Fatal(err)
	}
	if !rt.TakeImage(&img) {
		t.Fatal("no image after every task scanned")
	}
	if len(img.Tasks) != 2 || img.Tasks[0].Task != MainTaskName || img.Tasks[1].Task != "aux" {
		t.Fatalf("tasks %+v", img.Tasks)
	}
	for _, ti := range img.Tasks {
		if ti.Layout == "" || len(ti.Leaves) == 0 || ti.NowMs != clk.Now().UnixMilli() {
			t.Fatalf("task %s: layout %q, %d leaves, nowMs %d (clock %d)", ti.Task, ti.Layout, len(ti.Leaves), ti.NowMs, clk.Now().UnixMilli())
		}
	}
	if img.Seq != 1 || img.Tasks[0].Scans != 3 {
		t.Fatalf("seq %d, main's scans %d: want the first image, 3 scans completed before the copy", img.Seq, img.Tasks[0].Scans)
	}
	// Taken once: the next needs a new request and new scans.
	if rt.TakeImage(&img) {
		t.Fatal("the same copy taken twice")
	}
}

// What a leader copies, encoded and decoded, applies to a standby's shadow
// of the same build: its frames equal the leader's, timers and all, and the
// retain section carries the retained values.
func TestImageRoundTripToAStandbysShadow(t *testing.T) {
	clk := &imgClock{t: time.Unix(1_700_000_000, 0)}
	lead := newImgRuntime(t, clk, imgMain)
	if err := lead.Tags().WriteGlobal("Setpoint", ir.RealVal(7.25)); err != nil {
		t.Fatal(err)
	}
	scanBoth(t, lead, clk, 5)
	img := takeOne(t, lead, clk)
	img.Term, img.Build = 4, "test-build"
	ret, err := lead.RetainImage()
	if err != nil {
		t.Fatal(err)
	}
	img.Retain = ret

	wire := mustEncode(t, nil, img)
	var got Image
	if err := DecodeImage(wire, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&got, img) {
		t.Fatalf("decoded image differs:\n got %+v\nwant %+v", got, *img)
	}

	stby := newImgRuntime(t, &imgClock{t: time.Unix(1_600_000_000, 0)}, imgMain)
	sh := stby.NewShadow()
	rep, err := sh.Apply(&got)
	if err != nil || !rep.InSync() || len(rep.Applied) != 2 {
		t.Fatalf("apply: %+v, %v", rep, err)
	}
	for _, np := range lead.namedPrograms() {
		f, nowMs := sh.frameOf(np.name)
		if f == nil {
			t.Fatalf("task %s: no shadow frame", np.name)
		}
		np.prog.mu.Lock()
		want := ir.FlattenInto(nil, np.prog.frame)
		np.prog.mu.Unlock()
		if g := ir.FlattenInto(nil, f); !reflect.DeepEqual(g, want) {
			t.Fatalf("task %s: shadow frame differs from the leader's", np.name)
		}
		if nowMs != clk.Now().UnixMilli() {
			t.Fatalf("task %s: timer base %d, want the leader's %d", np.name, nowMs, clk.Now().UnixMilli())
		}
	}
	if term, scan := sh.Position(); term != 4 || scan != img.Seq {
		t.Fatalf("position (%d, %d)", term, scan)
	}
	var st retain.State
	if err := json.Unmarshal(got.Retain, &st); err != nil {
		t.Fatal(err)
	}
	if st.Tags["Setpoint"] != 7.25 || st.Locals[MainTaskName]["starts"] != float64(1) {
		t.Fatalf("retain section: tags %v locals %v", st.Tags, st.Locals)
	}
}

func takeOne(t *testing.T, rt *Runtime, clk *imgClock) *Image {
	t.Helper()
	rt.RequestImage()
	scanBoth(t, rt, clk, 1)
	img := &Image{}
	if !rt.TakeImage(img) {
		t.Fatal("no image")
	}
	return img
}

// The shadow applies images in (term, seq) order only: an older term, or
// an older seq of the same term, is refused and changes nothing. A reset
// (this controller led) forgets the position, and with it every frame.
func TestShadowRefusesAnOlderImage(t *testing.T) {
	clk := &imgClock{t: time.Unix(1_700_000_000, 0)}
	lead := newImgRuntime(t, clk, imgMain)
	scanBoth(t, lead, clk, 2)
	img := takeOne(t, lead, clk)
	sh := newImgRuntime(t, clk, imgMain).NewShadow()

	apply := func(term, scan uint64) error {
		img.Term, img.Seq = term, scan
		_, err := sh.Apply(img)
		return err
	}
	if err := apply(2, 5); err != nil {
		t.Fatal(err)
	}
	if err := apply(1, 9); err == nil {
		t.Fatal("an image of an older term applied")
	}
	if err := apply(2, 4); err == nil {
		t.Fatal("an older scan of the same term applied")
	}
	if term, scan := sh.Position(); term != 2 || scan != 5 {
		t.Fatalf("a refused image moved the position to (%d, %d)", term, scan)
	}
	if err := apply(2, 6); err != nil {
		t.Fatal(err)
	}
	if err := apply(3, 0); err != nil {
		t.Fatal(err)
	}
	sh.Reset()
	if term, scan := sh.Position(); term != 0 || scan != 0 {
		t.Fatalf("a reset kept the position (%d, %d)", term, scan)
	}
	if f, _ := sh.frameOf(MainTaskName); f != nil {
		t.Fatal("a reset kept a frame")
	}
	if err := apply(1, 0); err != nil {
		t.Fatalf("after a reset: %v", err)
	}
}

// A standby whose program has another layout (an edit not here yet, another
// build) applies none of that task's frame, says so, and still applies the
// tasks that match.
func TestShadowRefusesAnotherLayout(t *testing.T) {
	clk := &imgClock{t: time.Unix(1_700_000_000, 0)}
	lead := newImgRuntime(t, clk, imgMain)
	scanBoth(t, lead, clk, 2)
	img := takeOne(t, lead, clk)
	img.Term = 1

	edited := replaceFirst(t, imgMain, "    i : INT;", "    i : INT;\n    extra : BOOL;")
	sh := newImgRuntime(t, clk, edited).NewShadow()
	rep, err := sh.Apply(img)
	if err != nil {
		t.Fatal(err)
	}
	if rep.InSync() || !reflect.DeepEqual(rep.LayoutDiffers, []string{MainTaskName}) || !reflect.DeepEqual(rep.Applied, []string{"aux"}) {
		t.Fatalf("report %+v", rep)
	}
	if f, _ := sh.frameOf(MainTaskName); f != nil {
		t.Fatal("a frame of another layout was applied")
	}

	if f, _ := sh.frameOf("aux"); f == nil {
		t.Fatal("the matching task's frame was not applied")
	}

	// A task the standby does not run, and one the image lacks. The task
	// this image does not carry has no current frame any more: its older
	// one is not this image's.
	img.Tasks[1].Task = "other"
	img.Seq++
	rep, _ = sh.Apply(img)
	if !reflect.DeepEqual(rep.Missing, []string{"aux"}) || !reflect.DeepEqual(rep.Extra, []string{"other"}) {
		t.Fatalf("report %+v", rep)
	}
	if f, _ := sh.frameOf("aux"); f != nil {
		t.Fatal("a task the newest image lacks still offers the older image's frame")
	}
}

func replaceFirst(t *testing.T, s, old, new string) string {
	t.Helper()
	for i := 0; i+len(old) <= len(s); i++ {
		if s[i:i+len(old)] == old {
			return s[:i] + new + s[i+len(old):]
		}
	}
	t.Fatalf("%q not found", old)
	return ""
}

// Guard (a): the copy a scan makes is values only. Asking for an image
// adds no allocation to a scan (no hashing, no encoding: those are the
// stream's), and taking it hands the buffers back.
func TestImageCopyAddsNoAllocationToTheScan(t *testing.T) {
	clk := &imgClock{t: time.Unix(1_700_000_000, 0)}
	rt := newImgRuntime(t, clk, imgMain)
	scanBoth(t, rt, clk, 3)
	var img Image
	for range 3 { // warm both ping-pong buffers
		rt.RequestImage()
		scanBoth(t, rt, clk, 1)
		rt.TakeImage(&img)
	}
	plain := testing.AllocsPerRun(50, func() { rt.Scan() })
	asked := testing.AllocsPerRun(50, func() {
		rt.RequestImage()
		rt.Scan()
		rt.prog.takeImage(MainTaskName, &img.Tasks[0])
	})
	if asked > plain {
		t.Fatalf("a scan that copies its frame allocates %v, a plain scan %v", asked, plain)
	}
}

// Encoding into a buffer a previous image filled allocates nothing.
func TestImageEncodeReusesItsBuffer(t *testing.T) {
	clk := &imgClock{t: time.Unix(1_700_000_000, 0)}
	rt := newImgRuntime(t, clk, imgMain)
	scanBoth(t, rt, clk, 2)
	img := takeOne(t, rt, clk)
	buf := mustEncode(t, nil, img)
	if n := testing.AllocsPerRun(50, func() { buf, _ = EncodeImage(buf[:0], img) }); n != 0 {
		t.Fatalf("EncodeImage allocates %v", n)
	}
}

// A changed byte anywhere fails the checksum; a cut anywhere is refused;
// neither panics.
func TestImageDecodeRefusesDamage(t *testing.T) {
	clk := &imgClock{t: time.Unix(1_700_000_000, 0)}
	rt := newImgRuntime(t, clk, imgMain)
	scanBoth(t, rt, clk, 2)
	img := takeOne(t, rt, clk)
	img.Retain = []byte(`{"tags":{"Setpoint":4.5}}`)
	wire := mustEncode(t, nil, img)
	var out Image
	for i := range wire {
		bad := append([]byte(nil), wire...)
		bad[i] ^= 0x40
		if err := DecodeImage(bad, &out); !errors.Is(err, ErrImage) {
			t.Fatalf("byte %d flipped: %v", i, err)
		}
	}
	for n := range len(wire) {
		if err := DecodeImage(wire[:n], &out); !errors.Is(err, ErrImage) {
			t.Fatalf("cut at %d: %v", n, err)
		}
	}
}

// sealed is body as a whole image: the header in front, a valid checksum
// behind, so a malformed body reaches the parser instead of dying at the
// checksum.
func sealed(body []byte) []byte { return sealedAs(1, body) }

// sealedAs is body behind a header of the given format and a checksum.
func sealedAs(format byte, body []byte) []byte {
	b := append([]byte{'n', 's', 'i', format}, body...)
	return binary.LittleEndian.AppendUint32(b, crc32.Checksum(b, castagnoli))
}

// Whatever the body behind a valid header and checksum, DecodeImage
// returns: an image or ErrImage, never a panic.
func FuzzDecodeImage(f *testing.F) {
	img := &Image{Term: 3, Seq: 9, Build: "b", Retain: []byte(`{}`), Tasks: []TaskImage{{
		Task: "main", Layout: "abc", NowMs: -5, Scans: 2,
		Leaves: leavesOf(ir.BoolVal(true), ir.IntVal(-7), ir.Value{Kind: ir.TypeInt, I: 10, S: "Run"}, ir.RealVal(1.5), ir.TimeVal(300), ir.StringVal("s"), ir.Value{}),
	}}}
	img.Tasks[0].Source, img.TagsLayout = "src", "tl"
	img.Tasks[0].Tags = TagValues{Idx: []int{0}, Ends: []int{1}, Leaves: leavesOf(ir.IntVal(4))}
	img.Tags = TagValues{Idx: []int{1, 2}, Ends: []int{1, 3}, Leaves: leavesOf(ir.StringVal("op"), ir.RealVal(1), ir.BoolVal(true))}
	for _, format := range ImageFormats {
		wire, err := EncodeImageFormat(nil, img, format)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(format, wire[4:len(wire)-4])
	}
	f.Fuzz(func(t *testing.T, format byte, body []byte) {
		var out Image
		if err := DecodeImage(sealedAs(format, body), &out); err != nil && !errors.Is(err, ErrImage) {
			t.Fatalf("not ErrImage: %v", err)
		}
	})
}

// Bodies with a valid checksum that are malformed are refused, each for
// its own reason.
func TestImageDecodeRefusesMalformedBodies(t *testing.T) {
	uv := func(b []byte, v uint64) []byte { return binary.AppendUvarint(b, v) }
	head := func() []byte { // term, seq, build "", retain ""
		return uv(uv(uv(uv(nil, 1), 1), 0), 0)
	}
	task := func(b []byte, leaves uint64) []byte { // "t", layout "", nowMs 0, scans 0, leaves
		b = uv(b, 1)
		b = append(b, 't')
		b = uv(b, 0)
		b = binary.AppendVarint(b, 0)
		b = uv(b, 0)
		return uv(b, leaves)
	}
	for name, body := range map[string][]byte{
		"a huge task count":      uv(head(), 1<<40),
		"a huge leaf count":      task(uv(head(), 1), 1<<40),
		"a string past the end":  append(uv(uv(uv(nil, 1), 1), 50), 'x'),
		"a retain past the end":  append(uv(uv(uv(uv(nil, 1), 1), 0), 9), '{'),
		"a BOOL of 2":            append(task(uv(head(), 1), 1), byte(ir.TypeBool), 2),
		"a leaf of unknown kind": append(task(uv(head(), 1), 1), 99),
		"a REAL cut short":       append(task(uv(head(), 1), 1), byte(ir.TypeReal), 1, 2, 3),
		"bytes after the last":   append(uv(head(), 0), 7),
		"a truncated varint":     {0x80},
		"an empty body":          {},
	} {
		var out Image
		if err := DecodeImage(sealed(body), &out); !errors.Is(err, ErrImage) {
			t.Errorf("%s: got %v, want ErrImage", name, err)
		}
	}
	// And the well-formed minimum is read.
	var out Image
	if err := DecodeImage(sealed(uv(head(), 0)), &out); err != nil {
		t.Fatalf("the minimal image: %v", err)
	}
	// Format 2: the tags sections (#710 S2).
	tail2 := func(b []byte) []byte { // tags layout "", tagsNowMs 0
		return binary.AppendVarint(uv(b, 0), 0)
	}
	for name, body := range map[string][]byte{
		"no tags layout":            uv(head(), 0),
		"a huge tag count":          uv(tail2(uv(head(), 0)), 1<<40),
		"a tag of huge leaf count":  uv(uv(uv(tail2(uv(head(), 0)), 1), 0), 1<<40),
		"a tag at a huge place":     uv(uv(uv(tail2(uv(head(), 0)), 1), 1<<40), 0),
		"a tag leaf of bad kind":    append(uv(uv(uv(tail2(uv(head(), 0)), 1), 0), 1), 99),
		"a task with no source":     task(uv(head(), 1), 0),
		"bytes after the tags":      append(uv(tail2(uv(head(), 0)), 0), 7),
		"a format this build lacks": nil,
	} {
		wire := sealedAs(2, body)
		if body == nil {
			wire = sealedAs(imageFormat+1, uv(head(), 0))
		}
		if err := DecodeImage(wire, &out); !errors.Is(err, ErrImage) {
			t.Errorf("format 2, %s: got %v, want ErrImage", name, err)
		}
	}
	if err := DecodeImage(sealedAs(2, uv(tail2(uv(head(), 0)), 0)), &out); err != nil || out.Format != 2 {
		t.Fatalf("the minimal format 2 image: %v (format %d)", err, out.Format)
	}
}

// An image over the limits is refused by both ends: the encoder says so
// (a long STRING variable, a huge frame) rather than send what no standby
// would read, and the decoder reads nothing over MaxImageBytes.
func TestImageSizeLimits(t *testing.T) {
	long := &Image{Tasks: []TaskImage{{Task: "main", Leaves: leavesOf(ir.StringVal(strings.Repeat("x", maxImageString+1)))}}}
	if out, err := EncodeImage([]byte("keep"), long); !errors.Is(err, ErrImageTooBig) || string(out) != "keep" {
		t.Fatalf("a STRING over the limit: %d bytes out, %v", len(out), err)
	}
	n := MaxImageBytes / 9 // REAL leaves are 9 bytes on the wire
	big := &Image{Tasks: []TaskImage{{Task: "main", Leaves: make([]ir.Leaf, n+1)}}}
	for i := range big.Tasks[0].Leaves {
		big.Tasks[0].Leaves[i].Kind = ir.TypeReal
	}
	if out, err := EncodeImage(nil, big); !errors.Is(err, ErrImageTooBig) || len(out) != 0 {
		t.Fatalf("a frame over the limit: %d bytes, %v", len(out), err)
	}
	var out Image
	if err := DecodeImage(make([]byte, MaxImageBytes+1), &out); !errors.Is(err, ErrImage) {
		t.Fatalf("decoding over the limit: %v", err)
	}
}

func mustEncode(tb testing.TB, dst []byte, img *Image) []byte {
	tb.Helper()
	b, err := EncodeImage(dst, img)
	if err != nil {
		tb.Fatal(err)
	}
	return b
}

// The fuzz seed's image round-trips exactly, every leaf kind included.
func TestImageCodecEveryLeafKind(t *testing.T) {
	img := &Image{Term: 1 << 40, Seq: 77, Build: "v1", Retain: []byte(`{"tags":{}}`), Tasks: []TaskImage{
		{Task: "main", Layout: "l1", NowMs: 1_700_000_000_123, Scans: 9, Leaves: leavesOf(
			ir.BoolVal(true), ir.BoolVal(false), ir.IntVal(-1<<62), ir.Value{Kind: ir.TypeInt, I: 10, S: "Run"},
			ir.RealVal(-0.125), ir.TimeVal(86_400_000), ir.StringVal("héllo"), ir.Value{},
		)},
		{Task: "aux", Layout: "l2"},
	}}
	img.Tasks[0].Source, img.Tasks[1].Source, img.TagsLayout, img.TagsNowMs = "s1", "s2", "tl", 1_700_000_000_456
	img.Tasks[0].Tags = TagValues{Idx: []int{0, 3}, Ends: []int{1, 3}, Leaves: leavesOf(ir.BoolVal(true), ir.RealVal(2.5), ir.Value{Kind: ir.TypeInt, I: 10, S: "Run"})}
	img.Tags = TagValues{Idx: []int{1}, Ends: []int{1}, Leaves: leavesOf(ir.StringVal("op"))}
	img.Format = imageFormat
	var out Image
	if err := DecodeImage(mustEncode(t, nil, img), &out); err != nil {
		t.Fatal(err)
	}
	normImage(&out)
	if !reflect.DeepEqual(&out, img) {
		t.Fatalf("got %+v\nwant %+v", out, *img)
	}
	// Format 1 is the same image without its tags and sources.
	wire, err := EncodeImageFormat(nil, img, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := DecodeImage(wire, &out); err != nil {
		t.Fatal(err)
	}
	normImage(&out)
	want := *img
	want.Format, want.TagsLayout, want.TagsNowMs, want.Tags = 1, "", 0, TagValues{}
	want.Tasks = append([]TaskImage(nil), img.Tasks...)
	for i := range want.Tasks {
		want.Tasks[i].Source, want.Tasks[i].Tags = "", TagValues{}
	}
	if !reflect.DeepEqual(&out, &want) {
		t.Fatalf("format 1: got %+v\nwant %+v", out, want)
	}
}

// normImage turns the empty slices a decode leaves into nil, as a literal
// writes them.
func normImage(img *Image) {
	nilTags := func(tv *TagValues) {
		if len(tv.Idx) == 0 {
			*tv = TagValues{}
		}
	}
	nilTags(&img.Tags)
	for i := range img.Tasks {
		if len(img.Tasks[i].Leaves) == 0 {
			img.Tasks[i].Leaves = nil
		}
		nilTags(&img.Tasks[i].Tags)
	}
}

func leavesOf(vs ...ir.Value) []ir.Leaf {
	out := make([]ir.Leaf, len(vs))
	for i := range vs {
		out[i] = ir.LeafOf(&vs[i])
	}
	return out
}

// An online edit on the leader resets its program's scan count, never the
// stream's order: the image after the edit still applies to a standby that
// applied the one before (it would be refused as older for as long as the
// count took to catch up).
func TestImageAfterAnOnlineEditStillApplies(t *testing.T) {
	clk := &imgClock{t: time.Unix(1_700_000_000, 0)}
	lead := newImgRuntime(t, clk, imgMain)
	scanBoth(t, lead, clk, 20)
	sh := newImgRuntime(t, clk, imgMain).NewShadow()
	before := takeOne(t, lead, clk)
	before.Term = 1
	if _, err := sh.Apply(before); err != nil {
		t.Fatal(err)
	}
	// A body-only edit: the layout stays, the scan count goes back to 0.
	if _, err := lead.prog.SwapWarm(replaceFirst(t, imgMain, "i := i + 1;", "i := i + 2;")); err != nil {
		t.Fatal(err)
	}
	after := takeOne(t, lead, clk)
	after.Term = 1
	if after.Tasks[0].Scans >= before.Tasks[0].Scans {
		t.Fatalf("the edit did not reset the scan count (%d, then %d): the test proves nothing", before.Tasks[0].Scans, after.Tasks[0].Scans)
	}
	rep, err := sh.Apply(after)
	if err != nil || !rep.InSync() {
		t.Fatalf("the image after an online edit: %+v, %v", rep, err)
	}
}

// The stream takes the retain section from its own goroutine while the
// scans run (go test -race).
func TestRetainImageWhileRunning(t *testing.T) {
	o := imgOptions(nil, imgMain)
	o.Scan, o.Tasks[0].Scan = time.Millisecond, time.Millisecond
	rt, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rt.Run(ctx); close(done) }()
	var img Image
	deadline := time.Now().Add(300 * time.Millisecond)
	taken := 0
	for time.Now().Before(deadline) {
		if _, err := rt.RetainImage(); err != nil {
			t.Fatal(err)
		}
		rt.RequestImage()
		if rt.TakeImage(&img) {
			taken++
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if taken == 0 {
		t.Fatal("no image taken from a running runtime")
	}
}
