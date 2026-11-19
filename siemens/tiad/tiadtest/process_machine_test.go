package tiadtest_test

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/siemens/tiad"
	"github.com/joyautomation/nautilus/siemens/tiad/tiadtest"
)

// These tests run the agent's real gate evaluator (tools/tiad/core/
// Machine.cs) over a machine tiad-fake scripts, so every branch of the
// gates the Windows host computes is checked here, under Mono, without a
// TIA machine.

const (
	opennessKey  = `SOFTWARE\Siemens\Automation\Openness\21.0`
	publicAPIKey = opennessKey + `\PublicAPI\21.0.0.0\net48`
	apiDir       = `C:\Program Files\Siemens\Automation\Portal V21\PublicAPI\V21\net48`
	exePath      = `C:\Program Files\tiad\tiad.exe`
	allowKey     = `Software\Siemens\Automation\Openness\AllowList\tiad.exe`
)

var exeHash = func() []byte { h := sha256.Sum256([]byte("tiad.exe")); return h[:] }()

// exeTime has a fraction below the millisecond, as an NTFS time has; the
// allowlist holds it truncated to the millisecond.
var exeTime = time.Date(2026, 11, 18, 12, 30, 45, 123456700, time.UTC)

type entry struct{ name, path, date, hash string }

func goodEntry() entry {
	return entry{"Entry", exePath, "2026/11/18 12:30:45.123", base64.StdEncoding.EncodeToString(exeHash)}
}

// machine is a TIA machine where every gate the host computes holds,
// with the given allowlist entries.
func machine(entries ...entry) tiadtest.Machine {
	m := tiadtest.Machine{
		Registry: []tiadtest.RegValue{
			{Key: opennessKey, Name: "PortalVersion", Value: "V21"},
			{Key: publicAPIKey, Name: "Siemens.Engineering.Base", Value: apiDir + `\Siemens.Engineering.Base.dll`},
			{Key: publicAPIKey, Name: "AssemblyVersion", Value: "21.0.0.0"},
		},
		Files:       []string{apiDir + `\Siemens.Engineering.Base.dll`, apiDir + `\Siemens.Engineering.Step7.dll`},
		ExePath:     exePath,
		ExeModified: exeTime,
		ExeSHA256:   exeHash,
		Group:       "member",
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.name)
		sub := allowKey + `\` + e.name
		if e.path != "" {
			m.Registry = append(m.Registry, tiadtest.RegValue{Key: sub, Name: "Path", Value: e.path})
		}
		if e.date != "" {
			m.Registry = append(m.Registry, tiadtest.RegValue{Key: sub, Name: "DateModified", Value: e.date})
		}
		if e.hash != "" {
			m.Registry = append(m.Registry, tiadtest.RegValue{Key: sub, Name: "FileHash", Value: e.hash})
		}
	}
	if entries != nil {
		m.SubKeys = []tiadtest.RegKey{{Key: allowKey, Names: names}}
	}
	return m
}

func gate(t *testing.T, p tiad.Probe, name string) tiad.Gate {
	t.Helper()
	for _, g := range p.Gates {
		if g.Name == name {
			return g
		}
	}
	t.Fatalf("probe has no gate %s: %+v", name, p)
	return tiad.Gate{}
}

// TestMachineGates: each fact the host reads, wrong in one way, fails
// exactly its own gate, and says why.
func TestMachineGates(t *testing.T) {
	p := tiadtest.StartProcess(t, fakeBin(t), tiadtest.Config{})
	c := p.Client()

	without := func(m tiadtest.Machine, key, name string) tiadtest.Machine {
		var r []tiadtest.RegValue
		for _, v := range m.Registry {
			if v.Key != key || v.Name != name {
				r = append(r, v)
			}
		}
		m.Registry = r
		return m
	}
	set := func(m tiadtest.Machine, key, name, value string) tiadtest.Machine {
		m = without(m, key, name)
		m.Registry = append(m.Registry, tiadtest.RegValue{Key: key, Name: name, Value: value})
		return m
	}
	good := goodEntry()
	stale := good
	stale.date = "2026/11/18 12:30:45.124"
	rehashed := good
	rehashed.hash = base64.StdEncoding.EncodeToString(make([]byte, 32))

	cases := []struct {
		name   string
		m      tiadtest.Machine
		gate   string // the gate that fails; "" means none does
		detail string // in the failing gate's detail
	}{
		{"all hold", machine(good), "", ""},
		{"path case differs", machine(entry{"Entry", strings.ToUpper(exePath), good.date, good.hash}), "", ""},
		{"stale entry, then a good one", machine(entry{"Old", exePath, "2026/01/01 00:00:00.000", good.hash}, good), "", ""},
		{"TIAD_NO_TIA", func() tiadtest.Machine { m := machine(good); m.NoTIA = true; return m }(), "tia", "TIAD_NO_TIA=1"},
		{"another TIA version", set(machine(good), opennessKey, "PortalVersion", "V20"), "tia", `"V20"`},
		{"no TIA registration", without(machine(good), opennessKey, "PortalVersion"), "tia", "absent"},
		{"no public API registration", without(machine(good), publicAPIKey, "Siemens.Engineering.Base"), "tia", "public API"},
		{"public API assembly missing", func() tiadtest.Machine { m := machine(good); m.Files = m.Files[1:]; return m }(), "tia", "Base.dll"},
		{"STEP 7 assembly missing", func() tiadtest.Machine { m := machine(good); m.Files = m.Files[:1]; return m }(), "tia", "Step7.dll"},
		{"group not in the token", func() tiadtest.Machine { m := machine(good); m.Group = "not-in-token"; return m }(), "openness-group", "token does not hold"},
		{"no group", func() tiadtest.Machine { m := machine(good); m.Group = "missing"; return m }(), "openness-group", "no \"Siemens TIA Openness\" group"},
		{"no allowlist key", machine(), "openness-allowlist", "no allowlist entry for tiad.exe"},
		{"allowlist key without entries", machine([]entry{}...), "openness-allowlist", "no allowlist entry"},
		{"entry for another directory", machine(entry{"Entry", `C:\elsewhere\tiad.exe`, good.date, good.hash}), "openness-allowlist", "no entry names"},
		{"entry without a path", machine(entry{"Entry", "", good.date, good.hash}), "openness-allowlist", "no entry names"},
		{"entry a millisecond off", machine(stale), "openness-allowlist", "DateModified"},
		{"entry date in another format", machine(entry{"Entry", exePath, "2026-11-18 12:30:45.123", good.hash}), "openness-allowlist", "DateModified"},
		{"entry for another build", machine(rehashed), "openness-allowlist", "FileHash"},
		{"entry hash not base64", machine(entry{"Entry", exePath, good.date, "%%%"}), "openness-allowlist", "FileHash"},
		{"entry without a hash", machine(entry{"Entry", exePath, good.date, ""}), "openness-allowlist", "FileHash"},
		{"copied exe", func() tiadtest.Machine { m := machine(good); m.ExePath = `C:\Temp\copy\tiad.exe`; return m }(), "openness-allowlist", "no entry names"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p.SetMachine(t, tc.m)
			pr, err := c.Probe(ctx)
			if err != nil {
				t.Fatal(err)
			}
			tiadtest.CheckGateShape(t, pr)
			if l := gate(t, pr, "licence"); l.State != tiad.GateUnknown || l.Detail != "not readable until TIA's first start" {
				t.Errorf("licence = %+v, want unknown until TIA's first start", l)
			}
			for _, g := range pr.Gates {
				if g.Name == "licence" {
					continue
				}
				if g.Name == tc.gate {
					if g.State != tiad.GateFailed || !strings.Contains(g.Detail, tc.detail) || g.Remedy == "" {
						t.Errorf("gate %s = %+v, want failed with %q and a remedy", g.Name, g, tc.detail)
					}
				} else if g.State != tiad.GateOK {
					t.Errorf("gate %s = %+v, want ok", g.Name, g)
				}
			}
			if pr.Usable != (tc.gate == "") {
				t.Errorf("usable = %v, want %v", pr.Usable, tc.gate == "")
			}
		})
	}
}

// TestMachineVersions: health reports the TIA and Openness versions only
// for an installed Openness V21.
func TestMachineVersions(t *testing.T) {
	p := tiadtest.StartProcess(t, fakeBin(t), tiadtest.Config{})
	c := p.Client()
	p.SetMachine(t, machine(goodEntry()))
	h, err := c.Health(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if h.TIA != "V21" || h.Openness != "21.0.0.0" {
		t.Fatalf("health = %+v, want V21 and 21.0.0.0", h)
	}
	m := machine(goodEntry())
	m.Files = nil
	p.SetMachine(t, m)
	if h, err = c.Health(ctx); err != nil {
		t.Fatal(err)
	}
	if h.TIA != "" || h.Openness != "" {
		t.Fatalf("health without the assemblies = %+v, want no versions", h)
	}
}

// TestNoTIAStopsEveryTIACall: TIAD_NO_TIA fails the tia gate, so a TIA
// call answers gate before TIA could start.
func TestNoTIAStopsEveryTIACall(t *testing.T) {
	p := tiadtest.StartProcess(t, fakeBin(t), tiadtest.Config{})
	c := p.Client()
	m := machine(goodEntry())
	m.NoTIA = true
	p.SetMachine(t, m)
	const g = "GSDML-V2.4-Example-Device-20261117.xml"
	if _, err := c.PutFile(ctx, "gsd/"+g, []byte("<ISO15745Profile/>")); err != nil {
		t.Fatal(err)
	}
	_, err := c.InstallGSD(ctx, "gsd/"+g)
	var te *tiad.Error
	if !errors.As(err, &te) || te.Kind != tiad.KindGate || !strings.Contains(te.Message, "gate tia") {
		t.Fatalf("install with TIAD_NO_TIA: %v, want 412 gate tia", err)
	}
	if cs := p.Calls(t); len(cs) != 0 {
		t.Fatalf("TIA was reached: %s", callNames(cs))
	}
}

// TestLicenceLatch: the licence gate is unknown, and blocks nothing,
// until the TIA side starts. Then it latches: ok once TIA started, failed
// when the start failed on a licence error, after which every TIA call
// answers gate without starting TIA again. A start that failed for any
// other reason says nothing about the licence.
func TestLicenceLatch(t *testing.T) {
	const g = "GSDML-V2.4-Example-Device-20261117.xml"
	start := func(t *testing.T, failure string) (*tiadtest.Process, *tiad.Client) {
		p := tiadtest.StartProcess(t, fakeBin(t), tiadtest.Config{})
		c := p.Client()
		p.SetMachine(t, machine(goodEntry()))
		if failure != "" {
			p.SetScript(t, tiadtest.Script{Failures: []tiadtest.ScriptedCall{{Call: "start", Message: failure}}})
		}
		if _, err := c.PutFile(ctx, "gsd/"+g, []byte("<ISO15745Profile/>")); err != nil {
			t.Fatal(err)
		}
		return p, c
	}
	licence := func(t *testing.T, c *tiad.Client) (tiad.Gate, bool) {
		t.Helper()
		pr, err := c.Probe(ctx)
		if err != nil {
			t.Fatal(err)
		}
		tiadtest.CheckGateShape(t, pr)
		return gate(t, pr, "licence"), pr.Usable
	}
	install := func(c *tiad.Client) error { _, err := c.InstallGSD(ctx, "gsd/"+g); return err }

	t.Run("started", func(t *testing.T) {
		_, c := start(t, "")
		if l, usable := licence(t, c); l.State != tiad.GateUnknown || !usable {
			t.Fatalf("before TIA starts: licence %+v, usable %v; want unknown and usable", l, usable)
		}
		if err := install(c); err != nil {
			t.Fatal(err)
		}
		l, usable := licence(t, c)
		if l.State != tiad.GateOK || !l.OK || !strings.Contains(l.Detail, "TIA started") || l.Remedy != "" || !usable {
			t.Fatalf("after TIA started: licence %+v, usable %v; want ok", l, usable)
		}
	})

	t.Run("licence error", func(t *testing.T) {
		p, c := start(t, "No valid license for STEP 7 Professional")
		err := install(c)
		if tiad.Kind(err) != tiad.KindOperationFailed || !strings.Contains(err.Error(), "No valid license") {
			t.Fatalf("a start without a licence: %v, want operation_failed with TIA's text", err)
		}
		l, usable := licence(t, c)
		if l.State != tiad.GateFailed || l.Detail != "TIA did not start: No valid license for STEP 7 Professional" || l.Remedy == "" || usable {
			t.Fatalf("after a licence error: licence %+v, usable %v; want failed with TIA's text and a remedy", l, usable)
		}
		// Latched: even once TIA would start, nothing tries until the
		// agent restarts.
		p.SetScript(t, tiadtest.Script{})
		var te *tiad.Error
		if err := install(c); !errors.As(err, &te) || te.Kind != tiad.KindGate || !strings.Contains(te.Message, "gate licence") {
			t.Fatalf("a TIA call after a licence error: %v, want 412 gate licence", err)
		}
		if cs := callNames(p.Calls(t)); cs != "start" {
			t.Fatalf("TIA calls = %q, want the one failed start", cs)
		}
	})

	// TIA starts headless without a licence and refuses the first call
	// that needs one: the gate fails then, not at the start.
	t.Run("licence error after the start", func(t *testing.T) {
		p, c := start(t, "")
		if err := install(c); err != nil {
			t.Fatal(err)
		}
		const refused = "Error when calling method 'CreateWithItem'. Necessary license 'STEP 7 Professional' is missing."
		p.SetScript(t, tiadtest.Script{Failures: []tiadtest.ScriptedCall{{Call: "CreateProject", Message: refused}}})
		if _, err := c.CreateProject(ctx, "p", tiad.Station{CPU: tiad.CPU{Name: "PLC_1", Order: "6ES7 510-1DK03-0AB0", Firmware: "V3.1",
			Address: "192.0.2.10", SubnetMask: "255.255.255.0", ProfinetName: "plc-1"}}, tiad.Secrets{}); tiad.Kind(err) != tiad.KindOperationFailed {
			t.Fatalf("a call TIA refuses for its licence: %v, want operation_failed", err)
		}
		l, usable := licence(t, c)
		if l.State != tiad.GateFailed || l.Detail != "TIA refused a call: "+refused || l.Remedy == "" || usable {
			t.Fatalf("after a licence refusal: licence %+v, usable %v; want failed with TIA's text", l, usable)
		}
		p.SetScript(t, tiadtest.Script{})
		var te *tiad.Error
		if err := install(c); !errors.As(err, &te) || te.Kind != tiad.KindGate || !strings.Contains(te.Message, "gate licence") {
			t.Fatalf("a TIA call after a licence refusal: %v, want 412 gate licence", err)
		}
	})

	t.Run("other error", func(t *testing.T) {
		p, c := start(t, "the TIA Portal process ended")
		if err := install(c); tiad.Kind(err) != tiad.KindOperationFailed {
			t.Fatalf("a failed start: %v, want operation_failed", err)
		}
		if l, usable := licence(t, c); l.State != tiad.GateUnknown || l.Detail != "not readable until TIA's first start" || !usable {
			t.Fatalf("after another start failure: licence %+v, usable %v; want unknown and usable", l, usable)
		}
		p.SetScript(t, tiadtest.Script{})
		if err := install(c); err != nil {
			t.Fatalf("a TIA call once TIA starts: %v", err)
		}
		// A later failure that is no licence error leaves the gate ok.
		p.SetScript(t, tiadtest.Script{Failures: []tiadtest.ScriptedCall{{Call: "InstallGsd", Message: "the catalogue is busy"}}})
		if err := install(c); tiad.Kind(err) != tiad.KindOperationFailed {
			t.Fatalf("a failing call: %v", err)
		}
		if l, _ := licence(t, c); l.State != tiad.GateOK {
			t.Fatalf("after TIA started: licence %+v, want ok", l)
		}
	})
}
