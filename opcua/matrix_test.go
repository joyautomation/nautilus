// The OPC UA client matrix: the driver against servers it did not grow up
// with (#704). Every other test in this package speaks gopcua to gopcua —
// one codec on both ends. Here each server is someone else's stack in a
// container, and every server runs the same scenarios through the
// driver's public surface:
//
//   - import:    browse the scalar folder, generate the manifest twice,
//     byte-identical, every type mapped and writable;
//   - reads:     every scalar type Good and typed, a value that changes on
//     its own followed, an unknown node Bad without taking the rest down;
//   - writes:    every scalar type written and read back, a read-only node
//     refused on a healthy session;
//   - remap:     a binding carrying a wrong namespace index read by URI;
//   - restart:   `docker restart` (a clean close): __Online falls, delivered
//     values go Stale, and both come back on their own;
//   - blackhole: `docker pause` (a power cycle as the wire sees it: no FIN,
//     no RST, just silence): the same, detected by the request timeout;
//
// and the goroutine count is flat across the reconnects.
//
// The test starts the containers itself (docker on PATH). It is skipped
// unless NAUTILUS_OPCUA_MATRIX names the servers to run ("all", or a
// comma list of profile names). Images are pinned by digest; open62541 is
// built from opcua/testdata/matrix/open62541 at a tagged release. Server
// quirks found here are recorded in the guide (guides/opcua-client.md).
package opcua_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	gopcua "github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/opcua"
	"github.com/joyautomation/nautilus/opcua/codegen"
)

// matrixServer is one server's profile: how to run it and where its test
// nodes are. Node ids carry the index the server serves them at today;
// the bindings also carry the URI, so a renumbering is survived.
type matrixServer struct {
	name string
	// image is a pinned reference; build, when set, is the directory it is
	// built from (with `docker build -t image`) if it is not present.
	image string
	build string
	// path is the endpoint URL's path ("/milo"); the port is mapped.
	path string
	// nsURI is the namespace every test node lives in.
	nsURI string
	// scalarRoot is the folder holding scalars (browsed by the import).
	scalarRoot string
	// scalars maps each built-in type to a writable variable of it.
	scalars map[string]string
	// changing is a variable whose value changes on its own, faster than
	// once a second, and its type.
	changing, changingType string
	// readOnly is a variable the server refuses writes to, and its type.
	readOnly, readOnlyType string
	// trust makes the server trust the client certificate in pki, once a
	// secured connect has been refused (nil: the server accepts any).
	trust func(t *testing.T, c *container)
	// startup bounds how long the server takes to answer after a start.
	startup time.Duration
}

var matrixServers = []matrixServer{
	{
		name:       "open62541",
		image:      "nautilus-test/open62541:v1.4.22",
		build:      "testdata/matrix/open62541",
		nsURI:      "urn:nautilus:matrix:open62541",
		scalarRoot: "ns=2;s=Scalar",
		scalars:    scalarIDs("ns=2;s=Scalar.%s"),
		changing:   "ns=2;s=Dynamic.Counter", changingType: "Int32",
		readOnly: "ns=2;s=Dynamic.ReadOnly", readOnlyType: "Int32",
		startup: 20 * time.Second,
	},
	{
		name:       "milo",
		image:      "digitalpetri/opc-ua-demo-server@sha256:a479834444cabfda3013a15d24ee21f87d7ca00defe75a70c67c000e8714e2fe", // 1.0.3
		path:       "/milo",
		nsURI:      "urn:opc:eclipse:milo:opc-ua-demo-server:namespace:demo",
		scalarRoot: "ns=2;s=CTT.Static.AllProfiles.Scalar",
		scalars:    scalarIDs("ns=2;s=CTT.Static.AllProfiles.Scalar.%s"),
		changing:   "ns=2;s=Demo.Dynamic.Int32", changingType: "Int32",
		readOnly: "ns=2;s=Demo.Dynamic.Double", readOnlyType: "Double",
		// Milo refuses an unknown client certificate by closing the
		// connection (the client sees EOF, not a status) and files it
		// under security/rejected; trusting it is moving it to trusted.
		trust: func(t *testing.T, c *container) {
			c.exec(t, "sh", "-c", "mv /app/data/security/rejected/*.der /app/data/security/pki/trusted/certs/")
		},
		startup: 60 * time.Second,
	},
}

// matrixTypes are the built-in scalar types the driver maps (TypeNames
// minus none), each written with a value unlike the server's initial one.
var matrixTypes = []struct {
	typ   string
	write any
}{
	{"Boolean", false},
	{"SByte", int64(-100)},
	{"Byte", int64(77)},
	{"Int16", int64(-30000)},
	{"UInt16", int64(65000)},
	{"Int32", int64(-2_000_000_000)},
	{"UInt32", int64(4_000_000_001)},
	{"Int64", int64(-9_000_000_000_000_000)},
	{"UInt64", int64(9_000_000_000_000_000)},
	{"Float", 0.25},
	{"Double", -1.125},
	{"String", "nautilus ✓"},
	{"DateTime", int64(1_767_225_600_000)}, // 2026-01-01T00:00:00Z
}

func scalarIDs(format string) map[string]string {
	out := map[string]string{}
	for _, mt := range matrixTypes {
		out[mt.typ] = fmt.Sprintf(format, mt.typ)
	}
	return out
}

// matrixModes are the ways the driver reads. Each scenario that reads
// runs once per mode.
var matrixModes = []struct {
	name string
	opts []opcua.Option
}{
	{"poll", nil},
}

func TestForeignServerMatrix(t *testing.T) {
	want := os.Getenv("NAUTILUS_OPCUA_MATRIX")
	if want == "" {
		t.Skip("set NAUTILUS_OPCUA_MATRIX=all (or a comma list of servers) to run the OPC UA client matrix; needs docker")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("NAUTILUS_OPCUA_MATRIX is set but docker is not on PATH: %v", err)
	}
	selected := map[string]bool{}
	for s := range strings.SplitSeq(want, ",") {
		selected[strings.TrimSpace(s)] = true
	}
	ran := 0
	for _, s := range matrixServers {
		if !selected["all"] && !selected[s.name] {
			continue
		}
		ran++
		t.Run(s.name, func(t *testing.T) { runMatrix(t, s) })
	}
	if ran == 0 {
		t.Fatalf("NAUTILUS_OPCUA_MATRIX=%q names no server; have %v", want, serverNames())
	}
}

func serverNames() []string {
	var out []string
	for _, s := range matrixServers {
		out = append(out, s.name)
	}
	return out
}

func runMatrix(t *testing.T, s matrixServer) {
	c := startContainer(t, s)
	url := fmt.Sprintf("opc.tcp://127.0.0.1:%d%s", c.port, s.path)
	// One PKI directory for every session to this server: the client
	// certificate the server was told to trust, and the server's pinned
	// thumbprint, must be the same ones across scenarios — as on a site.
	pki := t.TempDir()
	conn := opcua.Connect{PKIDir: pki, Timeout: 5 * time.Second}
	waitServing(t, s, c, url, conn)

	t.Run("import", func(t *testing.T) { matrixImport(t, s, url, conn) })
	for _, mode := range matrixModes {
		opts := append([]opcua.Option{opcua.WithPKIDir(pki)}, mode.opts...)
		t.Run("reads/"+mode.name, func(t *testing.T) { matrixReads(t, s, url, opts) })
		t.Run("writes/"+mode.name, func(t *testing.T) { matrixWrites(t, s, url, opts) })
		t.Run("remap/"+mode.name, func(t *testing.T) { matrixRemap(t, s, url, opts) })
		t.Run("restart/"+mode.name, func(t *testing.T) {
			matrixOutage(t, s, url, opts, "restart", func() { c.docker(t, "restart", "-t", "1", c.name) }, nil)
		})
		t.Run("blackhole/"+mode.name, func(t *testing.T) {
			matrixOutage(t, s, url, opts, "blackhole",
				func() { c.docker(t, "pause", c.name) },
				func() { c.docker(t, "unpause", c.name) })
		})
	}
}

// ── scenarios ────────────────────────────────────────────────────────────

func matrixImport(t *testing.T, s matrixServer, url string, conn opcua.Connect) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	sess, err := opcua.Dial(ctx, url, conn)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer sess.Close()
	root, err := ua.ParseNodeID(s.scalarRoot)
	if err != nil {
		t.Fatal(err)
	}
	gen := func() ([]byte, codegen.Output) {
		br, err := opcua.Browse(ctx, sess.Client, root, 1, nil)
		if err != nil {
			t.Fatalf("browse %s: %v", s.scalarRoot, err)
		}
		out, err := codegen.Generate(br, codegen.Options{WritablePatterns: []string{"*"}, Endpoint: url})
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		y, err := codegen.ManifestYAML(out.Manifest, "naut opcua import")
		if err != nil {
			t.Fatal(err)
		}
		return y, out
	}
	first, out := gen()
	second, _ := gen()
	if !bytes.Equal(first, second) {
		t.Errorf("import is not byte-identical on re-run")
	}
	byNode := map[string]opcua.TagBinding{}
	for _, b := range out.Manifest.Tags {
		byNode[b.NodeID] = b
	}
	for _, mt := range matrixTypes {
		b, ok := byNode[s.scalars[mt.typ]]
		switch {
		case !ok:
			t.Errorf("%s (%s) not imported; skipped: %v", mt.typ, s.scalars[mt.typ], out.Skipped)
		case b.Type != mt.typ:
			t.Errorf("%s imported as type %q", s.scalars[mt.typ], b.Type)
		case !b.Writable:
			t.Errorf("%s imported read-only; the server says it is writable", s.scalars[mt.typ])
		case b.Namespace != s.nsURI:
			t.Errorf("%s imported with namespace %q, want %q", s.scalars[mt.typ], b.Namespace, s.nsURI)
		}
	}
	t.Logf("%s: %d bindings, %d skipped, namespaces %v", s.name, len(out.Manifest.Tags), len(out.Skipped), out.Manifest.Namespaces)
}

func matrixReads(t *testing.T, s matrixServer, url string, opts []opcua.Option) {
	m := s.manifest()
	m.Tags = append(m.Tags, opcua.TagBinding{Name: "Missing", NodeID: nsPrefix(s.changing) + ";s=nautilus.matrix.no.such.node", Namespace: s.nsURI, Type: "Int32"})
	d := startMatrixDriver(t, url, m, opts...)
	waitUntil(t, "online", 30*time.Second, d.Online)

	v := readAll(t, d)
	q := d.Quality()
	for _, mt := range matrixTypes {
		name := "S_" + mt.typ
		if q[name] != nio.Good {
			t.Errorf("%s quality %v, want good", name, q[name])
		}
		if !typedLike(v[name], mt.write) {
			t.Errorf("%s = %#v (%T), want the Go type of %T", name, v[name], v[name], mt.write)
		}
	}
	if q["Missing"] != nio.Bad {
		t.Errorf("unknown node quality %v, want bad", q["Missing"])
	}
	if online, _ := v[d.OnlineTagName()].(bool); !online {
		t.Errorf("%s = %v with a Bad node among Good ones; one bad node must not take the session down", d.OnlineTagName(), v[d.OnlineTagName()])
	}

	first := v["Changing"]
	waitUntil(t, "the changing node to change", 10*time.Second, func() bool {
		return readAll(t, d)["Changing"] != first
	})
}

func matrixWrites(t *testing.T, s matrixServer, url string, opts []opcua.Option) {
	m := s.manifest()
	d := startMatrixDriver(t, url, m, opts...)
	waitUntil(t, "online", 30*time.Second, d.Online)

	// The first push is the baseline (recorded, not sent); then every
	// type gets a value unlike the one there.
	baseline := readAll(t, d)
	if err := d.WriteOutputs(baseline); err != nil {
		t.Fatal(err)
	}
	// A value equal to the baseline is not a command (the driver's change
	// rule), so the Boolean is flipped from whatever the server holds.
	cmd := nio.Values{}
	for _, mt := range matrixTypes {
		cmd["S_"+mt.typ] = mt.write
	}
	if b, ok := baseline["S_Boolean"].(bool); ok {
		cmd["S_Boolean"] = !b
	}
	if err := d.WriteOutputs(cmd); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "every write read back", 15*time.Second, func() bool {
		v := readAll(t, d)
		for name, want := range cmd {
			if !sameValue(v[name], want) {
				return false
			}
		}
		return true
	})
	if h := d.Health(); h.Writes != uint64(len(cmd)) {
		t.Errorf("health counts %d writes, want %d (one per changed value)", h.Writes, len(cmd))
	}

	// A refusal on a healthy session is reported and dropped, not fatal.
	errsBefore := d.Health().Errors
	if err := d.WriteOutputs(nio.Values{"ReadOnly": writeFor(s.readOnlyType)}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the read-only write refused", 10*time.Second, func() bool {
		return d.Health().Errors > errsBefore
	})
	h := d.Health()
	t.Logf("%s refuses a read-only write with: %s", s.name, h.LastError)
	if !d.Online() {
		t.Errorf("a refused write took the session down: %+v", h)
	}
}

func matrixRemap(t *testing.T, s matrixServer, url string, opts []opcua.Option) {
	// The same node, its index wrong (as after a server renumbered its
	// namespaces since the import), its URI right.
	wrong := "ns=7" + strings.TrimPrefix(s.changing, nsPrefix(s.changing))
	m := opcua.Manifest{Tags: []opcua.TagBinding{{Name: "Changing", NodeID: wrong, Namespace: s.nsURI, Type: s.changingType}}}
	d := startMatrixDriver(t, url, m, opts...)
	waitUntil(t, "online", 30*time.Second, d.Online)
	if q := d.Quality()["Changing"]; q != nio.Good {
		t.Fatalf("remapped binding quality %v, want good (lastError %q)", q, d.Health().LastError)
	}
}

// matrixOutage takes the server away (down), checks the driver notices and
// holds its values as Stale, brings it back (up, nil when the outage ends
// by itself) and checks the driver recovers on its own — twice, with the
// goroutine count flat between the two recoveries.
func matrixOutage(t *testing.T, s matrixServer, url string, opts []opcua.Option, kind string, down, up func()) {
	d := startMatrixDriver(t, url, s.manifest(), opts...)
	waitUntil(t, "online", 30*time.Second, d.Online)

	var goroutines []int
	for cycle := 1; cycle <= 2; cycle++ {
		before := readAll(t, d)["Changing"]
		t0 := time.Now()
		down()
		// The request timeout is 2s (startMatrixDriver); a dead session
		// must be noticed well inside the next few seconds.
		waitUntil(t, kind+": __Online to fall", 15*time.Second, func() bool { return !d.Online() })
		fell := time.Since(t0)
		if v := readAll(t, d); v[d.OnlineTagName()] != false {
			t.Errorf("%s: __Online = %v while offline", kind, v[d.OnlineTagName()])
		}
		if q := d.Quality()["S_Int32"]; q != nio.Stale {
			t.Errorf("%s: a delivered value's quality while down is %v, want stale", kind, q)
		}
		if up != nil {
			time.Sleep(2 * time.Second) // stay down across a few retries
			up()
		}
		waitUntil(t, kind+": __Online to return", s.startup+30*time.Second, d.Online)
		back := time.Since(t0)
		if q := d.Quality()["S_Int32"]; q != nio.Good {
			t.Errorf("%s: quality after recovery %v, want good", kind, q)
		}
		waitUntil(t, kind+": the changing node to move again", 10*time.Second, func() bool {
			return readAll(t, d)["Changing"] != before
		})
		t.Logf("%s cycle %d: offline after %v, online again after %v (retries %d)", kind, cycle, fell.Round(time.Millisecond), back.Round(time.Millisecond), d.Health().Retries)
		time.Sleep(500 * time.Millisecond) // let the old session's goroutines exit
		goroutines = append(goroutines, runtime.NumGoroutine())
	}
	if goroutines[1] > goroutines[0]+3 {
		t.Errorf("%s: goroutines grew across reconnects: %v", kind, goroutines)
	}
}

// ── profile → manifest ───────────────────────────────────────────────────

// manifest binds every scalar as S_<Type> (writable), the changing node as
// Changing and the read-only node as ReadOnly (declared writable, so a
// write is attempted and the server must refuse it).
func (s matrixServer) manifest() opcua.Manifest {
	m := opcua.Manifest{Namespaces: []string{"http://opcfoundation.org/UA/"}}
	for _, mt := range matrixTypes {
		m.Tags = append(m.Tags, opcua.TagBinding{Name: "S_" + mt.typ, NodeID: s.scalars[mt.typ], Namespace: s.nsURI, Type: mt.typ, Writable: true})
	}
	m.Tags = append(m.Tags,
		opcua.TagBinding{Name: "Changing", NodeID: s.changing, Namespace: s.nsURI, Type: s.changingType},
		opcua.TagBinding{Name: "ReadOnly", NodeID: s.readOnly, Namespace: s.nsURI, Type: s.readOnlyType, Writable: true},
	)
	return m
}

func nsPrefix(nodeID string) string {
	if i := strings.IndexByte(nodeID, ';'); i > 0 {
		return nodeID[:i]
	}
	return ""
}

func writeFor(typ string) any {
	for _, mt := range matrixTypes {
		if mt.typ == typ {
			return mt.write
		}
	}
	return int64(1)
}

// typedLike: the codec delivers bool, int64, float64 and string.
func typedLike(v, like any) bool {
	switch like.(type) {
	case bool:
		_, ok := v.(bool)
		return ok
	case int64:
		_, ok := v.(int64)
		return ok
	case float64:
		_, ok := v.(float64)
		return ok
	case string:
		_, ok := v.(string)
		return ok
	}
	return false
}

func sameValue(got, want any) bool {
	if g, ok := got.(float64); ok {
		if w, ok := want.(float64); ok {
			return g == w || (g-w < 1e-6 && w-g < 1e-6)
		}
	}
	return got == want
}

// ── driver helpers ───────────────────────────────────────────────────────

func startMatrixDriver(t *testing.T, url string, m opcua.Manifest, opts ...opcua.Option) *opcua.Driver {
	t.Helper()
	all := append([]opcua.Option{
		opcua.WithName("matrix"),
		opcua.WithScanRate(200 * time.Millisecond),
		opcua.WithTimeout(2 * time.Second),
		opcua.WithRetry(200*time.Millisecond, time.Second),
	}, opts...)
	d, err := opcua.New(url, m, all...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.Start(ctx)
	t.Cleanup(func() { cancel(); d.Stop() })
	return d
}

func readAll(t *testing.T, d *opcua.Driver) nio.Values {
	t.Helper()
	v, err := d.ReadInputs()
	if err != nil {
		t.Fatalf("ReadInputs: %v", err)
	}
	return v
}

func waitUntil(t *testing.T, what string, limit time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", limit, what)
}

// ── containers ───────────────────────────────────────────────────────────

type container struct {
	name string
	port int
}

func startContainer(t *testing.T, s matrixServer) *container {
	t.Helper()
	if s.build != "" {
		if err := exec.Command("docker", "image", "inspect", s.image).Run(); err != nil {
			t.Logf("building %s from %s", s.image, s.build)
			dir, _ := filepath.Abs(s.build)
			out, err := exec.Command("docker", "build", "-t", s.image, dir).CombinedOutput()
			if err != nil {
				t.Fatalf("docker build %s: %v\n%s", s.image, err, tail(out))
			}
		}
	}
	// A fixed host port, not an ephemeral one: docker may hand out a new
	// ephemeral port on restart, and the driver must reconnect to the
	// endpoint it was configured with.
	c := &container{name: fmt.Sprintf("nautilus-matrix-%s-%d", s.name, os.Getpid()), port: freePort(t)}
	c.docker(t, "rm", "-f", c.name)
	c.docker(t, "run", "-d", "--name", c.name, "-p", fmt.Sprintf("127.0.0.1:%d:4840", c.port), s.image)
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := exec.Command("docker", "logs", "--tail", "40", c.name).CombinedOutput()
			t.Logf("%s logs:\n%s", c.name, out)
		}
		_ = exec.Command("docker", "rm", "-f", c.name).Run()
	})
	return c
}

func (c *container) docker(t *testing.T, args ...string) {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil && args[0] != "rm" {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, tail(out))
	}
}

func (c *container) exec(t *testing.T, cmd ...string) {
	t.Helper()
	c.docker(t, append([]string{"exec", c.name}, cmd...)...)
}

func tail(b []byte) string {
	if len(b) > 2000 {
		b = b[len(b)-2000:]
	}
	return string(b)
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// waitServing waits for the server to answer GetEndpoints, then for a
// secured session — running the server's trust step when it refuses the
// first one.
func waitServing(t *testing.T, s matrixServer, c *container, url string, conn opcua.Connect) {
	t.Helper()
	deadline := time.Now().Add(s.startup)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := gopcua.GetEndpoints(ctx, url)
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not answer GetEndpoints within %v: %v", s.name, s.startup, err)
		}
		time.Sleep(250 * time.Millisecond)
	}
	dial := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		sess, err := opcua.Dial(ctx, url, conn)
		if err != nil {
			return err
		}
		t.Logf("%s: session on %s / %s", s.name, sess.Endpoint.SecurityPolicyURI, sess.Endpoint.SecurityMode)
		sess.Close()
		return nil
	}
	err := dial()
	if err == nil {
		return
	}
	if s.trust == nil {
		t.Fatalf("%s: secured session refused: %v", s.name, err)
	}
	t.Logf("%s refused the first secured session (%v); trusting the client certificate on the server", s.name, err)
	if !strings.Contains(err.Error(), "trust") {
		t.Errorf("the refusal does not tell the user to trust the client certificate: %v", err)
	}
	s.trust(t, c)
	deadline = time.Now().Add(20 * time.Second)
	for {
		if err = dial(); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: secured session still refused after trusting: %v", s.name, errors.Unwrap(err))
		}
		time.Sleep(500 * time.Millisecond)
	}
}
