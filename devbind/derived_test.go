//go:build !js

package devbind

import (
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"context"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/modbus"
	"github.com/joyautomation/nautilus/modbus/slave"
)

func derivedSet(t *testing.T, points ...string) *Set {
	t.Helper()
	s, err := Resolve(vendored(t, "abb-acs355-fmbt21"), []Device{{Name: "CONV", Profile: "abb-acs355-fmbt21", Endpoint: "h", Points: points}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func field(v ir.Value, name string) ir.Value { return v.Fld[v.Struct.FieldIndex[name]] }

// A derived point is a struct member computed from its source: selecting it
// polls the source (a member too) and nothing else, and it is never a
// binding of the driver's manifest.
func TestDerivedPointIsComputedNotPolled(t *testing.T) {
	s := derivedSet(t, "Tripped")
	for _, b := range s.ModbusManifest().Tags {
		if strings.Contains(b.Name, "Tripped") {
			t.Errorf("manifest binds %s: a derived point is not polled", b.Name)
		}
	}
	st, _ := s.TypesST()
	if !strings.Contains(st, "StatusWord : WORD") || !strings.Contains(st, "Tripped : BOOL") || strings.Contains(st, "Current") {
		t.Errorf("types:\n%s", st)
	}
	a := newAssembler(s.devices[0])
	for _, tc := range []struct {
		word int64
		want bool
	}{{0x0008, true}, {0x0237, false}, {0x023F, true}, {0, false}} {
		vals := map[string]any{"CONV.StatusWord": tc.word}
		a.take(vals)
		v := vals["CONV"].(ir.Value)
		if got := field(v, "Tripped").B; got != tc.want || field(v, "StatusWord").I != tc.word {
			t.Errorf("status word %#x: Tripped %v, want %v", tc.word, got, tc.want)
		}
	}
}

// A derived point recomputes only when its source is delivered, and one
// whose source has not arrived is false: "code 0 equals eq: 0" must not
// read true for a register the device never answered.
func TestDerivedWaitsForItsSource(t *testing.T) {
	raw := strings.Replace(string(builtin(t, "abb-acs355-fmbt21")), "{ name: Tripped, from: StatusWord, bit: 3,", "{ name: Idle, from: Actual1, eq: 0,", 1)
	fsys := fstest.MapFS{"profiles/p.yaml": &fstest.MapFile{Data: []byte(strings.Replace(raw, "profile: abb-acs355-fmbt21", "profile: p", 1))}}
	s, err := Resolve(fsys, []Device{{Name: "CONV", Profile: "p", Endpoint: "h", Points: []string{"StatusWord", "Idle"}}})
	if err != nil {
		t.Fatal(err)
	}
	a := newAssembler(s.devices[0])
	vals := map[string]any{"CONV.StatusWord": int64(1)}
	a.take(vals)
	if got := field(vals["CONV"].(ir.Value), "Idle").B; got {
		t.Error("Idle is true before Actual1 has arrived")
	}
	vals = map[string]any{"CONV.Actual1": int64(0)} // arrives at its zero
	a.take(vals)
	if got := field(vals["CONV"].(ir.Value), "Idle").B; !got {
		t.Error("Idle stayed false when Actual1 arrived at 0: a first delivery at the zero value was skipped as unchanged")
	}
	vals = map[string]any{"CONV.Actual1": int64(5)}
	a.take(vals)
	if got := field(vals["CONV"].(ir.Value), "Idle").B; got {
		t.Error("Idle stayed true after Actual1 moved to 5")
	}
}

// The in-process face's copy of the input side computes them too.
func TestNewAssemblyComputesDerived(t *testing.T) {
	s := derivedSet(t)
	mm, _ := s.MergeModbus(modbus.Manifest{})
	md, err := modbus.New(mm)
	if err != nil {
		t.Fatal(err)
	}
	vals := nio.Values{"CONV.StatusWord": int64(0x08)}
	s.WrapModbus(md).NewAssembly()(vals)
	if v, ok := vals["CONV"].(ir.Value); !ok || !field(v, "Tripped").B {
		t.Errorf("Tripped from a fresh assembly: %v", vals)
	}
}

// End to end through a Modbus device: the register's bit, polled, is the
// struct's Tripped.
func TestDerivedFromALiveRegister(t *testing.T) {
	srv := slave.NewServer("", nil)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	srv.Unit(1).SetHolding(3, 0x0008)
	s, err := Resolve(vendored(t, "abb-acs355-fmbt21"), []Device{{Name: "CONV", Profile: "abb-acs355-fmbt21", Endpoint: srv.Addr()}})
	if err != nil {
		t.Fatal(err)
	}
	mm, _ := s.MergeModbus(modbus.Manifest{})
	md, err := modbus.New(mm, modbus.WithScanRate(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	w := s.WrapModbus(md)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	t.Cleanup(func() { cancel(); w.Stop() })
	dst := nio.Values{}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		_ = w.ReadInputsInto(dst)
		if v, ok := dst["CONV"].(ir.Value); ok && field(v, "StatusWord").I == 8 {
			if !field(v, "Tripped").B {
				t.Fatal("status word bit 3 set, Tripped false")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never delivered: %v", dst)
		}
	}
}

// Devices of one type must have the same members, derived ones included:
// two rows of one profile that select different derived points are an error.
func TestDerivedMembersMustAgreeAcrossRowsOfAType(t *testing.T) {
	_, err := Resolve(vendored(t, "abb-acs355-fmbt21"), []Device{
		{Name: "A", Profile: "abb-acs355-fmbt21", Endpoint: "h", Points: []string{"StatusWord"}},
		{Name: "B", Profile: "abb-acs355-fmbt21", Endpoint: "h", Points: []string{"StatusWord", "Tripped"}},
	})
	if err == nil || !strings.Contains(err.Error(), "same points") {
		t.Errorf("err = %v, want a refusal to share the type", err)
	}
	// the same number of members, one of them derived in the second row
	_, err = Resolve(vendored(t, "abb-acs355-fmbt21"), []Device{
		{Name: "A", Profile: "abb-acs355-fmbt21", Endpoint: "h", Points: []string{"StatusWord", "Current"}},
		{Name: "B", Profile: "abb-acs355-fmbt21", Endpoint: "h", Points: []string{"Tripped"}},
	})
	if err == nil {
		t.Error("rows with a derived member in one and a polled one in the other share a type")
	}
}
