//go:build !js

package project

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"testing"
	"testing/fstest"
	"time"

	"github.com/joyautomation/nautilus/eip/logixserver"
	"github.com/joyautomation/nautilus/runtime"
)

// startLogix serves one REAL tag, Speed, from an in-process Logix emulator.
func startLogix(t *testing.T) (port int, store *logixserver.TagStore) {
	t.Helper()
	schema, tags, name, err := logixserver.CompileSurface(&logixserver.TagSurfaceSpec{
		ControllerName: "Peer",
		Symbols:        []logixserver.SymbolSpec{{Name: "Speed", Datatype: "REAL"}},
		Tags:           []logixserver.TagSpec{{Path: "Speed", Datatype: "REAL"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	store = logixserver.NewTagStore()
	for _, tc := range tags {
		store.Set(tc.Path, tc.LeafType, tc.Default)
	}
	srv := logixserver.NewServer(store, schema, name, "127.0.0.1:0", nil)
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	_, ps, _ := net.SplitHostPort(srv.Addr())
	port, _ = strconv.Atoi(ps)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Run(ctx) }()
	return port, store
}

// channels: in nautilus.yaml, end to end: a READ on a cip channel lands the
// peer's value, and a READ on a peer that never answers fails on its
// timeout while the scan runs on, untouched.
func TestChannelsEndToEnd(t *testing.T) {
	port, store := startLogix(t)
	store.UpdateValue("Speed", float64(12.5))
	fsys := fstest.MapFS{
		"nautilus.yaml": {Data: []byte(fmt.Sprintf(`tasks:
  - program: main.st
    scan: 10ms
tags:
  - {name: Go, type: BOOL, role: setpoint}
  - {name: Got, type: REAL, role: state}
  - {name: Ndr, type: BOOL, role: state}
  - {name: DeadErr, type: BOOL, role: state}
  - {name: DeadSt, type: INT, role: state}
  - {name: Lost, type: REAL, role: state}
  - {name: Scans, type: DINT, role: state}
channels:
  - {name: peer, type: cip, host: 127.0.0.1, port: %d}
  - {name: dead, type: cip, host: 192.0.2.1, timeout: 300ms}
`, port))},
		"main.st": {Data: []byte(`PROGRAM Main
VAR_EXTERNAL Go : BOOL; Got : REAL; Ndr : BOOL; DeadErr : BOOL; DeadSt : INT; Lost : REAL; Scans : DINT; END_VAR
VAR rd : READ; dead : READ; END_VAR
rd(REQ := Go, ID := 'peer', VAR_1 := 'Speed', RD_1 := Got);
Ndr := rd.NDR;
dead(REQ := Go, ID := 'dead', VAR_1 := 'Speed', RD_1 := Lost);
DeadErr := dead.ERROR; DeadSt := dead.STATUS;
Scans := Scans + 1;
END_PROGRAM`)},
	}
	proj, err := Load(fsys, ManifestName)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	if u := rt.UnknownChannels(); len(u) != 0 {
		t.Fatalf("UnknownChannels = %v", u)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go rt.Run(ctx)
	tags := rt.Tags()
	read := func(n string) any { v, _ := tags.ReadGlobal(n); return v }
	tags.Set("Go", true)

	deadline := time.Now().Add(5 * time.Second)
	for {
		ndr, _ := tags.ReadGlobal("Ndr")
		de, _ := tags.ReadGlobal("DeadErr")
		if ndr.B && de.B {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Ndr=%v DeadErr=%v Got=%v", read("Ndr"), read("DeadErr"), read("Got"))
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got, _ := tags.ReadGlobal("Got"); got.F != 12.5 {
		t.Errorf("Got = %v, want 12.5", got.F)
	}
	if st, _ := tags.ReadGlobal("DeadSt"); st.I != -3 && st.I != -2 {
		t.Errorf("DeadSt = %d, want -3 (timeout) or -2 (unreachable)", st.I)
	}
	// 300 ms of a dead peer at a 10 ms scan: the scan kept its pace.
	if s, _ := tags.ReadGlobal("Scans"); s.I < 15 {
		t.Errorf("only %d scans ran while a request hung: the scan waited on the channel", s.I)
	}
}
