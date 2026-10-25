package blackbox

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// Manifest is checks.yaml: what the tester proposed, and where each check
// came from. Only entries with a Script and a disposition the run was asked
// for become subtests; the rest are listed for the record.
type Manifest struct {
	Oracles  map[string]string            `yaml:"oracles"`
	Projects map[string]map[string]string `yaml:"projects"` // the tester's address notes, kept as written
	Checks   []Check                      `yaml:"checks"`
}

// Check is one entry of the manifest; the field names are the tester's
// (nautilus-blackbox runs/README.md) plus run and the rewritten paths.
type Check struct {
	ID          string            `yaml:"id"`
	Run         string            `yaml:"run"`
	Finding     string            `yaml:"finding"`
	Pass        string            `yaml:"pass"`
	Disposition string            `yaml:"disposition"`
	Surface     string            `yaml:"surface"`
	Oracle      string            `yaml:"oracle"`
	Project     string            `yaml:"project"`
	Script      string            `yaml:"script"`
	Env         map[string]string `yaml:"env"`
	Timeout     string            `yaml:"timeout"`
	Note        string            `yaml:"note"`

	// The tester's own bookkeeping, kept verbatim.
	ExpectedToday string `yaml:"expected_today"`
	Group         string `yaml:"group"`
	Revised       string `yaml:"revised"` // the run that revised a check under its old id

	// Open names the issue a check's finding is filed under while it is
	// unfixed: the check is expected to fail, and does not fail the job
	// until it unexpectedly passes, which is the signal to clear this.
	Open string `yaml:"open"`
}

// TestBlackbox starts each project the manifest names, once, and runs every
// check against it as a subtest named by the check id, so a claim can cite
// `go ./tools/blackbox TestBlackbox/<id>`. Skips unless
// NAUTILUS_BLACKBOX_PYTHON names an interpreter with the oracles installed.
func TestBlackbox(t *testing.T) {
	python := os.Getenv("NAUTILUS_BLACKBOX_PYTHON")
	if python == "" {
		t.Skip("set NAUTILUS_BLACKBOX_PYTHON=/path/to/python (tools/blackbox/requirements.txt installed) to run the black-box checks")
	}
	want := os.Getenv("NAUTILUS_BLACKBOX_DISPOSITION")
	if want == "" {
		want = "gate"
	}
	manifestPath := os.Getenv("NAUTILUS_BLACKBOX_MANIFEST") // runner development only
	if manifestPath == "" {
		manifestPath = "checks.yaml"
	}
	m := loadManifest(t, manifestPath)
	base := filepath.Dir(manifestPath)

	byProject := map[string][]Check{}
	var order []string
	for _, c := range m.Checks {
		if c.Script == "" || c.Disposition != want {
			continue
		}
		if c.ID == "" || c.Project == "" {
			t.Fatalf("checks.yaml: entry %+v needs id and project", c)
		}
		if _, seen := byProject[c.Project]; !seen {
			order = append(order, c.Project)
		}
		byProject[c.Project] = append(byProject[c.Project], c)
	}
	if len(order) == 0 {
		t.Skipf("no %s checks in %s", want, manifestPath)
	}

	naut := os.Getenv("NAUTILUS_BLACKBOX_NAUT")
	if naut == "" {
		naut = buildNaut(t)
	}

	for _, project := range order {
		sut := startProject(t, naut, filepath.Join(base, project), m.Projects[project])
		// A check for an open finding may leave the SUT damaged (the
		// overflow one flips a node's type for good), so those run last.
		checks := byProject[project]
		sort.SliceStable(checks, func(i, j int) bool { return checks[i].Open == "" && checks[j].Open != "" })
		for _, c := range checks {
			c := c
			t.Run(c.ID, func(t *testing.T) { runCheck(t, python, filepath.Join(base, c.Script), sut, c) })
		}
		sut.stop()
	}
}

func loadManifest(t *testing.T, path string) Manifest {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return m
}

// buildNaut builds the CLI from this checkout: the gate job's SUT. The
// release job hands in the published binary with NAUTILUS_BLACKBOX_NAUT.
// With NAUTILUS_BLACKBOX_COVERDIR set the binary is coverage-instrumented
// and each project's naut writes its counters there on exit: the measure
// of how much of the code the foreign client's checks reach.
func buildNaut(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "naut")
	args := []string{"build", "-o", bin}
	if os.Getenv("NAUTILUS_BLACKBOX_COVERDIR") != "" {
		args = append(args, "-cover", "-coverpkg=./...")
	}
	cmd := exec.Command("go", append(args, "./cmd/naut")...)
	cmd.Dir = filepath.Join("..", "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/naut: %v\n%s", err, out)
	}
	return bin
}

// sut is one running project: the addresses its nautilus.yaml declares,
// handed to every check through the environment.
type sut struct {
	dir  string
	env  []string
	stop func()
}

// project is the slice of nautilus.yaml the runner needs: where the HTTP
// API and the protocol servers listen.
type project struct {
	Server struct {
		Addr string `yaml:"addr"`
	} `yaml:"server"`
	OPCUA struct {
		Endpoint string `yaml:"endpoint"`
	} `yaml:"opcua"`
	Modbus struct {
		Listen string `yaml:"listen"`
	} `yaml:"modbus"`
	MQTT struct {
		Broker string `yaml:"broker"`
	} `yaml:"mqtt"`
}

// startProject copies the project to a temp dir (naut run writes state
// beside the manifest), starts naut run there and waits for the HTTP API.
// addrs is the manifest's note of where the project listens (the tester's
// own words); what it leaves out is read from the project's nautilus.yaml.
func startProject(t *testing.T, naut, src string, addrs map[string]string) *sut {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("cp", "-r", src+"/.", dir).CombinedOutput(); err != nil {
		t.Fatalf("copy %s: %v\n%s", src, err, out)
	}
	b, err := os.ReadFile(filepath.Join(dir, "nautilus.yaml"))
	if err != nil {
		t.Fatalf("%s: %v", src, err)
	}
	var p project
	if err := yaml.Unmarshal(b, &p); err != nil {
		t.Fatalf("%s/nautilus.yaml: %v", src, err)
	}
	if p.Server.Addr == "" {
		p.Server.Addr = "localhost:8080"
	}
	if a := addrs["http"]; a != "" {
		p.Server.Addr = strings.TrimPrefix(a, "http://")
	}
	if a := addrs["opcua"]; a != "" {
		p.OPCUA.Endpoint = a
	}
	if a := addrs["modbus"]; a != "" {
		p.Modbus.Listen = a
	}
	if a := addrs["mqtt"]; a != "" {
		p.MQTT.Broker = a
	}

	cmd := exec.Command(naut, "run")
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "NAUTILUS_ADDR=") { // the project's addr is the one the checks were written against
			cmd.Env = append(cmd.Env, kv)
		}
	}
	if dir := os.Getenv("NAUTILUS_BLACKBOX_COVERDIR"); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		cmd.Env = append(cmd.Env, "GOCOVERDIR="+dir)
	}
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	stop := func() {
		cmd.Process.Signal(os.Interrupt)
		select {
		case <-exited:
		case <-time.After(15 * time.Second):
			cmd.Process.Kill()
			<-exited
		}
	}
	t.Cleanup(stop)

	// Up means the dashboard answers at all; which paths exist is the
	// checks' business, not the runner's.
	probe := "http://" + dialable(p.Server.Addr) + "/"
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := http.Get(probe)
		if err == nil {
			resp.Body.Close()
			break
		}
		select {
		case <-exited:
			t.Fatalf("naut run exited before the HTTP API answered:\n%s", out.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("naut run: %s did not answer within 30s:\n%s", probe, out.String())
		}
		time.Sleep(100 * time.Millisecond)
	}

	nautDir, _ := filepath.Abs(filepath.Dir(naut))
	env := []string{
		"SUT_PROJECT=" + dir,
		"SUT_HTTP=http://" + p.Server.Addr,
		// Checks that start a controller of their own call `naut`, and the
		// shell ones call the oracles' CLI tools, as the tester's container
		// had them on PATH.
		"PATH=" + nautDir + string(os.PathListSeparator) + filepath.Dir(os.Getenv("NAUTILUS_BLACKBOX_PYTHON")) + string(os.PathListSeparator) + os.Getenv("PATH"),
	}
	if p.OPCUA.Endpoint != "" {
		env = append(env, "SUT_OPCUA="+p.OPCUA.Endpoint)
	}
	if p.Modbus.Listen != "" {
		env = append(env, "SUT_MODBUS="+p.Modbus.Listen)
	}
	if p.MQTT.Broker != "" {
		env = append(env, "SUT_MQTT="+p.MQTT.Broker)
	}
	return &sut{dir: dir, env: env, stop: stop}
}

// dialable turns a listen address into one a client can connect to: a
// wildcard host becomes loopback.
func dialable(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func runCheck(t *testing.T, python, script string, s *sut, c Check) {
	t.Helper()
	timeout := 120 * time.Second
	if c.Timeout != "" {
		d, err := time.ParseDuration(c.Timeout)
		if err != nil {
			t.Fatalf("%s: timeout %q: %v", c.ID, c.Timeout, err)
		}
		timeout = d
	}
	abs, err := filepath.Abs(script)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var cmd *exec.Cmd
	if strings.HasSuffix(abs, ".sh") {
		cmd = exec.CommandContext(ctx, "bash", abs)
	} else {
		cmd = exec.CommandContext(ctx, python, "-I", abs)
	}
	cmd.Dir = s.dir
	cmd.Env = append(append(os.Environ(), s.env...), envList(c.Env)...)
	out, err := cmd.CombinedOutput()
	what := c.Finding
	if what == "" {
		what = "pass: " + c.Pass
	}
	t.Logf("%s · %s · run %s · %s · oracle %s\n%s", c.ID, what, c.Run, c.Surface, c.Oracle, out)
	if ctx.Err() != nil {
		t.Fatalf("%s: no verdict within %s", c.ID, timeout)
	}
	switch {
	case err != nil && c.Open != "":
		t.Skipf("%s: fails as expected; its finding is open under %s", c.ID, c.Open)
	case err != nil:
		t.Fatalf("%s: %v", c.ID, err)
	case c.Open != "":
		t.Fatalf("%s passes, so the finding filed under %s is fixed: clear `open` in checks.yaml", c.ID, c.Open)
	}
}

func envList(m map[string]string) []string {
	var env []string
	for k, v := range m {
		env = append(env, k+"="+v)
	}
	return env
}
