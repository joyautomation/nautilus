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
