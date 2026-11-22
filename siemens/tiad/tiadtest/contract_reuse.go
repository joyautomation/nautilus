package tiadtest

import (
	"reflect"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// contractReuse: an apply keeps the project's CPU when the spec names it
// (name, order, firmware exactly, a leading V aside), refuses any other
// CPU with the project's in the error, and keeps the devices besides it
// as found, listed again at the download (#789). A refusal leaves the
// session as it was.
func contractReuse(t *testing.T, start Starter) {
	a := start(t, Config{Targets: []string{benchCPU}})
	c := a.Client()
	installGSD(t, c)
	// The spec an adopted station starts from: the bench CPU, keeping its
	// protection, no IO system of its own.
	adopted := func() tiad.Station {
		st := BenchStation()
		st.Profinet = nil
		st.CPU.ConfigurationProtection = tiad.ProtectKeep
		st.CPU.AccessControl = tiad.AccessKeep
		return st
	}

	s, err := c.CreateProject(ctx, "reuse", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	up, err := s.UploadWith(ctx, tiad.UploadRequest{})
	if err != nil {
		t.Fatal(err)
	}
	dev := BenchDevice()
	want := tiad.ProjectCPU{Name: UploadedCPUName, Order: dev.Order, Firmware: dev.Firmware}
	if up.CPU != want {
		t.Fatalf("the upload's cpu = %+v, want %+v", up.CPU, want)
	}

	// Any other CPU refuses, naming what differs, with the project's CPU.
	mismatch := []struct {
		what string
		edit func(*tiad.CPU)
		says string
	}{
		{"name", func(c *tiad.CPU) { c.Name = "PLC_2" }, `name "PLC_1", not "PLC_2"`},
		{"order", func(c *tiad.CPU) { c.Order = "6ES7 510-1SK03-0AB0" }, `order "6ES7 510-1DK03-0AB0", not "6ES7 510-1SK03-0AB0"`},
		{"firmware", func(c *tiad.CPU) { c.Firmware = "V3.1.3" }, `firmware "V3.1", not "V3.1.3"`},
	}
	for _, m := range mismatch {
		st := adopted()
		m.edit(&st.CPU)
		_, err := s.ApplyStation(ctx, st, BenchSecrets)
		if tiad.Kind(err) != tiad.KindCPUMismatch || !strings.Contains(err.Error(), m.says) ||
			!strings.Contains(err.Error(), "create the project again (POST /v1/projects)") {
			t.Fatalf("%s: %v", m.what, err)
		}
		if got, ok := tiad.ProjectCPUOf(err); !ok || got != want {
			t.Fatalf("%s: the error's cpu = %+v %v, want %+v", m.what, got, ok, want)
		}
	}
	// A device of the spec's that shares a name or an address with a kept
	// one refuses: the two would coexist.
	clash := adopted()
	clash.Profinet = &tiad.Profinet{Devices: []tiad.Device{{GSDML: benchGSDML, DAP: "DAP_1", Name: "io-new", Address: UploadedIODevice().Address}}}
	if _, err := s.ApplyStation(ctx, clash, BenchSecrets); tiad.Kind(err) != tiad.KindCPUMismatch ||
		!strings.Contains(err.Error(), `"io-device-up" profinetName="io-device-up" address="192.0.2.30"`) {
		t.Fatalf("a device colliding with a kept one: %v", err)
	}

	// The CPU named as the project holds it is kept; so are the devices
	// besides it. The refusals above changed nothing.
	r, err := s.ApplyStation(ctx, adopted(), BenchSecrets)
	if err != nil {
		t.Fatalf("the uploaded CPU, named: %v", err)
	}
	kept := []tiad.KeptDevice{UploadedIODevice()}
	if !reflect.DeepEqual(r.Changes, []string{"cpu PLC_1 taken over from the project"}) || !reflect.DeepEqual(r.KeptDevices, kept) {
		t.Fatalf("taken over: %+v", r)
	}
	if len(r.Notes) != 1 || !strings.Contains(r.Notes[0], "kept as found") || !strings.Contains(r.Notes[0], `"io-device-up"`) {
		t.Fatalf("taken over, notes: %q", r.Notes)
	}
	// Firmware without its V is the same version.
	noV := adopted()
	noV.CPU.Firmware = strings.TrimPrefix(noV.CPU.Firmware, "V")
	if r, err := s.ApplyStation(ctx, noV, BenchSecrets); err != nil || !reflect.DeepEqual(r.KeptDevices, kept) {
		t.Fatalf("firmware without its V: %+v %v", r, err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatalf("the kept CPU compiles: %v", err)
	}

	// The download lists the kept devices before anything loads (a dry
	// run, a refusal for decisions, a declined question), and again when
	// it has loaded.
	// Loading them needs a yes that names exactly them.
	keptQ := func(qs []tiad.Question) bool {
		for _, q := range qs {
			if q.ID == tiad.QuestionKeptDevices && reflect.DeepEqual(q.Objects, []string{"io-device-up"}) {
				return true
			}
		}
		return false
	}
	// A dry run cancels at TIA's first check (a dry run TIA asks nothing
	// about cannot be cancelled).
	a.SetQuestions(t, []tiad.Question{{ID: tiad.QuestionStopCPU, Message: "Stop the CPU"}})
	dl := tiad.DownloadRequest{Address: benchCPU, Hardware: true, Software: true, Announced: true, DryRun: true}
	if res, err := s.Download(ctx, dl); err != nil || !res.DryRun || !reflect.DeepEqual(res.KeptDevices, kept) || !keptQ(res.Questions) {
		t.Fatalf("a dry run: %+v %v", res, err)
	}
	dl.DryRun = false
	dl = AcceptHardware(dl)
	dl.Decisions.StartAfter = tiad.No()
	_, err = s.Download(ctx, dl)
	if tiad.Kind(err) != tiad.KindDecisionRequired || !keptQ(tiad.Questions(err)) || !reflect.DeepEqual(tiad.KeptDevicesOf(err), kept) ||
		!strings.Contains(err.Error(), "name them in decisions.keptDevices") {
		t.Fatalf("a download without a yes for the kept devices: %v", err)
	}
	for _, stale := range [][]string{{"io-device-up", "io-device-2"}, {"io-device-2"}} {
		dl.Decisions.KeptDevices = stale
		if _, err := s.Download(ctx, dl); tiad.Kind(err) != tiad.KindDecisionRequired || !strings.Contains(err.Error(), "a yes for another set does not carry over") {
			t.Fatalf("a yes for %q: %v", stale, err)
		}
	}
	if len(a.Loaded(t)) != 0 {
		t.Fatalf("loaded without a matching yes: %+v", a.Loaded(t))
	}
	dl.Decisions = tiad.Decisions{AcceptUnverifiedHardware: tiad.Yes(), KeptDevices: []string{"io-device-up"}}
	_, err = s.Download(ctx, dl)
	if tiad.Kind(err) != tiad.KindDecisionRequired || !reflect.DeepEqual(tiad.KeptDevicesOf(err), kept) {
		t.Fatalf("a download needing decisions: %v, kept %+v", err, tiad.KeptDevicesOf(err))
	}
	a.SetQuestions(t, []tiad.Question{{ID: tiad.QuestionStopCPU, Message: "Stop the CPU"}})
	dl.Decisions = tiad.Decisions{AcceptUnverifiedHardware: tiad.Yes(), StopCPU: tiad.No(), StartAfter: tiad.No(), KeptDevices: []string{"io-device-up"}}
	_, err = s.Download(ctx, dl)
	if tiad.Kind(err) != tiad.KindDeclined || !reflect.DeepEqual(tiad.KeptDevicesOf(err), kept) {
		t.Fatalf("a declined download: %v, kept %+v", err, tiad.KeptDevicesOf(err))
	}
	a.SetQuestions(t, nil)
	dl.Decisions = tiad.Decisions{AcceptUnverifiedHardware: tiad.Yes(), StartAfter: tiad.No(), KeptDevices: []string{"io-device-up"}}
	res, err := s.Download(ctx, dl)
	if err != nil || !res.Downloaded || !reflect.DeepEqual(res.KeptDevices, kept) {
		t.Fatalf("the download: %+v %v", res, err)
	}

	// A created project keeps nothing besides its CPU, and its second
	// apply of the same spec reuses the CPU and changes nothing.
	closeFirst(t, s)
	n, err := c.CreateProject(ctx, "fresh", adopted(), BenchSecrets)
	if tiad.Kind(err) != tiad.KindBadRequest {
		t.Fatalf("keep on a new CPU still has nothing to keep: %v %v", n, err)
	}
	n, err = c.CreateProject(ctx, "fresh", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := n.ApplyStation(ctx, BenchStation(), BenchSecrets); err != nil || len(r.Changes) != 0 || len(r.KeptDevices) != 0 {
		t.Fatalf("the same spec on a created project: %+v %v", r, err)
	}
	other := BenchStation()
	other.CPU.Order = "6ES7 510-1SK03-0AB0"
	if _, err := n.ApplyStation(ctx, other, BenchSecrets); tiad.Kind(err) != tiad.KindCPUMismatch {
		t.Fatalf("another CPU on a created project is refused, not made anew: %v", err)
	}

	// A saved project, opened again, holds its CPU: the same spec takes it
	// over, another CPU refuses.
	path, err := n.Save(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Close(ctx); err != nil {
		t.Fatal(err)
	}
	o, err := c.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.ApplyStation(ctx, other, BenchSecrets); tiad.Kind(err) != tiad.KindCPUMismatch {
		t.Fatalf("another CPU on an opened project: %v", err)
	}
	if r, err := o.ApplyStation(ctx, BenchStation(), BenchSecrets); err != nil || !reflect.DeepEqual(r.Changes, []string{"cpu PLC_1 taken over from the project"}) {
		t.Fatalf("the saved CPU on an opened project: %+v %v", r, err)
	}
	if _, err := o.Compile(ctx, true, true); err != nil {
		t.Fatalf("the opened project, its CPU kept, compiles: %v", err)
	}
	if err := o.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

// contractReuseAfterARefusedUpload: an upload whose CPU is not the pinned
// one leaves a project nobody may take over or save; an upload of the
// pinned CPU cleans the session again.
func contractReuseAfterARefusedUpload(t *testing.T, start Starter) {
	a := start(t, Config{Targets: []string{benchCPU}})
	c := a.Client()
	installGSD(t, c)
	s, err := c.CreateProject(ctx, "refused", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Upload(ctx); err != nil {
		t.Fatal(err)
	}
	adopted := BenchStation()
	adopted.Profinet = nil
	adopted.CPU.ConfigurationProtection = tiad.ProtectKeep
	adopted.CPU.AccessControl = tiad.AccessKeep
	swapped := BenchDevice()
	swapped.Order = "6ES7 510-1SK03-0AB0"
	a.SetDevice(t, swapped, false)
	if err := s.Upload(ctx); tiad.Kind(err) != tiad.KindDeviceChanged {
		t.Fatalf("an upload from a swapped CPU: %v", err)
	}
	// The project now holds the swapped CPU: named exactly, it is still
	// not taken over, and the project is not saved.
	named := adopted
	named.CPU.Order = swapped.Order
	if _, err := s.ApplyStation(ctx, named, BenchSecrets); tiad.Kind(err) != tiad.KindUncleanSession {
		t.Fatalf("an apply after a refused upload: %v", err)
	}
	if _, err := s.Save(ctx, false); tiad.Kind(err) != tiad.KindUncleanSession {
		t.Fatalf("a save after a refused upload: %v", err)
	}
	a.SetDevice(t, BenchDevice(), false)
	if err := s.Upload(ctx); err != nil {
		t.Fatalf("an upload of the pinned CPU: %v", err)
	}
	if _, err := s.ApplyStation(ctx, adopted, BenchSecrets); err != nil {
		t.Fatalf("an apply after the pinned CPU's upload: %v", err)
	}
	if _, err := s.Save(ctx, false); err != nil {
		t.Fatalf("a save after the pinned CPU's upload: %v", err)
	}
}
