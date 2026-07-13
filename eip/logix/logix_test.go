package logix

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/eip/cip"
	"github.com/joyautomation/nautilus/eip/logixserver"
)

// startEmulator brings up the in-repo Logix emulator on a free loopback port
// and returns its address plus the seeded tag store.
func startEmulator(t *testing.T, spec *logixserver.TagSurfaceSpec) (string, *logixserver.TagStore) {
	t.Helper()
	schema, tags, name, err := logixserver.CompileSurface(spec)
	if err != nil {
		t.Fatalf("compile surface: %v", err)
	}
	store := logixserver.NewTagStore()
	for _, tc := range tags {
		store.Set(tc.Path, tc.LeafType, tc.Default)
	}

	// Reserve a free port, then hand it to the server.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	srv := logixserver.NewServer(store, schema, name, addr, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Run(ctx) }()

	// Wait until the listener answers.
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return addr, store
		}
		if time.Now().After(deadline) {
			t.Fatalf("emulator never came up on %s", addr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func testSpec() *logixserver.TagSurfaceSpec {
	return &logixserver.TagSurfaceSpec{
		ControllerName: "TestController",
		Templates: []logixserver.TemplateSpec{
			{Name: "Header_Type", Members: []logixserver.MemberSpec{
				{Name: "Displacement", Datatype: "REAL"},
				{Name: "Valid", Datatype: "BOOL"},
			}},
			{Name: "Plt_Type", Members: []logixserver.MemberSpec{
				{Name: "Header", Datatype: "Header_Type"},
				{Name: "Count", Datatype: "DINT"},
			}},
		},
		Symbols: []logixserver.SymbolSpec{
			{Name: "Speed", Datatype: "REAL"},
			{Name: "Enable", Datatype: "BOOL"},
			{Name: "TRS", Datatype: "Plt_Type"},
			{Name: "Program:MainProgram", Program: true},
			{Name: "Motor", Scope: "Program:MainProgram", Datatype: "DINT"},
		},
		Tags: []logixserver.TagSpec{
			{Path: "Speed", Datatype: "REAL"},
			{Path: "Enable", Datatype: "BOOL"},
			{Path: "TRS.Header.Displacement", Datatype: "REAL"},
			{Path: "TRS.Header.Valid", Datatype: "BOOL"},
			{Path: "TRS.Count", Datatype: "DINT"},
			{Path: "Program:MainProgram.Motor", Datatype: "DINT"},
		},
	}
}

func dialTest(t *testing.T, addr string) *Controller {
	t.Helper()
	host, portStr, _ := net.SplitHostPort(addr)
	var port int
	_, _ = fmt.Sscanf(portStr, "%d", &port)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, host, WithPort(port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestBrowseAgainstEmulator(t *testing.T) {
	addr, _ := startEmulator(t, testSpec())
	c := dialTest(t, addr)
	ctx := context.Background()

	br, err := c.Browse(ctx)
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	names := map[string]Symbol{}
	for _, s := range br.Symbols {
		names[s.Name] = s
	}
	for _, want := range []string{"Speed", "Enable", "TRS", "Program:MainProgram.Motor"} {
		if _, ok := names[want]; !ok {
			t.Errorf("browse missing symbol %q (got %v)", want, br.Symbols)
		}
	}
	if len(br.Programs) != 1 || br.Programs[0] != "MainProgram" {
		t.Errorf("programs = %v, want [MainProgram]", br.Programs)
	}

	plt, ok := br.TemplateByName("Plt_Type")
	if !ok {
		t.Fatalf("Plt_Type template not uploaded; have %v", br.Templates)
	}
	if len(plt.Members) != 2 || plt.Members[0].Name != "Header" || plt.Members[1].Name != "Count" {
		t.Fatalf("Plt_Type members wrong: %+v", plt.Members)
	}
	if !plt.Members[0].IsStruct() {
		t.Errorf("Header member should be a struct reference")
	}
	nested, ok := br.Templates[plt.Members[0].NestedID()]
	if !ok || nested.Name != "Header_Type" {
		t.Fatalf("nested template not resolved: %v", br.Templates)
	}
}

func TestReadWriteScalars(t *testing.T) {
	addr, store := startEmulator(t, testSpec())
	c := dialTest(t, addr)
	ctx := context.Background()

	store.UpdateValue("Speed", 42.5)
	store.UpdateValue("Enable", true)

	raw, err := c.ReadTag(ctx, "Speed", 1)
	if err != nil {
		t.Fatalf("read Speed: %v", err)
	}
	reg := NewRegistry(nil)
	v, err := reg.Decode(raw)
	if err != nil {
		t.Fatalf("decode Speed: %v", err)
	}
	if v.Scalar != float64(42.5) {
		t.Errorf("Speed = %v, want 42.5", v.Scalar)
	}

	raw, err = c.ReadTag(ctx, "Enable", 1)
	if err != nil {
		t.Fatalf("read Enable: %v", err)
	}
	v, _ = reg.Decode(raw)
	if v.Scalar != true {
		t.Errorf("Enable = %v, want true", v.Scalar)
	}

	// Write and read back.
	data, err := EncodeScalar(0xCA, 99.25)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := c.WriteTag(ctx, "Speed", 0xCA, 1, data); err != nil {
		t.Fatalf("write Speed: %v", err)
	}
	raw, _ = c.ReadTag(ctx, "Speed", 1)
	v, _ = reg.Decode(raw)
	if v.Scalar != float64(99.25) {
		t.Errorf("Speed after write = %v, want 99.25", v.Scalar)
	}

	// Program-scoped write+read.
	data, _ = EncodeScalar(0xC4, int64(-7))
	if err := c.WriteTag(ctx, "Program:MainProgram.Motor", 0xC4, 1, data); err != nil {
		t.Fatalf("write Motor: %v", err)
	}
	raw, err = c.ReadTag(ctx, "Program:MainProgram.Motor", 1)
	if err != nil {
		t.Fatalf("read Motor: %v", err)
	}
	v, _ = reg.Decode(raw)
	if v.Scalar != int64(-7) {
		t.Errorf("Motor = %v, want -7", v.Scalar)
	}
}

func TestReadStructRoot(t *testing.T) {
	addr, store := startEmulator(t, testSpec())
	c := dialTest(t, addr)
	ctx := context.Background()

	store.UpdateValue("TRS.Header.Displacement", 3.5)
	store.UpdateValue("TRS.Header.Valid", true)
	store.UpdateValue("TRS.Count", 12.0)

	br, err := c.Browse(ctx)
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	reg := NewRegistry(br.Templates)

	raw, err := c.ReadTag(ctx, "TRS", 1)
	if err != nil {
		t.Fatalf("read TRS: %v", err)
	}
	if raw.Type != cip.TypeStruct {
		t.Fatalf("TRS type = 0x%04x, want 0x02A0", raw.Type)
	}
	v, err := reg.Decode(raw)
	if err != nil {
		t.Fatalf("decode TRS: %v", err)
	}
	if v.Type != "Plt_Type" || len(v.Fields) != 2 {
		t.Fatalf("TRS decoded to %+v", v)
	}
	header := v.Fields[0].Value
	if header.Type != "Header_Type" || len(header.Fields) != 2 {
		t.Fatalf("Header decoded to %+v", header)
	}
	if header.Fields[0].Value.Scalar != float64(3.5) {
		t.Errorf("Displacement = %v, want 3.5", header.Fields[0].Value.Scalar)
	}
	if header.Fields[1].Value.Scalar != true {
		t.Errorf("Valid = %v, want true", header.Fields[1].Value.Scalar)
	}
	if v.Fields[1].Value.Scalar != int64(12) {
		t.Errorf("Count = %v, want 12", v.Fields[1].Value.Scalar)
	}
}

func TestBatchedReads(t *testing.T) {
	addr, store := startEmulator(t, testSpec())
	c := dialTest(t, addr)
	ctx := context.Background()

	store.UpdateValue("Speed", 1.5)
	store.UpdateValue("Enable", true)
	store.UpdateValue("Program:MainProgram.Motor", 33.0)

	tags := []string{"Speed", "Enable", "Program:MainProgram.Motor", "DoesNotExist"}
	sizes := map[string]int{"Speed": 4, "Enable": 1, "Program:MainProgram.Motor": 4, "DoesNotExist": 4}
	results := c.ReadTags(ctx, tags, func(tag string) int { return sizes[tag] })

	reg := NewRegistry(nil)
	want := map[string]any{"Speed": 1.5, "Enable": true, "Program:MainProgram.Motor": int64(33)}
	for i, r := range results {
		if r.Tag == "DoesNotExist" {
			if r.Err == nil {
				t.Errorf("expected error for missing tag")
			}
			continue
		}
		if r.Err != nil {
			t.Errorf("result %d (%s): %v", i, r.Tag, r.Err)
			continue
		}
		v, err := reg.Decode(r.RawTag)
		if err != nil {
			t.Errorf("decode %s: %v", r.Tag, err)
			continue
		}
		if v.Scalar != want[r.Tag] {
			t.Errorf("%s = %v, want %v", r.Tag, v.Scalar, want[r.Tag])
		}
	}
}

func TestEncodeTagPathRoundTrip(t *testing.T) {
	cases := []string{
		"Motor1",
		"Motor1.Cmd.Speed",
		"Program:MainProgram.Counts[4]",
		"Grid[2][3].Val",
		"Plt[300].Header.Displacement",
	}
	for _, tc := range cases {
		path, err := EncodeTagPath(tc)
		if err != nil {
			t.Fatalf("%s: %v", tc, err)
		}
		segs, err := cip.ParseSymbolicTag(path)
		if err != nil {
			t.Fatalf("%s: parse back: %v", tc, err)
		}
		if got := cip.CanonicalTagPath(segs); got != tc {
			t.Errorf("round trip %q -> %q", tc, got)
		}
	}
}
