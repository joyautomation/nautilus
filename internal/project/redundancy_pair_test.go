package project

import (
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// pairFS is a one-program project with the given redundancy: section.
func pairFS(red string) fstest.MapFS {
	return fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte("name: pair\ntasks:\n  - program: main.st\n    scan: 100ms\n" + red)},
		"main.st":       &fstest.MapFile{Data: []byte("PROGRAM Main\nVAR n : INT; END_VAR\nn := n + 1;\nEND_PROGRAM\n")},
	}
}

const pairOK = "redundancy:\n  experimental: true\n  peers:\n    a: ctl-a:9450\n    b: ctl-b:9450\n  witness: wit:9460\n"

// A pair's section (design §2) loads with its defaults, and the Kubernetes
// forms keep their meaning: lease: is a name there, a time with peers:.
func TestPairSectionLoads(t *testing.T) {
	p, err := Load(pairFS(pairOK), "")
	if err != nil {
		t.Fatal(err)
	}
	r := p.Redundancy
	if !r.Pair() || r.Peers["a"] != "ctl-a:9450" || r.Peers["b"] != "ctl-b:9450" || r.Witness != "wit:9460" {
		t.Fatalf("pair section: %+v", r)
	}
	if r.LeaseTime() != DefaultPairLease {
		t.Errorf("default lease %v, want %v", r.LeaseTime(), DefaultPairLease)
	}
	p, err = Load(pairFS(pairOK+"  lease: 750ms\n  state-dir: /srv/naut\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Redundancy.LeaseTime() != 750*time.Millisecond || p.Redundancy.StateDir != "/srv/naut" {
		t.Errorf("lease %v state-dir %q", p.Redundancy.LeaseTime(), p.Redundancy.StateDir)
	}
	for _, k8s := range []string{"redundancy: {}\n", "redundancy:\n  lease: plant-a\n"} {
		p, err := Load(pairFS(k8s), "")
		if err != nil {
			t.Fatalf("%q: %v", k8s, err)
		}
		if p.Redundancy == nil || p.Redundancy.Pair() {
			t.Errorf("%q: want the Kubernetes Lease mode, got %+v", k8s, p.Redundancy)
		}
	}
	p, err = Load(pairFS(pairOK+"  public-urls: {a: https://plc-a.lan:8443/, b: https://plc-b.lan}\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Redundancy.LeaderURLs(); got["a"] != "https://plc-a.lan:8443" || got["b"] != "https://plc-b.lan" {
		t.Errorf("public-urls: %v", got)
	}
	if p, _ := Load(pairFS(""), ""); p.Redundancy.Pair() {
		t.Error("no redundancy: section is not a pair")
	}
	// peer-tls (#710): YAML 1.1 would read an unquoted off as false; the
	// field takes it as written.
	for _, v := range []string{"off", `"off"`, "on"} {
		p, err = Load(pairFS(pairOK+"  peer-tls: "+v+"\n"), "")
		if err != nil {
			t.Fatalf("peer-tls: %s: %v", v, err)
		}
		if want := strings.Trim(v, `"`); p.Redundancy.PeerTLS != want {
			t.Errorf("peer-tls: %s read as %q", v, p.Redundancy.PeerTLS)
		}
	}
}

// Each malformed pair section is refused at load, naming what to fix:
// misconfiguration is never resolved quietly (design §2).
func TestPairSectionRefusals(t *testing.T) {
	for _, c := range []struct{ name, red, want string }{
		{"a lease name with peers", pairOK + "  lease: plant-a\n", "not a name"},
		{"a lease too short", pairOK + "  lease: 10ms\n", "want between"},
		{"a lease too long", pairOK + "  lease: 1h\n", "want between"},
		{"no witness", "redundancy:\n  experimental: true\n  peers:\n    a: ctl-a:9450\n    b: ctl-b:9450\n", "witness is required"},
		{"one peer", "redundancy:\n  experimental: true\n  peers:\n    a: ctl-a:9450\n  witness: wit:9460\n", "peers.b"},
		{"a third name", "redundancy:\n  experimental: true\n  peers:\n    a: ctl-a:9450\n    b: ctl-b:9450\n    c: ctl-c:9450\n  witness: wit:9460\n", `"c" is not a controller`},
		{"one address twice", "redundancy:\n  experimental: true\n  peers:\n    a: ctl:9450\n    b: ctl:9450\n  witness: wit:9460\n", "both ctl:9450"},
		{"no port", "redundancy:\n  experimental: true\n  peers:\n    a: ctl-a\n    b: ctl-b:9450\n  witness: wit:9460\n", "want host:port"},
		{"port out of range", "redundancy:\n  experimental: true\n  peers:\n    a: ctl-a:70000\n    b: ctl-b:9450\n  witness: wit:9460\n", "1 to 65535"},
		{"bad witness", "redundancy:\n  experimental: true\n  peers:\n    a: ctl-a:9450\n    b: ctl-b:9450\n  witness: wit\n", "redundancy.witness"},
		{"relative state dir", pairOK + "  state-dir: state\n", "absolute path"},
		{"witness without peers", "redundancy:\n  witness: wit:9460\n", "needs peers"},
		{"state dir without peers", "redundancy:\n  state-dir: /srv/naut\n", "needs peers"},
		{"a pair without the opt-in", "redundancy:\n  peers:\n    a: ctl-a:9450\n    b: ctl-b:9450\n  witness: wit:9460\n", "a takeover goes on hot"},
		{"a negative hot-max-age", pairOK + "  hot-max-age: -1s\n", "want 0"},
		{"a hot-max-age that is not a time", pairOK + "  hot-max-age: soon\n", "bad duration"},
		{"a hot-max-age without peers", "redundancy:\n  hot-max-age: 2s\n", "hot-max-age applies to a redundant pair"},
		{"a stream budget that is not a rate", pairOK + "  stream-budget: fast\n", "redundancy.stream-budget"},
		{"a stream budget too small", pairOK + "  stream-budget: 1kB/s\n", "at least 10kB/s"},
		{"a stream budget without peers", "redundancy:\n  stream-budget: 1MB/s\n", "applies to a redundant pair"},
		{"the opt-in without peers", "redundancy:\n  experimental: true\n", "applies to a redundant pair"},
		{"empty peers", "redundancy:\n  peers: {}\n  witness: wit:9460\n", "peers is empty"},
		{"a fence margin of a quarter lease", pairOK + "  lease: 2s\n  net-delay: 480ms\n  send-margin: 20ms\n", "under a quarter of the lease"},
		{"a net delay alone too long for a short lease", pairOK + "  lease: 200ms\n  net-delay: 40ms\n", "under a quarter of the lease"},
		{"a negative send margin", pairOK + "  send-margin: -5ms\n", "zero or more"},
		{"a fence margin without peers", "redundancy:\n  net-delay: 50ms\n", "apply to a redundant pair"},
		{"public-urls without peers", "redundancy:\n  public-urls: {a: https://plc-a.lan}\n", "public-urls applies to a redundant pair"},
		{"a public URL for a third node", pairOK + "  public-urls: {c: https://plc-c.lan}\n", `"c" is not a controller`},
		{"a public URL with a path", pairOK + "  public-urls: {a: https://plc-a.lan/hmi}\n", "no path"},
		{"a public URL with no scheme", pairOK + "  public-urls: {a: plc-a.lan:8443}\n", "no path"},
		{"a public URL with credentials", pairOK + "  public-urls: {a: https://u:p@plc-a.lan}\n", "no path"},
		{"peer-tls neither on nor off", pairOK + "  peer-tls: maybe\n", "want on (the default) or off"},
		{"peer-tls without peers", "redundancy:\n  peer-tls: off\n", "peer-tls applies to a redundant pair"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := Load(pairFS(c.red), "")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want an error containing %q", err, c.want)
			}
		})
	}
}

// The fence margin's parts (design §3.4) load when set under a quarter of
// the lease; unset, they stay zero (the elector scales its defaults).
func TestPairFenceMarginLoads(t *testing.T) {
	p, err := Load(pairFS(pairOK+"  lease: 2s\n  net-delay: 100ms\n  send-margin: 30ms\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	if r := p.Redundancy; time.Duration(r.NetDelay) != 100*time.Millisecond || time.Duration(r.SendMargin) != 30*time.Millisecond {
		t.Fatalf("net-delay %v send-margin %v", time.Duration(r.NetDelay), time.Duration(r.SendMargin))
	}
	if p, err := Load(pairFS(pairOK+"  lease: 200ms\n"), ""); err != nil || p.Redundancy.NetDelay != 0 {
		t.Fatalf("a short lease with the margin unset: %v", err)
	}
}

// #798: a pair can't run a Sparkplug edge node yet (both members would
// publish as the same edge node), so a pair with sparkplug: is refused at
// load, naming the issue. The Kubernetes Lease mode is left as it was.
func TestPairRefusesASparkplugEdge(t *testing.T) {
	edge := "sparkplug:\n  broker: tcp://broker:1883\n  group-id: plant\n"
	_, err := Load(pairFS(edge+pairOK), "")
	if err == nil || !strings.Contains(err.Error(), "#798") {
		t.Fatalf("a pair with a Sparkplug edge: %v; want refused naming #798", err)
	}
	if _, err := Load(pairFS(edge+"redundancy:\n  lease: plant-a\n"), ""); err != nil {
		t.Fatalf("the Kubernetes Lease mode with a Sparkplug edge: %v; want it loaded as before", err)
	}
}

// The least budget a pair's write gets in normal running: three quarters
// of the lease (it renews every quarter) minus the net delay.
func TestPairSendBudget(t *testing.T) {
	for _, c := range []struct {
		red  string
		want time.Duration
	}{
		{pairOK, 700 * time.Millisecond}, // the default 1 s lease
		{pairOK + "  lease: 2s\n", 1450 * time.Millisecond},
		{pairOK + "  lease: 200ms\n", 125 * time.Millisecond},
		{pairOK + "  lease: 4s\n  net-delay: 100ms\n", 2900 * time.Millisecond},
	} {
		p, err := Load(pairFS(c.red), "")
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Redundancy.SendBudget(); got != c.want {
			t.Errorf("%q: budget %s, want %s", c.red, got, c.want)
		}
	}
}

// #1040 re-review: a pair changes no source's request timeout (it drives
// polls too: a BMC with 2 s GETs must not fail every poll); each write is
// cut at its own send deadline instead. WriteTimeouts reports only a
// timeout the manifest sets, which is what naut check compares.
func TestPairKeepsTimeoutsAndReportsOnlyExplicitOnes(t *testing.T) {
	fs := pairFS(pairOK + "driver:\n  type: modbus\n  manifest: modbus_manifest.yaml\n")
	fs["modbus_manifest.yaml"] = &fstest.MapFile{Data: []byte("sources:\n" +
		"  - {id: W, host: 192.0.2.1}\n" +
		"  - {id: X, host: 192.0.2.2, timeout: 3s}\n" +
		"tags:\n" +
		"  - {name: SP, source: W, table: holding, address: 0, format: uint16, writable: true}\n" +
		"  - {name: SP2, source: X, table: holding, address: 0, format: uint16, writable: true}\n")}
	p, err := Load(fs, "")
	if err != nil {
		t.Fatal(err)
	}
	d := p.Runtime.Driver.(interface {
		RequestTimeout(string) time.Duration
		WriteTimeouts() map[string]time.Duration
	})
	if got := d.RequestTimeout("W"); got != 3*time.Second {
		t.Errorf("pair, writing source with the default: %s; want the 3 s default unchanged", got)
	}
	if wt := d.WriteTimeouts(); len(wt) != 1 || wt["X"] != 3*time.Second {
		t.Errorf("WriteTimeouts %v; want only X's explicit 3s", wt)
	}
}

// A pair's controllers are two OPC UA servers: each member's ApplicationUri
// carries its name, and ServerUriArray lists both (#716). Outside a pair the
// configured URI is untouched.
func TestOPCUAApplicationURIPerMember(t *testing.T) {
	p, err := Load(pairFS(pairOK+"opcua:\n  endpoint: 0.0.0.0:4840\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	a, b := p.OPCUAApplicationURI("a"), p.OPCUAApplicationURI("b")
	if a == b || !strings.HasSuffix(a, "-a") || !strings.HasSuffix(b, "-b") {
		t.Errorf("member URIs: %q %q", a, b)
	}
	if got := p.OPCUAMemberURIs(); len(got) != 2 || got[0] != a || got[1] != b {
		t.Errorf("OPCUAMemberURIs = %v", got)
	}
	p.OPCUA.ApplicationURI = "urn:plant:plc?v=1#frag"
	if got := p.OPCUAApplicationURI("b"); got != "urn:plant:plc-b?v=1#frag" {
		t.Errorf("query and fragment: %q", got)
	}
	p.OPCUA.ApplicationURI = ""
	if got := p.OPCUAApplicationURI(""); got != "" {
		t.Errorf("no member: %q, want the configured (empty) URI", got)
	}
}

// redundancy.floating-ip: a pair's, parsed at load, and refused where it
// would be ignored (#716).
func TestFloatingIPConfig(t *testing.T) {
	p, err := Load(pairFS(pairOK+"  floating-ip: {address: 10.0.0.50/24, interface: eth0}\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	if f := p.Redundancy.FloatingIP; f == nil || f.Address != "10.0.0.50/24" || f.Interface != "eth0" {
		t.Errorf("FloatingIP = %+v", f)
	}
	for _, c := range []struct{ name, extra, want string }{
		{"no prefix length", "  floating-ip: {address: 10.0.0.50, interface: eth0}\n", "prefix length"},
		{"IPv6", "  floating-ip: {address: 'fe80::1/64', interface: eth0}\n", "IPv4"},
		{"no interface", "  floating-ip: {address: 10.0.0.50/24}\n", "interface"},
		{"a peer's own address", "  floating-ip: {address: 10.0.0.50/24, interface: eth0}\n", "never the floating one"},
	} {
		src := pairOK + c.extra
		if c.name == "a peer's own address" {
			src = strings.Replace(pairOK, "ctl-a:9450", "10.0.0.50:9450", 1) + c.extra
		}
		if _, err := Load(pairFS(src), ""); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	if _, err := Load(pairFS("redundancy:\n  floating-ip: {address: 10.0.0.50/24, interface: eth0}\n"), ""); err == nil || !strings.Contains(err.Error(), "applies to a redundant pair") {
		t.Errorf("floating-ip without peers: %v", err)
	}
}

// The state stream's budget (#710 S1b): bytes a second, plain or with a
// unit; 1 MB/s when unset.
func TestStreamBudget(t *testing.T) {
	for in, want := range map[string]int64{"": DefaultStreamBudget, "2000000": 2_000_000, "500kB/s": 500_000, "2MB/s": 2_000_000, "1MiB/s": 1 << 20, "64 KiB": 64 << 10, "1.5MB": 1_500_000} {
		r := &RedundancyConfig{StreamBudget: in}
		if got := r.StreamBudgetBytes(); got != want {
			t.Errorf("stream-budget %q = %d, want %d", in, got, want)
		}
	}
	if got := (*RedundancyConfig)(nil).StreamBudgetBytes(); got != DefaultStreamBudget {
		t.Errorf("no section: %d, want the default", got)
	}
}

// redundancy.hot-max-age: unset is the caller's default, 0 is 0 (every
// takeover cold), a time is that time (#710 S2).
func TestHotMaxAge(t *testing.T) {
	if got := (*RedundancyConfig)(nil).HotMaxAgeOr(-1); got != -1 {
		t.Errorf("nil config: %v, want the default", got)
	}
	for in, want := range map[string]time.Duration{"": -1, "  hot-max-age: 0s\n": 0, "  hot-max-age: 4s\n": 4 * time.Second} {
		p, err := Load(pairFS(pairOK+in), "")
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got := p.Redundancy.HotMaxAgeOr(-1); got != want {
			t.Errorf("%q: hot-max-age %v, want %v", in, got, want)
		}
	}
}
