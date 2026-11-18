package project

import (
	"context"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/modbus/slave"
	"github.com/joyautomation/nautilus/runtime"
)

// acs355 is the built-in ACS355 + FMBT-21 profile, as `naut devices add`
// vendors it.
func acs355(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../devprofile/profiles/abb-acs355-fmbt21.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func devicesProject(t *testing.T, manifest, program string) fstest.MapFS {
	return fstest.MapFS{
		"nautilus.yaml":                   {Data: []byte(manifest)},
		"main.st":                         {Data: []byte(program)},
		"profiles/abb-acs355-fmbt21.yaml": {Data: acs355(t)},
		"profiles/abb-drives.st":          {Data: []byte("(* the simulator; not the controller's *)\n")},
	}
}

// A device declared by profile is one struct tag typed by the profile,
// whose members read back the drive, and a command tag per writable point
// that reaches the drive's register: end to end, through the real Modbus
// driver against a Modbus slave standing in for the drive, with a program
// that uses both.
func TestDeviceIsAStructTagWithCommandTags(t *testing.T) {
	srv := slave.NewServer("", nil)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	drive := srv.Unit(1)
	drive.SetHolding(3, 0x0237) // status word: READY TO SWITCH ON …
	drive.SetHolding(103, 125)  // 12.5 A

	fsys := devicesProject(t, `name: d
tasks: [{ program: main.st, scan: 20ms }]
drivers: [{ type: modbus, scan-rate: 20ms }]
devices:
  - { name: CONV, profile: abb-acs355-fmbt21, endpoint: "`+srv.Addr()+`", desc: Conveyor drive }
tags:
  - { name: Ready, role: state, type: BOOL }
  - { name: Amps,  role: state, type: REAL }
  - { name: Cmd,   role: setpoint, type: WORD, init: 0 }
`, `PROGRAM Main
Ready := CONV.StatusWord.0;
Amps := CONV.Current;
CONV_ControlWord := Cmd;
END_PROGRAM
`)
	proj, err := Load(fsys, "")
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]string{}
	roles := map[string]bool{}
	for _, td := range proj.Runtime.Tags {
		types[td.Name] = td.Type
		switch td.Role {
		case runtime.RoleInput:
			roles["in:"+td.Name] = true
		case runtime.RoleOutput:
			roles["out:"+td.Name] = true
		}
	}
	if types["CONV"] != "ABB_ACS355_FMBT21" || !roles["in:CONV"] {
		t.Errorf("CONV: type %q, input %v; want an input of ABB_ACS355_FMBT21", types["CONV"], roles["in:CONV"])
	}
	for _, cmd := range []string{"CONV_ControlWord", "CONV_Reference1", "CONV_Reference2"} {
		if !roles["out:"+cmd] {
			t.Errorf("%s is not an output", cmd)
		}
	}
	if _, ok := types["CONV_StatusWord"]; ok {
		t.Error("a read-only point got a command tag")
	}

	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatalf("the project does not compile against the device type: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	drv := proj.Runtime.Driver
	drv.(interface{ Start(context.Context) }).Start(ctx)
	go func() { rt.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
		drv.(interface{ Stop() }).Stop()
	})

	eventually(t, "the struct tag reads back the drive", func() bool {
		v, err := rt.Tags().ReadGlobal("CONV")
		if err != nil || v.Kind != ir.TypeStruct {
			return false
		}
		sw := v.Fld[v.Struct.FieldIndex["StatusWord"]]
		cur := v.Fld[v.Struct.FieldIndex["Current"]]
		return sw.I == 0x0237 && cur.F > 12.49 && cur.F < 12.51
	})
	eventually(t, "the program sees the members", func() bool {
		return rt.Tags().Bool("Ready") && rt.Tags().Real("Amps") > 12.4
	})
	// The command reaches the register. The first push is the driver's
	// start-up baseline (recorded, not sent), so the write is a change
	// made after it.
	for _, cw := range []uint16{0x0476, 0x047F} {
		if err := rt.Tags().SetPath("Cmd", int64(cw)); err != nil {
			t.Fatal(err)
		}
		eventually(t, "a command tag's write reaches the drive", func() bool { return drive.Holding(0) == cw })
	}
	eventually(t, "the read-back member shows what the drive holds", func() bool {
		v, _ := rt.Tags().ReadGlobal("CONV")
		return v.Kind == ir.TypeStruct && v.Fld[v.Struct.FieldIndex["ControlWord"]].I == int64(drive.Holding(0))
	})
}

// A project without a Modbus driver gets one for its devices; a row whose
// profile is not vendored says how to vendor it; a derived tag that
// collides with a declared one names both.
func TestDevicesLoadErrors(t *testing.T) {
	ok := devicesProject(t, `name: d
tasks: [{ program: main.st }]
devices: [{ name: CONV, profile: abb-acs355-fmbt21, endpoint: 10.0.0.9 }]
`, "PROGRAM Main\nEND_PROGRAM\n")
	proj, err := Load(ok, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := proj.Drivers(); len(got) != 1 || got[0].Name != "modbus" {
		t.Errorf("drivers %+v, want the one modbus driver added for the devices", got)
	}

	for name, tc := range map[string]struct{ manifest, want string }{
		"not vendored": {`devices: [{ name: CONV, profile: abb-acs580-fmbt21, endpoint: 10.0.0.9 }]`, "naut devices add abb-acs580-fmbt21"},
		"collision": {"devices: [{ name: CONV, profile: abb-acs355-fmbt21, endpoint: 10.0.0.9 }]\ntags: [{ name: CONV_ControlWord, role: output, type: WORD }]",
			`tag "CONV_ControlWord" is declared in both devices: and nautilus.yaml`},
		"two drivers": {"drivers: [{ type: modbus, name: a }, { type: modbus, name: b }]\ndevices: [{ name: CONV, profile: abb-acs355-fmbt21, endpoint: 10.0.0.9 }]",
			"more than one modbus driver"},
		"point typo":        {`devices: [{ name: CONV, profile: abb-acs355-fmbt21, endpoint: 10.0.0.9, points: [StatusWrod] }]`, `"StatusWrod" matches no point`},
		"not an identifier": {`devices: [{ name: 1CONV, profile: abb-acs355-fmbt21, endpoint: 10.0.0.9 }]`, "not an IEC identifier"},
	} {
		fsys := devicesProject(t, "name: d\ntasks: [{ program: main.st }]\n"+tc.manifest+"\n", "PROGRAM Main\nEND_PROGRAM\n")
		_, err := Load(fsys, "")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
