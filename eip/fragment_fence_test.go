package eip

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/eip/logix"
	"github.com/joyautomation/nautilus/eip/logixserver"
	"github.com/joyautomation/nautilus/internal/fence"
)

// bigArrayEmulator is a controller with one DINT[200]: 800 bytes, more
// than one request carries, so a whole-array write goes as Write Tag
// Fragmented. The emulator applies it only once every fragment arrived.
func bigArrayEmulator(t *testing.T) (string, int, *logixserver.TagStore) {
	t.Helper()
	spec := &logixserver.TagSurfaceSpec{
		ControllerName: "Frag",
		Symbols:        []logixserver.SymbolSpec{{Name: "Big", Datatype: "DINT", Dims: []uint32{200}}},
	}
	for i := 0; i < 200; i++ {
		spec.Tags = append(spec.Tags, logixserver.TagSpec{Path: fmt.Sprintf("Big[%d]", i), Datatype: "DINT"})
	}
	schema, tags, name, err := logixserver.CompileSurface(spec)
	if err != nil {
		t.Fatalf("compile surface: %v", err)
	}
	store := logixserver.NewTagStore()
	for _, tc := range tags {
		store.Set(tc.Path, tc.LeafType, tc.Default)
	}
	srv := logixserver.NewServer(store, schema, name, "127.0.0.1:0", nil)
	if err := srv.Listen(); err != nil {
		t.Fatalf("emulator listen: %v", err)
	}
	_, ps, _ := net.SplitHostPort(srv.Addr())
	port, _ := strconv.Atoi(ps)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Run(ctx) }()
	return "127.0.0.1", port, store
}

// 3b-3b-2: a Write Tag Fragmented reads the pair's gate before EACH
// fragment (fence.Check in cip.SendUnitData). With the gate closing after
// the first fragment, the rest never goes, so the controller never
// assembles the write; with it open throughout, the write lands.
// fragments_unchecked turns it red.
func TestFragmentedWriteStopsWhenTheGateCloses(t *testing.T) {
	host, port, store := bigArrayEmulator(t)
	ctrl, err := logix.Dial(context.Background(), host, logix.WithPort(port))
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	data := make([]byte, 800)
	for i := 0; i < 200; i++ {
		binary.LittleEndian.PutUint32(data[4*i:], uint32(1000+i))
	}
	last := func() any { _, v, _ := store.Resolve("Big[199]"); return v }

	var looks atomic.Int64
	closesAfterFirst := func() (bool, time.Duration, uint64) { return looks.Add(1) <= 1, time.Second, 5 }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = ctrl.WriteTagArray(fence.WithGate(ctx, closesAfterFirst, 5), "Big", 0xC4, 200, data)
	if !errors.Is(err, fence.ErrGateClosed) {
		t.Fatalf("a fragmented write whose gate closed after the first fragment: %v; want ErrGateClosed", err)
	}
	if looks.Load() < 2 {
		t.Fatalf("the gate was read %d time(s); the write was not fragmented (the test needs it to be)", looks.Load())
	}
	if v := last(); v == float64(1199) || v == int64(1199) {
		t.Fatalf("Big[199] = %v: the rest of the write went out after the gate closed", v)
	}
	if ctrl.Broken() {
		t.Fatal("a fragment refused before sending broke the session")
	}

	open := func() (bool, time.Duration, uint64) { return true, time.Second, 5 }
	if err := ctrl.WriteTagArray(fence.WithGate(ctx, open, 5), "Big", 0xC4, 200, data); err != nil {
		t.Fatalf("the same write with the gate open: %v", err)
	}
	if v := last(); v != float64(1199) && v != int64(1199) {
		t.Fatalf("Big[199] = %v (%T) after the open-gate write; want 1199", v, v)
	}
}
