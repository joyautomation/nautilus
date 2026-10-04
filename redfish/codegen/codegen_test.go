package codegen

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/redfish"
	"github.com/joyautomation/nautilus/redfish/mockup"
)

var fixtures = []struct {
	dir, generation string
	tags            []string
}{
	{"supermicro-x14", "ThermalSubsystem/PowerSubsystem", []string{
		"NODE1", "NODE1_Fan1", "NODE1_Fan2", "NODE1_Fan3", "NODE1_Fan4", "NODE1_Fan5", "NODE1_Fan6", "NODE1_PSU1", "NODE1_PSU2",
		"NODE1_Temp_CPU", "NODE1_Temp_Inlet", "NODE1_Temp_System", "NODE1_Temp_Peripheral", "NODE1_Temp_VRM_CPU", "NODE1_Temp_DIMM", "NODE1_Temp_NVMe_SSD"}},
	{"legacy-1u", "Thermal/Power", []string{
		"NODE1", "NODE1_Fan1", "NODE1_Fan2", "NODE1_Fan3", "NODE1_Fan4", "NODE1_PSU1", "NODE1_PSU2",
		"NODE1_Temp_CPU1", "NODE1_Temp_Inlet", "NODE1_Temp_Exhaust", "NODE1_Temp_PCH"}},
	{"subsystem-1u", "ThermalSubsystem/PowerSubsystem", []string{
		"NODE1", "NODE1_Fan1", "NODE1_Fan2", "NODE1_Fan3", "NODE1_Fan4", "NODE1_Fan5", "NODE1_Fan6", "NODE1_PSU1", "NODE1_PSU2",
		"NODE1_Temp_CPU1", "NODE1_Temp_Inlet", "NODE1_Temp_Exhaust"}},
}

func opts() Options {
	return Options{Tag: "NODE1", Host: "https://bmc1", User: "admin", PasswordEnv: "NODE1_BMC_PASSWORD", Insecure: true}
}

func load(t *testing.T, dir string) mockup.Tree {
	t.Helper()
	tree, err := mockup.LoadDir(filepath.Join("..", "testdata", dir))
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func importTree(t *testing.T, g Getter) (Output, []byte) {
	t.Helper()
	out, err := Import(context.Background(), g, opts())
	if err != nil {
		t.Fatal(err)
	}
	return out, ManifestYAML(out.Manifest, "naut redfish import --tag NODE1")
}

func tagNames(m redfish.Manifest) []string {
	var out []string
	for _, t := range m.Tags {
		out = append(out, t.Name)
	}
	return out
}

func TestImportFixtures(t *testing.T) {
	for _, fx := range fixtures {
		t.Run(fx.dir, func(t *testing.T) {
			out, body := importTree(t, TreeGetter(load(t, fx.dir)))
			if out.Generation != fx.generation {
				t.Errorf("generation = %s", out.Generation)
			}
			if got := strings.Join(tagNames(out.Manifest), ","); got != strings.Join(fx.tags, ",") {
				t.Errorf("tags =\n%s\nwant\n%s", got, strings.Join(fx.tags, ","))
			}
			// The rendered file decodes through the driver's strict loader
			// to the same manifest, and renders to the same bytes again.
			m, err := redfish.ParseManifest(body)
			if err != nil {
				t.Fatalf("generated manifest does not load: %v\n%s", err, body)
			}
			if err := m.Validate(); err != nil {
				t.Fatal(err)
			}
			if again := ManifestYAML(m, "naut redfish import --tag NODE1"); string(again) != string(body) {
				t.Errorf("render → parse → render drifted:\n%s", again)
			}
			if _, err := redfish.New(m); err != nil {
				t.Fatalf("driver refuses the generated manifest: %v", err)
			}
			// No binding is writable: import never generates a command.
			if len(m.Writes) != 0 || strings.Contains(string(body), "\nwrites:") {
				t.Error("import generated a write")
			}
		})
	}
}

// Every binding the importer writes resolves on the device it came from:
// served by the stand-in, polled by the real driver, nothing is absent
// and nothing is Bad.
func TestGeneratedBindingsResolve(t *testing.T) {
	for _, fx := range fixtures {
		t.Run(fx.dir, func(t *testing.T) {
			tree := load(t, fx.dir)
			srv := mockup.New(tree, mockup.Options{})
			if err := srv.Start("127.0.0.1:0"); err != nil {
				t.Fatal(err)
			}
			defer srv.Stop()
			out, _ := importTree(t, TreeGetter(tree))
			m := out.Manifest
			m.Sources[0].Host, m.Sources[0].User, m.Sources[0].PasswordEnv, m.Sources[0].TLS = srv.URL(), "", "", redfish.TLS{}
			m.Sources[0].Interval = 30 * time.Millisecond
			d, err := redfish.New(m, redfish.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			d.Start(ctx)
			defer d.Stop()
			deadline := time.Now().Add(5 * time.Second)
			for {
				v, _ := d.ReadInputs()
				if v["NODE1__Online"] == true && len(d.Quality()) == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("never fully delivered: quality %v", d.Quality())
				}
				time.Sleep(10 * time.Millisecond)
			}
			if a := d.Absent(); len(a) != 0 {
				t.Fatalf("generated bindings that do not resolve: %v", a)
			}
		})
	}
}

// A live import (through the driver's own client, from the stand-in
// serving the recording) gives the recording's import byte for byte; and
// a --record of that service imports to the same bytes again.
func TestLiveImportMatchesRecording(t *testing.T) {
	for _, fx := range fixtures {
		t.Run(fx.dir, func(t *testing.T) {
			tree := load(t, fx.dir)
			_, offline := importTree(t, TreeGetter(tree))

			t.Setenv("RF_CODEGEN_PASSWORD", "pw")
			srv := mockup.New(tree, mockup.Options{Auth: mockup.AuthSession, User: "admin", Password: "pw"})
			if err := srv.Start("127.0.0.1:0"); err != nil {
				t.Fatal(err)
			}
			defer srv.Stop()
			cl, err := redfish.NewClient(redfish.Source{ID: "X", Host: srv.URL(), User: "admin", PasswordEnv: "RF_CODEGEN_PASSWORD"})
			if err != nil {
				t.Fatal(err)
			}
			defer cl.Close(context.Background())
			_, live := importTree(t, FetcherGetter(cl))
			if string(live) != string(offline) {
				t.Fatalf("live import differs from the recording's:\n%s", live)
			}

			rec, notes, err := Record(context.Background(), cl, RecordOptions{})
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			if err := rec.WriteDir(dir); err != nil {
				t.Fatal(err)
			}
			if err := rec.WriteDir(dir); err == nil {
				t.Fatal("recording over a recording must be refused")
			}
			back, err := mockup.LoadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			_, again := importTree(t, TreeGetter(back))
			if string(again) != string(offline) {
				t.Fatalf("import of a fresh recording differs (notes %v):\n%s", notes, again)
			}
			// The recording carries no session — ours included.
			sess, _ := back.Get(redfish.SessionsPath)
			if n, _ := sess["Members"].([]any); len(n) != 0 {
				t.Fatalf("recorded sessions: %v", sess["Members"])
			}
		})
	}
}

func TestSanitiseAndNaming(t *testing.T) {
	for in, want := range map[string]string{
		"CPU Temp":                       "CPU",
		"CPU #1 Temperature":             "CPU_1",
		"Front Panel Intake Temperature": "Front_Panel_Intake",
		"DIMMA~D Temp":                   "DIMMA_D",
		"VRM CPU Temp":                   "VRM_CPU",
		"Temp":                           "Temp",
		"  ":                             "Sensor",
		"PCH_Temp":                       "PCH",
	} {
		if got := Sanitise(in); got != want {
			t.Errorf("Sanitise(%q) = %q, want %q", in, got, want)
		}
	}
	if childName("SW1", "Port", 1, 28) != "SW1_Port01" || childName("N", "Fan", 6, 6) != "N_Fan6" || childName("N", "Fan", 3, 100) != "N_Fan003" {
		t.Error("childName padding")
	}
}

// tree builds a minimal new-form service with the given sensors.
func tree(sensors map[string]string) mockup.Tree {
	t := mockup.Tree{
		"/redfish/v1":                            json.RawMessage(`{"Systems":{"@odata.id":"/redfish/v1/Systems"},"Chassis":{"@odata.id":"/redfish/v1/Chassis"}}`),
		"/redfish/v1/Systems":                    json.RawMessage(`{"Members":[{"@odata.id":"/redfish/v1/Systems/A"},{"@odata.id":"/redfish/v1/Systems/B"}]}`),
		"/redfish/v1/Systems/A":                  json.RawMessage(`{"Id":"A","PowerState":"On","Links":{"Chassis":[{"@odata.id":"/redfish/v1/Chassis/1"}]}}`),
		"/redfish/v1/Systems/B":                  json.RawMessage(`{"Id":"B","PowerState":"Off","Links":{"Chassis":[{"@odata.id":"/redfish/v1/Chassis/1"}]}}`),
		"/redfish/v1/Chassis":                    json.RawMessage(`{"Members":[{"@odata.id":"/redfish/v1/Chassis/1"}]}`),
		"/redfish/v1/Chassis/1":                  json.RawMessage(`{"ThermalSubsystem":{"@odata.id":"/redfish/v1/Chassis/1/ThermalSubsystem"},"Sensors":{"@odata.id":"/redfish/v1/Chassis/1/Sensors"}}`),
		"/redfish/v1/Chassis/1/ThermalSubsystem": json.RawMessage(`{}`),
	}
	var members []string
	for id, body := range sensors {
		members = append(members, `{"@odata.id":"/redfish/v1/Chassis/1/Sensors/`+id+`"}`)
		t["/redfish/v1/Chassis/1/Sensors/"+id] = json.RawMessage(body)
	}
	t["/redfish/v1/Chassis/1/Sensors"] = json.RawMessage(`{"Members":[` + strings.Join(members, ",") + `]}`)
	return t
}

func TestImportEdges(t *testing.T) {
	ctx := context.Background()
	// Two sensors that sanitise alike are an error naming both, never a
	// silent merge.
	_, err := Import(ctx, TreeGetter(tree(map[string]string{
		"a": `{"Name":"CPU Temp","ReadingType":"Temperature","Reading":40}`,
		"b": `{"Name":"CPU Temperature","ReadingType":"Temperature","Reading":41}`,
	})), opts())
	if err == nil || !strings.Contains(err.Error(), "NODE1_Temp_CPU") || !strings.Contains(err.Error(), `"CPU Temp"`) || !strings.Contains(err.Error(), `"CPU Temperature"`) {
		t.Fatalf("collision: %v", err)
	}

	// A temperature Sensor that says so only by ReadingUnits "Cel" (DMTF's
	// own public-rackmount1 omits ReadingType on most of them) counts; a
	// fan tach does not; the first of two systems is taken, with a note.
	out, err := Import(ctx, TreeGetter(tree(map[string]string{
		"t": `{"Name":"Ambient Temperature","ReadingUnits":"Cel","Reading":22.5,"PhysicalContext":"Room"}`,
		"f": `{"Name":"Fan","ReadingType":"Rotational","ReadingUnits":"RPM","Reading":2000}`,
	})), opts())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(tagNames(out.Manifest), ","); got != "NODE1,NODE1_Temp_Ambient" {
		t.Fatalf("tags = %s", got)
	}
	// The effective resource: the member's own, or the one build() hoisted
	// to the tag when it was the most used.
	if b, tg := out.Manifest.Tags[0].Members["InletTempC"], out.Manifest.Tags[0]; b.Resource != "/redfish/v1/Chassis/1/Sensors/t" && !(b.Resource == "" && tg.Resource == "/redfish/v1/Chassis/1/Sensors/t") {
		t.Fatalf("the Room sensor is the inlet: %+v (tag resource %s)", b, tg.Resource)
	}
	if !strings.Contains(strings.Join(out.Notes, "\n"), "2 system resources; took /redfish/v1/Systems/A") {
		t.Fatalf("notes = %v", out.Notes)
	}

	o := opts()
	o.System = "B"
	out, err = Import(ctx, TreeGetter(tree(nil)), o)
	if err != nil || out.Manifest.Tags[0].Resource != "/redfish/v1/Systems/B" {
		t.Fatalf("--system B: %v %+v", err, out.Manifest.Tags)
	}
	o.System = "Z"
	if _, err := Import(ctx, TreeGetter(tree(nil)), o); err == nil || !strings.Contains(err.Error(), "have A, B") {
		t.Fatalf("--system Z: %v", err)
	}
	o = opts()
	o.Tag = "node-1"
	if _, err := Import(ctx, TreeGetter(tree(nil)), o); err == nil || !strings.Contains(err.Error(), "[A-Za-z0-9_]") {
		t.Fatalf("bad tag: %v", err)
	}
}

func TestTagsYAMLFromManifestAlone(t *testing.T) {
	out, body := importTree(t, TreeGetter(load(t, "legacy-1u")))
	a, err := TagsYAML(out.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := redfish.ParseManifest(body)
	b, _ := TagsYAML(m)
	if string(a) != string(b) {
		t.Fatal("the tag file must be derivable from the committed manifest alone")
	}
	for _, want := range []string{
		`- { name: NODE1, role: input, type: Server, desc: "Example Systems R1U-L" }`,
		`- { name: NODE1_Fan4, role: input, type: Fan, desc: "Fan 4" }`,
		`- { name: NODE1__Online, role: input, desc:`,
	} {
		if !strings.Contains(string(a), want) {
			t.Errorf("tag file lacks %q:\n%s", want, a)
		}
	}
	// A hand-added command becomes an output tag.
	m.Writes = []redfish.Write{{Name: "NODE1_PowerCmd", Tag: "NODE1", Member: "PowerOn", Target: "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset"}}
	c, _ := TagsYAML(m)
	if !strings.Contains(string(c), `- { name: NODE1_PowerCmd, role: output, init: 0, desc:`) {
		t.Fatalf("command tag:\n%s", c)
	}
	// And the manifest renders it back.
	if !strings.Contains(string(ManifestYAML(m, "x")), "writes:\n  - name: NODE1_PowerCmd\n    tag: NODE1\n    member: PowerOn\n") {
		t.Fatal("writes do not render")
	}
}
