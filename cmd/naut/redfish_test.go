package main

// The importer's semantics are tested in redfish/codegen. What is pinned
// here is the COMMAND and its artifacts: for each recorded BMC under
// redfish/testdata, the three generated files byte for byte
// (testdata/redfish/<fixture>/, refresh with -update); that the generated
// hw_types.st compiles with a program using the generated tags; that
// `tags` re-derives the tag file from the committed manifest; that a live
// import through `serve` gives the recording's bytes; and that `browse
// --record` writes a recording that imports the same again.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/st"
	"github.com/joyautomation/nautilus/redfish"
)

var redfishFixtures = []string{"supermicro-x14", "legacy-1u", "subsystem-1u"}

func redfishFixture(name string) string {
	return filepath.Join("..", "..", "redfish", "testdata", name)
}

func redfishGolden(t *testing.T, fixture, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "redfish", fixture, name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run `go test ./cmd/naut -run Redfish -args -update` to create it)", err)
	}
	if string(want) != string(got) {
		t.Errorf("%s/%s drifted from the golden:\n--- got ---\n%s\n--- want ---\n%s", fixture, name, got, want)
	}
}

// redfishImport runs `naut redfish import` into dir and returns the three
// files.
func redfishImport(t *testing.T, dir string, args ...string) (manifest, tags, types string) {
	t.Helper()
	all := append([]string{"import", "--tag", "NODE1", "--user", "admin", "--password-env", "NODE1_BMC_PASSWORD", "--out", dir}, args...)
	out, code := captureRun(t, func() int { return runRedfish(all) })
	if code != 0 {
		t.Fatalf("import %v failed (%d):\n%s", args, code, out)
	}
	return read(t, filepath.Join(dir, "redfish_manifest.yaml")),
		read(t, filepath.Join(dir, "tags", "redfish.yaml")),
		read(t, filepath.Join(dir, "hw_types.st"))
}

func TestRedfishImportGolden(t *testing.T) {
	for _, fx := range redfishFixtures {
		t.Run(fx, func(t *testing.T) {
			args := []string{"--mockup", redfishFixture(fx), "--host", "https://bmc1", "--insecure"}
			m, tags, types := redfishImport(t, t.TempDir(), args...)
			redfishGolden(t, fx, "redfish_manifest.yaml", []byte(m))
			redfishGolden(t, fx, "tags_redfish.yaml", []byte(tags))
			redfishGolden(t, fx, "hw_types.st", []byte(types))

			// Byte-identical on re-run, from another directory.
			m2, t2, ty2 := redfishImport(t, t.TempDir(), args...)
			if m2 != m || t2 != tags || ty2 != types {
				t.Fatal("a second import differs from the first")
			}

			// The generated manifest loads through the driver's strict
			// decoder and builds a driver, offline.
			man, err := redfish.ParseManifest([]byte(m))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := redfish.New(man); err != nil {
				t.Fatal(err)
			}

			// hw_types.st compiles together with a program that declares
			// every generated struct tag by its generated type.
			var decls strings.Builder
			for _, tag := range man.Tags {
				decls.WriteString("  " + tag.Name + " : " + tag.Type + ";\n")
			}
			prog := types + "\nPROGRAM Main\nVAR_EXTERNAL\n" + decls.String() +
				"  NODE1__Online : BOOL;\n  Hot : BOOL;\nEND_VAR\nIF NODE1__Online THEN\n  Hot := NODE1.MaxTempC > 80.0 OR NODE1.Fault;\nEND_IF;\nEND_PROGRAM\n"
			parsed, err := st.Parse(prog)
			if err != nil {
				t.Fatalf("hw_types.st + program does not parse: %v", err)
			}
			if _, err := st.Lower(parsed); err != nil {
				t.Fatalf("hw_types.st + program does not compile: %v", err)
			}
		})
	}
}

// `naut redfish tags` re-derives the import's tag file from the committed
// manifest, byte for byte.
func TestRedfishTagsRederives(t *testing.T) {
	dir := t.TempDir()
	_, tags, _ := redfishImport(t, dir, "--mockup", redfishFixture("legacy-1u"), "--host", "https://bmc1")
	again := filepath.Join(dir, "again.yaml")
	out, code := captureRun(t, func() int {
		return runRedfish([]string{"tags", "-o", again, filepath.Join(dir, "redfish_manifest.yaml")})
	})
	if code != 0 {
		t.Fatalf("tags failed (%d):\n%s", code, out)
	}
	if got := read(t, again); got != tags {
		t.Errorf("re-derived tag file differs:\n%s", got)
	}
}

// The same bytes from the live BMC and from its recording: import through
// `serve` (a stand-in with session auth) against the offline import; then
// `browse --record` that service and import the recording.
func TestRedfishLiveImportAndRecord(t *testing.T) {
	t.Setenv("RF_SERVE_PASSWORD", "pw")
	t.Setenv("NODE1_BMC_PASSWORD", "pw")
	fx := "supermicro-x14"
	offline, _, _ := redfishImport(t, t.TempDir(), "--mockup", redfishFixture(fx), "--host", "https://bmc1")

	var srvOut strings.Builder
	srv, err := startRedfishServe(redfishFixture(fx), "127.0.0.1:0", "session", "admin", "RF_SERVE_PASSWORD", &srvOut)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	if !strings.Contains(srvOut.String(), srv.URL()) {
		t.Fatalf("serve banner: %s", srvOut.String())
	}
	// Live: --host is the stand-in; the manifest must still say the BMC's
	// real address, so the live import passes --host twice in spirit:
	// compare with the host line normalised.
	live, _, _ := redfishImport(t, t.TempDir(), "--host", srv.URL())
	if strings.Replace(live, `host: "`+srv.URL()+`"`, `host: "https://bmc1"`, 1) != offline {
		t.Fatalf("live import differs from the recording's:\n%s", live)
	}

	rec := filepath.Join(t.TempDir(), "rec")
	out, code := captureRun(t, func() int {
		return runRedfish([]string{"browse", "--host", srv.URL(), "--user", "admin", "--password-env", "NODE1_BMC_PASSWORD", "--record", rec})
	})
	if code != 0 || !strings.Contains(out, "recorded ") {
		t.Fatalf("browse --record (%d):\n%s", code, out)
	}
	again, _, _ := redfishImport(t, t.TempDir(), "--mockup", rec, "--host", "https://bmc1")
	if again != offline {
		t.Fatalf("import of the recording differs:\n%s", again)
	}

	// browse without --record: the resource, its links, the auth used.
	out, code = captureRun(t, func() int {
		return runRedfish([]string{"browse", "--host", srv.URL(), "--user", "admin", "--password-env", "NODE1_BMC_PASSWORD", "--path", "/redfish/v1/Chassis/1/ThermalSubsystem/Fans/FAN1"})
	})
	if code != 0 || !strings.Contains(out, `"SpeedRPM": 7200`) || !strings.Contains(out, "/redfish/v1/Chassis/1/Sensors/fan_tach_FAN1") || !strings.Contains(out, "auth: session") {
		t.Fatalf("browse (%d):\n%s", code, out)
	}
}

func TestRedfishUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"nonsense"},
		{"import", "--tag", "NODE1"},
		{"import", "--host", "https://bmc1"},
		{"browse"},
		{"serve"},
		{"serve", "--mockup", "x", "--from", "http://127.0.0.1:8087"}, // --from without --manifest
		{"tags"},
	} {
		_, code := captureRun(t, func() int { return runRedfish(args) })
		if code != 2 {
			t.Errorf("%v: code %d, want 2", args, code)
		}
	}
	// A --tag that cannot name a tag fails the import, not the usage.
	_, code := captureRun(t, func() int {
		return runRedfish([]string{"import", "--tag", "node-1", "--host", "https://bmc1", "--mockup", redfishFixture("legacy-1u"), "--out", t.TempDir()})
	})
	if code != 1 {
		t.Errorf("bad tag: code %d", code)
	}
}

// --manifest/--source pick the BMC a plant-fed stand-in is, and it listens
// where the monitoring project polls it.
func TestRedfishServePlantSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.yaml")
	two := `sources:
  - {id: NODE1, host: "http://127.0.0.1:8001"}
  - {id: NODE2, host: "http://127.0.0.1:8002"}
tags:
  - {name: NODE2, type: Server, source: NODE2, resource: /redfish/v1/Systems/1, members: {PowerOn: {path: PowerState, eq: "On"}}}
`
	if err := os.WriteFile(path, []byte(two), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, src, host, err := loadRedfishPlant(path, "NODE2"); err != nil || src != "NODE2" || host != "127.0.0.1:8002" {
		t.Fatalf("NODE2: %q %q %v", src, host, err)
	}
	if _, _, _, err := loadRedfishPlant(path, ""); err == nil || !strings.Contains(err.Error(), "--source") {
		t.Fatalf("two sources and no --source: %v", err)
	}
	if _, _, _, err := loadRedfishPlant(path, "NODE9"); err == nil {
		t.Fatal("an unknown --source must be an error")
	}
}
