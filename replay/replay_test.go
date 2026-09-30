package replay

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
)

// A four-sample recording, 60 s apart: SW1_Port25 carries traffic and
// drops link for one sample; the first InBps sample was never scraped.
const recording = `{"t0": 1000, "step": 60, "n": 4,
 "bool": ["SW1_Port25.OperUp"],
 "series": {
  "SW1_Port25.InBps":  [null, 600, 1200, 1800],
  "SW1_Port25.OperUp": [1, 1, 0, 1],
  "SW1_Port25.SpeedMbps": [10000, 10000, null, 10000],
  "NEVER.Seen": [null, null, null, null]
 }}`

func gz(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	_, _ = w.Write([]byte(s))
	_ = w.Close()
	return b.Bytes()
}

func TestLoadHistoryFillsGaps(t *testing.T) {
	for name, raw := range map[string][]byte{"plain": []byte(recording), "gzip": gz(t, recording)} {
		h, err := LoadHistory(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := h.Series["SW1_Port25.InBps"]; got[0] != 600 {
			t.Errorf("%s: a late series back-fills its first value: %v", name, got)
		}
		if got := h.Series["SW1_Port25.SpeedMbps"]; got[2] != 10000 {
			t.Errorf("%s: a gap holds the last value: %v", name, got)
		}
		if _, ok := h.Series["NEVER.Seen"]; ok {
			t.Errorf("%s: a series never sampled is absent", name)
		}
		if h.End() != 1180 {
			t.Errorf("%s: End = %d", name, h.End())
		}
	}
	if _, err := LoadHistory(strings.NewReader(`{"t0":1,"step":60,"n":2,"series":{"A.B":[1]}}`)); err == nil {
		t.Error("a short series must be an error")
	}
}

func TestHistoryAt(t *testing.T) {
	h, _ := LoadHistory(strings.NewReader(recording))
	cases := []struct {
		name string
		t    float64
		want float64
	}{
		{"SW1_Port25.InBps", 1090, 900},  // halfway between 600 and 1200
		{"SW1_Port25.InBps", 0, 600},     // before: clamped
		{"SW1_Port25.InBps", 9999, 1800}, // after: clamped
		{"SW1_Port25.OperUp", 1119, 1},   // a bool steps: still the 1 at 1060
		{"SW1_Port25.OperUp", 1120, 0},   // and drops exactly at its sample
	}
	for _, c := range cases {
		if got, _ := h.At(c.name, c.t); got != c.want {
			t.Errorf("%s at %v = %v, want %v", c.name, c.t, got, c.want)
		}
	}
}

func TestClock(t *testing.T) {
	c := Clock{From: 1000, To: 1180, Speed: 1}
	c.Seek(1000)
	t0 := time.Unix(0, 0)
	c.Advance(t0)
	if got := c.Advance(t0.Add(10 * time.Second)); got != 1010 {
		t.Fatalf("1×: %v", got)
	}
	c.Speed = 60
	if got := c.Advance(t0.Add(11 * time.Second)); got != 1070 {
		t.Fatalf("60×: %v", got)
	}
	c.Pause = true
	if got := c.Advance(t0.Add(20 * time.Second)); got != 1070 {
		t.Fatalf("paused: %v", got)
	}
	c.Pause = false
	if got := c.Advance(t0.Add(23 * time.Second)); got != 1070+180-180 || c.Loops != 1 {
		t.Fatalf("wrap: pos %v loops %d", got, c.Loops)
	}
	c.Seek(5000)
	if c.Pos != 1179 {
		t.Fatalf("seek past the end clamps inside: %v", c.Pos)
	}
}

const manifest = `history: rec.json.gz
tags:
  - name: Rec_SW1_Port25
    type: SwitchPort
    series: SW1_Port25
    const: { Name: "TGigaEthernet0/25", Index: 189, AdminUp: true }
`

func member(t *testing.T, v any, name string) ir.Value {
	t.Helper()
	iv, ok := v.(ir.Value)
	if !ok {
		t.Fatalf("not a struct: %T", v)
	}
	return iv.Fld[iv.Struct.FieldIndex[name]]
}

func TestDriverDelivers(t *testing.T) {
	m, err := ParseManifest([]byte(manifest))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(0, 0)
	fsys := fstest.MapFS{"rec.json.gz": {Data: gz(t, recording)}}
	d, err := New(m, fsys, WithClock(func() time.Time { return now }), WithLogger(quiet()))
	if err != nil {
		t.Fatal(err)
	}
	d.Start(context.Background())
	v, err := d.ReadInputs()
	if err != nil {
		t.Fatal(err)
	}
	if v[TagAt] != int64(1000) || v[TagFrom] != int64(1000) || v[TagTo] != int64(1180) {
		t.Fatalf("clock tags: %v %v %v", v[TagAt], v[TagFrom], v[TagTo])
	}
	p := v["Rec_SW1_Port25"]
	if member(t, p, "InBps").F != 600 || !member(t, p, "OperUp").B || member(t, p, "SpeedMbps").F != 10000 {
		t.Fatalf("recorded members: %+v", p)
	}
	if member(t, p, "Name").S != "TGigaEthernet0/25" || member(t, p, "Index").I != 189 || !member(t, p, "AdminUp").B {
		t.Fatal("constants")
	}
	if member(t, p, "OutBps").F != 0 {
		t.Fatal("a member neither recorded nor constant is zero")
	}

	// Steer it: 60× for 2 s lands on the link-down sample.
	if err := d.WriteOutputs(nio.Values{TagSpeed: 60.0, TagPause: false}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	v, _ = d.ReadInputs()
	if v[TagAt] != int64(1120) || member(t, v["Rec_SW1_Port25"], "OperUp").B {
		t.Fatalf("at %v: OperUp %v", v[TagAt], member(t, v["Rec_SW1_Port25"], "OperUp").B)
	}
	// Seek, as the HMI's scrubber does; the same value again is not a new seek.
	_ = d.WriteOutputs(nio.Values{TagSeek: int64(1060), TagPause: true})
	now = now.Add(5 * time.Second)
	v, _ = d.ReadInputs()
	if v[TagAt] != int64(1060) {
		t.Fatalf("seek: %v", v[TagAt])
	}
	_ = d.WriteOutputs(nio.Values{TagSeek: int64(1060), TagPause: false})
	now = now.Add(time.Second)
	v, _ = d.ReadInputs()
	if v[TagAt] != int64(1120) {
		t.Fatalf("an unchanged seek must not jump back: %v", v[TagAt])
	}
	h := d.Health()
	if !h.Loaded || h.Matched != 1 || h.Speed != 60 {
		t.Fatalf("health %+v", h)
	}
	if q := d.Quality(); q != nil {
		t.Fatalf("loaded: quality %v", q)
	}
}

func TestDriverWithoutItsRecording(t *testing.T) {
	m, _ := ParseManifest([]byte(manifest))
	d, err := New(m, fstest.MapFS{}, WithLogger(quiet()))
	if err != nil {
		t.Fatal("building must not need the recording: ", err)
	}
	d.Start(context.Background())
	v, err := d.ReadInputs()
	if err != nil || v[TagAt] != int64(0) {
		t.Fatalf("no recording: the clock reads 0 (%v, %v)", v[TagAt], err)
	}
	// Every tag is still delivered, at its constants, so a program reading
	// it does not fault.
	if p := v["Rec_SW1_Port25"]; member(t, p, "Index").I != 189 || member(t, p, "InBps").F != 0 {
		t.Fatalf("no recording: %+v", p)
	}
	if d.Quality()["Rec_SW1_Port25"] != nio.NotConnected {
		t.Fatal("tags must read NotConnected")
	}
	if h := d.Health(); h.Loaded || !strings.Contains(h.LastError, "rec.json.gz") {
		t.Fatalf("health %+v", h)
	}
}

func TestManifestErrors(t *testing.T) {
	for _, bad := range []string{
		"tags: []",                    // no history
		"history: x\nfrom: yesterday", // bad time
		"history: x\nfrom: 2026-09-29T00:00:00Z\nto: 2026-09-28T00:00:00Z",
		"history: x\ntags: [{name: A, type: Blender, series: A}]",
		"history: x\ntags: [{name: A, type: SwitchPort}]",
		"history: x\ntags: [{name: A, type: SwitchPort, series: A, const: {Nmae: x}}]",
		"history: x\ntags: [{name: Replay_At, type: SwitchPort, series: A}]",
		"history: x\ntags: [{name: A, type: SwitchPort, series: A, const: {OperUp: maybe}}]",
		"history: x\ntgas: []",
	} {
		if _, err := ParseManifest([]byte(bad)); err == nil {
			t.Errorf("accepted:\n%s", bad)
		}
	}
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
