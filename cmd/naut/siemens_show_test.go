package main

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// show runs naut siemens show with args, through the real flag parser.
func (r *deployRig) show(args ...string) (string, int) {
	r.t.Helper()
	var out bytes.Buffer
	o := siemensShowOptions{stdout: &out, stderr: &out, getenv: func(k string) string { return r.env()[k] }}
	if err := parseSiemensShowFlags(&o, append(append([]string{"--agent", r.url}, args...), r.dir), &out); err != nil {
		r.t.Fatalf("flags %v: %v\n%s", args, err, out.String())
	}
	code := siemensShow(context.Background(), o)
	return out.String(), code
}

func (r *deployRig) sessions() []tiad.Session {
	r.t.Helper()
	ss, err := tiad.New(r.url, r.token).Sessions(context.Background())
	if err != nil {
		r.t.Fatal(err)
	}
	return ss
}

// TestSiemensShowNeedsTheAgentsWindow: an agent without TIA's window is
// refused before anything opens, naming the fix.
func TestSiemensShowNeedsTheAgentsWindow(t *testing.T) {
	r := newDeployRig(t)
	r.deployed()
	out, code := r.show("naut_main")
	if code != 1 || !strings.Contains(out, "install-tiad.ps1 -Ui") || !strings.Contains(out, "Nothing was opened") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if ss := r.sessions(); len(ss) != 0 || len(r.fake.Shown()) != 0 {
		t.Fatalf("sessions %+v, shown %v: want none", ss, r.fake.Shown())
	}
}

// TestSiemensShowOpensTheDeployedProject: the saved project opens and the
// block shows; a second show reuses the open project; verify is busy
// until --close, then compares.
func TestSiemensShowOpensTheDeployedProject(t *testing.T) {
	r := newDeployRig(t)
	r.deployed()
	r.fake.UI = true
	out, code := r.show("NAUT_MAIN")
	if code != 0 || !strings.Contains(out, "opened projects/s7_demo/s7_demo.ap21 in TIA Portal") ||
		!strings.Contains(out, "TIA Portal's editor shows naut_main [FB1], in group nautilus") || !strings.Contains(out, "naut siemens show --close") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	out, code = r.show("Main")
	if code != 0 || strings.Contains(out, "opened ") || !strings.Contains(out, "Main [OB1], in the program blocks' root") || len(r.sessions()) != 1 {
		t.Fatalf("second show: exit %d, sessions %d:\n%s", code, len(r.sessions()), out)
	}
	if got, want := r.fake.Shown(), []string{"nautilus/naut_main", "Main"}; !slices.Equal(got, want) {
		t.Fatalf("shown %v, want %v", got, want)
	}
	if out, code := r.verify(nil); code != 1 || !strings.Contains(out, "close it first") {
		t.Fatalf("verify while shown: exit %d:\n%s", code, out)
	}
	if out, code := r.show("--close"); code != 0 || !strings.Contains(out, "closed projects/s7_demo/s7_demo.ap21") || len(r.sessions()) != 0 {
		t.Fatalf("--close: exit %d:\n%s", code, out)
	}
	if out, code := r.show("--close"); code != 0 || !strings.Contains(out, "nothing to close") {
		t.Fatalf("--close again: exit %d:\n%s", code, out)
	}
	if out, code := r.verify(nil); code != 0 {
		t.Fatalf("verify after --close: exit %d:\n%s", code, out)
	}
}

// TestSiemensShowRefusals: a block the project lacks (the session show
// opened is closed again), no saved project, and another project open.
func TestSiemensShowRefusals(t *testing.T) {
	r := newDeployRig(t)
	r.fake.UI = true
	out, code := r.show("naut_main")
	if code != 1 || !strings.Contains(out, "no saved project projects/s7_demo/s7_demo.ap21") || !strings.Contains(out, "deploy first") {
		t.Fatalf("no saved project: exit %d:\n%s", code, out)
	}
	r.fake.UI = false
	r.deployed()
	r.fake.UI = true
	out, code = r.show("naut_nope")
	if code != 1 || !strings.Contains(out, `no program block "naut_nope"`) || len(r.sessions()) != 0 {
		t.Fatalf("unknown block: exit %d, sessions %d:\n%s", code, len(r.sessions()), out)
	}
	s, err := tiad.New(r.url, r.token).Open(context.Background(), "projects/s7_demo/s7_demo.ap21")
	if err != nil {
		t.Fatal(err)
	}
	// The same project open by another call is reused, but a block it
	// lacks never closes a session show did not open.
	if out, code := r.show("naut_nope"); code != 1 || len(r.sessions()) != 1 {
		t.Fatalf("unknown block on a reused session: exit %d, sessions %d:\n%s", code, len(r.sessions()), out)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	other, err := tiad.New(r.url, r.token).CreateProject(context.Background(), "other", r.station(), tiad.Secrets{ConfigurationPassword: r.env()["NAUTILUS_SIEMENS_CONFIG_PASSWORD"], FullAccessPassword: r.env()["NAUTILUS_SIEMENS_FULL_ACCESS_PASSWORD"]})
	if err != nil {
		t.Fatal(err)
	}
	out, code = r.show("naut_main")
	if code != 1 || !strings.Contains(out, "tiad holds one project at a time") || !strings.Contains(out, other.ID) || len(r.sessions()) != 1 {
		t.Fatalf("another project open: exit %d:\n%s", code, out)
	}
}

// TestSiemensShowOnline: --online holds the deployed project online in
// TIA's window before the show; --close takes it offline, then closes it.
func TestSiemensShowOnline(t *testing.T) {
	r := newDeployRig(t)
	r.deployed()
	r.fake.UI = true
	out, code := r.show("--online", "naut_main")
	if code != 0 || !strings.Contains(out, "TIA Portal is online to the CPU at "+deployCPU+" (Online)") ||
		!strings.Contains(out, "naut_main [FB1]") || !strings.Contains(out, "--close takes it offline first") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := r.fake.OnlineEvents(); !slices.Equal(got, []string{"online " + deployCPU}) {
		t.Fatalf("online events %v", got)
	}
	// Again on the reused session: still one connection.
	if out, code := r.show("--online", "naut_Tags"); code != 0 || len(r.fake.OnlineEvents()) != 1 {
		t.Fatalf("second --online: exit %d, events %v:\n%s", code, r.fake.OnlineEvents(), out)
	}
	out, code = r.show("--close")
	if code != 0 || !strings.Contains(out, "took projects/s7_demo/s7_demo.ap21 offline from "+deployCPU) || len(r.sessions()) != 0 {
		t.Fatalf("--close: exit %d:\n%s", code, out)
	}
	if got := r.fake.OnlineEvents(); !slices.Equal(got, []string{"online " + deployCPU, "offline " + deployCPU}) {
		t.Fatalf("online events %v", got)
	}
}

// TestSiemensShowOnlineRefused: a go-online the agent refuses closes the
// session the show opened, and shows nothing.
func TestSiemensShowOnlineRefused(t *testing.T) {
	r := newDeployRig(t)
	r.deployed()
	r.fake.UI = true
	r.fake.Targets = []string{"192.0.2.99"}
	out, code := r.show("--online", "naut_main")
	// One refusal, and nothing after it: the show is never tried.
	if code != 1 || !strings.Contains(out, "going online:") || !strings.Contains(out, "TIAD_TARGETS") || strings.Count(out, "naut siemens show:") != 1 ||
		len(r.sessions()) != 0 || len(r.fake.Shown()) != 0 {
		t.Fatalf("exit %d, sessions %d, shown %v:\n%s", code, len(r.sessions()), r.fake.Shown(), out)
	}
}
