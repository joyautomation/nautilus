// foreign_test.go is the foreign-implementation suite: the real importer,
// client and Driver against DMTF's own Redfish-Mockup-Server serving two
// of DMTF's published mockups (DSP2043), instead of our mockup package —
// so the session dance, the path decoder and both schema generations are
// checked against somebody else's server and somebody else's JSON:
//
//	public-localstorage  legacy: Chassis/1U/Thermal + Power only
//	public-rackmount1    current: ThermalSubsystem/PowerSubsystem/Sensors
//	                     (and the deprecated Thermal/Power beside them)
//
// Gated on NAUTILUS_REDFISH_SIM=<dir> (scripts/redfish-sim.sh prepares the
// directory: venv/, server/redfishMockupServer.py, mockups/) exactly like
// modbus' pymodbus suite — normal `go test ./...` skips it. The test
// starts and stops the Python server itself: the session-recovery case
// has to restart it under a running driver.
package redfish_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/redfish"
	"github.com/joyautomation/nautilus/redfish/codegen"
	"github.com/joyautomation/nautilus/redfish/mockup"
)

func simDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("NAUTILUS_REDFISH_SIM")
	if dir == "" {
		t.Skip("set NAUTILUS_REDFISH_SIM=<dir> (scripts/redfish-sim.sh prepares it) to run the foreign-stack test")
	}
	return dir
}

// dmtf is one running Redfish-Mockup-Server.
type dmtf struct {
	t      *testing.T
	dir    string
	mockup string
	port   int
	cmd    *exec.Cmd
}

func (s *dmtf) url() string { return fmt.Sprintf("http://127.0.0.1:%d", s.port) }

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func startDMTF(t *testing.T, mockupName string) *dmtf {
	t.Helper()
	s := &dmtf{t: t, dir: simDir(t), mockup: mockupName, port: freePort(t)}
	s.start()
	t.Cleanup(s.stop)
	return s
}

func (s *dmtf) start() {
	s.t.Helper()
	cmd := exec.Command(filepath.Join(s.dir, "venv", "bin", "python"),
		filepath.Join(s.dir, "server", "redfishMockupServer.py"),
		"-S", "-D", filepath.Join(s.dir, "mockups", s.mockup),
		"-H", "127.0.0.1", "-p", fmt.Sprint(s.port))
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		s.t.Fatal(err)
	}
	s.cmd = cmd
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(s.url() + "/redfish/v1")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.t.Fatalf("Redfish-Mockup-Server did not come up on %s", s.url())
}

func (s *dmtf) stop() {
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _ = s.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = s.cmd.Process.Kill()
		<-done
	}
	s.cmd = nil
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// importLive imports through the driver's own client, logged in: the DMTF
// server answers the session POST with 204 and a token, not 201.
func importLive(t *testing.T, url string) (codegen.Output, []byte, *redfish.Client) {
	t.Helper()
	t.Setenv("RF_FOREIGN_PASSWORD", "password")
	cl, err := redfish.NewClient(redfish.Source{ID: "NODE1", Host: url, User: "Administrator", PasswordEnv: "RF_FOREIGN_PASSWORD"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close(context.Background()) })
	out, err := codegen.Import(context.Background(), codegen.FetcherGetter(cl), codegen.Options{Tag: "NODE1", Host: "https://bmc1"})
	if err != nil {
		t.Fatal(err)
	}
	return out, codegen.ManifestYAML(out.Manifest, "naut redfish import --tag NODE1"), cl
}

// driveFor starts a driver on m against url and waits for a full delivery.
func driveFor(t *testing.T, m redfish.Manifest, url string) *redfish.Driver {
	t.Helper()
	m.Sources[0].Host = url
	m.Sources[0].User, m.Sources[0].PasswordEnv = "Administrator", "RF_FOREIGN_PASSWORD"
	m.Sources[0].Interval = 200 * time.Millisecond
	m.Sources[0].StaleAfter = 800 * time.Millisecond
	d, err := redfish.New(m, redfish.WithLogger(quietLog()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.Start(ctx)
	t.Cleanup(func() { cancel(); d.Stop() })
	waitFor(t, 10*time.Second, "first delivery", func() bool {
		v, _ := d.ReadInputs()
		return v["NODE1__Online"] == true
	})
	return d
}

func waitFor(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func get(t *testing.T, d *redfish.Driver, tag, m string) ir.Value {
	t.Helper()
	vals, _ := d.ReadInputs()
	v, ok := vals[tag].(ir.Value)
	if !ok {
		t.Fatalf("%s not delivered", tag)
	}
	return v.Fld[v.Struct.FieldIndex[m]]
}

var foreignMockups = []struct {
	name, generation string
	// spot values DMTF's JSON must decode to (tag, member, want)
	spot []struct {
		tag, member string
		want        any
	}
}{
	{"public-localstorage", "Thermal/Power", []struct {
		tag, member string
		want        any
	}{
		{"NODE1", "PowerOn", true},
		{"NODE1", "Model", "3500RX"},
		{"NODE1", "PowerW", 344.0},
		{"NODE1", "InletTempC", 25.0},
		{"NODE1", "MaxTempC", 41.0},
		{"NODE1_Fan1", "RPM", 2100.0},
		{"NODE1_PSU1", "InputV", 120.0},
		{"NODE1_PSU1", "OutputW", 325.0},
		{"NODE1_PSU1", "Fault", true}, // Health: Warning in the mockup
		{"NODE1_PSU1", "InputOk", true},
		{"NODE1_Temp_CPU1", "Value", 41.0},
		{"NODE1_Temp_CPU1", "HighSP", 42.0},
		{"NODE1_Temp_CPU1", "High", false},
	}},
	{"public-rackmount1", "ThermalSubsystem/PowerSubsystem", []struct {
		tag, member string
		want        any
	}{
		{"NODE1", "PowerOn", true},
		{"NODE1", "Model", "3500"},
		{"NODE1", "PowerW", 374.0},
		{"NODE1", "InletTempC", 24.8},
		{"NODE1_Fan1", "RPM", 2200.0},
		{"NODE1_Fan1", "Pct", 45.0},
		{"NODE1_PSU1", "InputV", 230.2},
		{"NODE1_PSU1", "Fault", true}, // predicted failure: Health Warning
		{"NODE1_Temp_CPU_1", "Value", 44.0},
		{"NODE1_Temp_CPU_1", "High", true}, // 44 ≥ the 42 UpperCaution
		{"NODE1_Temp_CPU_1", "HighHigh", false},
	}},
}

func TestForeignImportAndDecode(t *testing.T) {
	for _, fm := range foreignMockups {
		t.Run(fm.name, func(t *testing.T) {
			srv := startDMTF(t, fm.name)
			out, first, cl := importLive(t, srv.url())
			if out.Generation != fm.generation {
				t.Fatalf("generation = %s", out.Generation)
			}
			if cl.AuthMode() != redfish.AuthSession || cl.Sessions() != 1 {
				t.Fatalf("auth %s, %d sessions: the DMTF server's 204-with-token must count as a session", cl.AuthMode(), cl.Sessions())
			}
			// Byte-identical on re-run.
			_, second, _ := importLive(t, srv.url())
			if string(first) != string(second) {
				t.Fatalf("a second live import differs:\n%s\n---\n%s", first, second)
			}
			// …and the same bytes from the bundle directory on disk: the
			// recording and the live service agree.
			tree, err := mockup.LoadDir(filepath.Join(simDir(t), "mockups", fm.name))
			if err != nil {
				t.Fatal(err)
			}
			offline, err := codegen.Import(context.Background(), codegen.TreeGetter(tree), codegen.Options{Tag: "NODE1", Host: "https://bmc1"})
			if err != nil {
				t.Fatal(err)
			}
			if got := codegen.ManifestYAML(offline.Manifest, "naut redfish import --tag NODE1"); string(got) != string(first) {
				t.Fatalf("offline import of the bundle differs from the live one:\n%s", got)
			}
			t.Logf("%s: %d tags; notes: %s", fm.name, len(out.Manifest.Tags), strings.Join(out.Notes, "; "))

			// Every generated binding resolves on the live server, and
			// every tag is Good.
			d := driveFor(t, out.Manifest, srv.url())
			waitFor(t, 5*time.Second, "all tags Good", func() bool { return len(d.Quality()) == 0 })
			if a := d.Absent(); len(a) != 0 {
				t.Fatalf("generated bindings that do not resolve on DMTF's server: %v", a)
			}
			for _, s := range fm.spot {
				got := get(t, d, s.tag, s.member)
				var ok bool
				switch w := s.want.(type) {
				case bool:
					ok = got.Kind == ir.TypeBool && got.B == w
				case float64:
					ok = got.Kind == ir.TypeReal && got.F == w
				case string:
					ok = got.Kind == ir.TypeString && got.S == w
				}
				if !ok {
					t.Errorf("%s.%s = %+v, want %v", s.tag, s.member, got, s.want)
				}
			}
		})
	}
}

// The BMC restarts under a running driver: Stale and __Online false while
// it is gone, values held, then recovery — with a fresh session.
func TestForeignRestartRecovers(t *testing.T) {
	srv := startDMTF(t, "public-rackmount1")
	out, _, _ := importLive(t, srv.url())
	d := driveFor(t, out.Manifest, srv.url())
	rpm := get(t, d, "NODE1_Fan1", "RPM").F

	srv.stop()
	waitFor(t, 10*time.Second, "Stale while the server is down", func() bool {
		v, err := d.ReadInputs()
		return err == nil && v["NODE1__Online"] == false && d.Quality()["NODE1_Fan1"] == nio.Stale
	})
	if get(t, d, "NODE1_Fan1", "RPM").F != rpm {
		t.Fatal("values must hold while the BMC is down")
	}
	srv.start()
	waitFor(t, 70*time.Second, "recovery after the restart", func() bool {
		v, _ := d.ReadInputs()
		return v["NODE1__Online"] == true && len(d.Quality()) == 0
	})
	if h := d.Health(); h.Sources[0].State != "connected" || h.Sources[0].Retries == 0 {
		t.Fatalf("health after recovery = %+v", h.Sources[0])
	}
}

// A fan is pulled from its hot-swap bay mid-run: DMTF's server answers a
// DELETE of a collection member by 404-ing it from then on. Exactly the
// tag bound to it turns Bad and holds its last value; its siblings stay
// Good and the source stays online. A server restart restores the bay and
// the tag recovers.
func TestForeign404IsBadSiblingsGood(t *testing.T) {
	srv := startDMTF(t, "public-rackmount1")
	out, _, _ := importLive(t, srv.url())
	d := driveFor(t, out.Manifest, srv.url())
	waitFor(t, 5*time.Second, "all tags Good", func() bool { return len(d.Quality()) == 0 })

	const bay1 = "/redfish/v1/Chassis/1U/ThermalSubsystem/Fans/Bay1"
	req, _ := http.NewRequest(http.MethodDelete, srv.url()+bay1, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE %s: %d", bay1, resp.StatusCode)
	}
	waitFor(t, 5*time.Second, "Fan1 Bad", func() bool { return d.Quality()["NODE1_Fan1"] == nio.Bad })
	time.Sleep(500 * time.Millisecond)
	if q := d.Quality(); len(q) != 1 {
		t.Fatalf("quality = %v — only the pulled fan may be non-Good", q)
	}
	v, _ := d.ReadInputs()
	if v["NODE1__Online"] != true {
		t.Fatal("a 404 on one resource must not take the source offline")
	}
	if get(t, d, "NODE1_Fan1", "RPM").F != 2200 || get(t, d, "NODE1_Fan2", "RPM").F == 0 {
		t.Fatal("the pulled fan holds its last value; a sibling keeps delivering")
	}

	srv.stop()
	srv.start()
	waitFor(t, 70*time.Second, "Fan1 back after the restart", func() bool { return len(d.Quality()) == 0 })
}
