//go:build !js

package project

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/devbind"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/opcua"
	"github.com/joyautomation/nautilus/server"
)

// wrapped is a driver that hides the master adapter under an Unwrap, as a
// gate around it would.
type wrapped struct{ nio.Driver }

func (w wrapped) Unwrap() nio.Driver { return w.Driver }

func portMaster(t *testing.T) *devbind.OPCUADriver {
	t.Helper()
	prof, err := os.ReadFile("../../devprofile/profiles/sick-sig300.yaml")
	if err != nil {
		t.Fatal(err)
	}
	level, err := os.ReadFile("../../iolink/iodd/testdata/acme-level100-20261118-IODD1.1.xml")
	if err != nil {
		t.Fatal(err)
	}
	set, err := devbind.Resolve(fstest.MapFS{
		"profiles/sick-sig300.yaml":               {Data: prof},
		"iodd/acme-level100-20261118-IODD1.1.xml": {Data: level},
	}, []devbind.Device{{Name: "M", Profile: "sick-sig300", Endpoint: "h", Points: []string{"Port1_*"},
		Ports: map[int]devbind.Port{1: {Name: "LEVEL1", IODD: "acme-level100-20261118-IODD1.1.xml"}}}})
	if err != nil {
		t.Fatal(err)
	}
	od := set.OPCUADevices()[0]
	d, err := opcua.New("opc.tcp://127.0.0.1:1", od.Manifest, opcua.WithName("M"))
	if err != nil {
		t.Fatal(err)
	}
	w, err := set.WrapOPCUA("M", d, "None", "None")
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// The hook finds the master that has a port device among the live drivers,
// through any wrapper, lists its IODD's parameters with no traffic, and
// answers each failure as its kind: no such port device is unknown, a write
// before the master's ids have arrived is refused, a read with no session
// is the master's own failure (none of the three).
func TestParamHookFindsThePortDevice(t *testing.T) {
	m := portMaster(t)
	h := NewParamHook(func() []nio.NamedDriver {
		return []nio.NamedDriver{{Name: "other", Driver: nio.NewMemory()}, {Name: "M", Driver: wrapped{m}}}
	})
	list, ok := h.List("LEVEL1")
	if !ok || list.Master != "M" || list.Port != "LEVEL1" {
		t.Fatalf("list: %+v %v", list, ok)
	}
	found := false
	for _, p := range list.Params {
		if p.ID == "V_Setpoint" && p.Access == "rw" && len(p.Range) == 1 && p.Range[0] == "0..1000" && p.Carried {
			found = true
		}
	}
	if !found {
		t.Errorf("V_Setpoint is not in the listing: %+v", list.Params)
	}
	if _, ok := h.List("NOPE"); ok {
		t.Error("a port device no master has was listed")
	}
	ctx := context.Background()
	if _, err := h.Read(ctx, "NOPE", "V_Setpoint"); !errors.Is(err, server.ErrParamUnknown) {
		t.Errorf("read of an unknown port device: %v", err)
	}
	if _, err := h.Write(ctx, "NOPE", "V_Setpoint", int64(1)); !errors.Is(err, server.ErrParamUnknown) {
		t.Errorf("write to an unknown port device: %v", err)
	}
	if _, err := h.Write(ctx, "LEVEL1", "V_Setpoint", int64(500)); !errors.Is(err, server.ErrParamRefused) {
		t.Errorf("a write before the identity is known: %v, want a refusal", err)
	}
	_, err := h.Read(ctx, "LEVEL1", "V_Setpoint")
	if err == nil || errors.Is(err, server.ErrParamUnknown) || errors.Is(err, server.ErrParamInvalid) || errors.Is(err, server.ErrParamRefused) {
		t.Errorf("a read with no session: %v, want the master's own failure", err)
	}
	if _, err := h.Read(ctx, "LEVEL1", "V_Nothing"); !errors.Is(err, server.ErrParamUnknown) {
		t.Errorf("read of an unknown variable: %v", err)
	}
}

func TestParamErrorKinds(t *testing.T) {
	for kind, want := range map[error]error{
		devbind.ErrISDUUnknown:   server.ErrParamUnknown,
		devbind.ErrISDUInvalid:   server.ErrParamInvalid,
		devbind.ErrISDURefused:   server.ErrParamRefused,
		devbind.ErrISDUUncertain: server.ErrParamUncertain,
		devbind.ErrISDUBusy:      server.ErrParamBusy,
	} {
		err := paramError(fmt.Errorf("%w: x", kind))
		if !errors.Is(err, want) || !errors.Is(err, kind) {
			t.Errorf("%v became %v, want %v kept with its cause", kind, err, want)
		}
	}
	plain := errors.New("the master timed out")
	if got := paramError(plain); got != plain {
		t.Errorf("a master failure was changed: %v", got)
	}
	if paramError(nil) != nil {
		t.Error("nil became an error")
	}
}

// A project with no port devices has no hook, and the routes answer 404.
func TestParamHookIsNilWithoutPorts(t *testing.T) {
	if h := (&Project{}).ParamHook(); h != nil {
		t.Errorf("a project with no port devices has a hook: %+v", h)
	}
}
