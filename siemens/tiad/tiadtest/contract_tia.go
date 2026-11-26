package tiadtest

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// The TIA half of the contract: GSDML install, projects and sessions,
// station, software, compile, the gates, and the order in which the
// agent's checks apply.

const (
	benchCPU   = "192.0.2.10"
	benchGSDML = "GSDML-V2.4-Example-Device-20261117.xml"
)

func intp(i int) *int { return &i }

// BenchStation is a valid station: a 1510SP with one local module and one
// GSDML device. Addresses are from the documentation range.
func BenchStation(modules ...tiad.DeviceModule) tiad.Station {
	return tiad.Station{
		CPU: tiad.CPU{Name: "PLC_1", Order: "6ES7 510-1DK03-0AB0", Firmware: "V3.1",
			Address: benchCPU, SubnetMask: "255.255.255.0", ProfinetName: "plc-1",
			// The bench CPU's own setting (the S2a upload): a download of
			// the bench station leaves its protection as it is.
			ConfigurationProtection: tiad.ProtectNone,
			// A station naut makes has access control on, with its Full
			// access user (#789).
			AccessControl: tiad.AccessFullAccessUser},
		WebServer: &tiad.WebServer{Enabled: true, Users: []tiad.WebUser{{Name: "naut", Rights: []string{"read", "write", "mode"}}}},
		Modules:   []tiad.Module{{Slot: 2, Order: "6ES7 131-6BH01-0BA0", InputStart: intp(0)}},
		Profinet: &tiad.Profinet{UpdateTime: "16ms", Devices: []tiad.Device{{
			GSDML: benchGSDML, DAP: "DAP_1", Name: "io-device-1", Address: "192.0.2.20", Modules: modules,
		}}},
	}
}

// BenchWebPassword is the bench station's web user's password: it is set,
// so it meets TIA's password rules, and it is its own secret.
const BenchWebPassword = "Bench-web-2026"

// BenchSecrets carries the bench station's web user password.
var BenchSecrets = tiad.Secrets{WebUsers: map[string]string{"naut": BenchWebPassword}, FullAccessPassword: BenchFullAccessPassword}

// BenchAttestation is a test operator's attestation for a download that
// loads hardware (#789 hardware gate).
var BenchAttestation = tiad.Attestation{By: "bench operator", Text: "the bench CPU's hardware configuration is what I intend to load"}

// AcceptHardware returns r with the opt-in and BenchAttestation that
// every real download that loads hardware needs.
func AcceptHardware(r tiad.DownloadRequest) tiad.DownloadRequest {
	r.Decisions.AcceptUnverifiedHardware = tiad.Yes()
	a := BenchAttestation
	r.Attestation = &a
	return r
}

// BenchFullAccessPassword meets TIA's password rules. It is a test value,
// never a real CPU's.
const BenchFullAccessPassword = "Bench-access-2026"

// BenchLogin answers TIA's credentials question going online to a CPU a
// bench station's hardware load turned access control on for: its user
// "nautilus" holds BenchFullAccessPassword (#789 stage 5 (A)).
var BenchLogin = &tiad.OnlineSecrets{FullAccessPassword: BenchFullAccessPassword}

// benchLoginJSON is BenchLogin as a raw request's secrets.
var benchLoginJSON = map[string]string{"fullAccessPassword": BenchFullAccessPassword}

// BenchCompare is a compare with BenchLogin.
var BenchCompare = tiad.CompareRequest{Secrets: BenchLogin}

// BenchDownloadSecrets are the secrets a download to a bench CPU carries
// when it names none of its own: the Full access password, so the
// download and the compare after it can log on.
func BenchDownloadSecrets() *tiad.Secrets {
	return &tiad.Secrets{FullAccessPassword: BenchFullAccessPassword}
}

// BenchConfigurationPassword meets TIA's password rules. It is a test
// value, never a real CPU's.
const BenchConfigurationPassword = "Bench-config-2026"

// StationCases break one station rule each. Want is part of the message
// both copies of the rule (Go and C#) must give, word for word.
var StationCases = []struct {
	Name string
	Edit func(*tiad.Station)
	Want string
}{
	{"cpu name", func(s *tiad.Station) { s.CPU.Name = "" }, "cpu.name is empty"},
	{"cpu order", func(s *tiad.Station) { s.CPU.Order = "" }, "cpu.order is empty"},
	{"cpu firmware", func(s *tiad.Station) { s.CPU.Firmware = "" }, "cpu.firmware is empty"},
	{"cpu address", func(s *tiad.Station) { s.CPU.Address = "plc.local" }, `cpu.address "plc.local" is not an IPv4 address`},
	{"cpu ipv6", func(s *tiad.Station) { s.CPU.Address = "::1" }, `cpu.address "::1" is not an IPv4 address`},
	{"cpu address leading zero", func(s *tiad.Station) { s.CPU.Address = "192.0.2.010" }, "cpu.address"},
	{"cpu mask", func(s *tiad.Station) { s.CPU.SubnetMask = "255.255.255" }, `cpu.subnetMask "255.255.255" is not an IPv4 mask`},
	{"cpu router", func(s *tiad.Station) { s.CPU.Router = "gw" }, `cpu.router "gw" is not an IPv4 address`},
	{"cpu configuration protection empty", func(s *tiad.Station) { s.CPU.ConfigurationProtection = "" }, "cpu.configurationProtection is empty (one of password, none, keep)"},
	{"cpu configuration protection unknown", func(s *tiad.Station) { s.CPU.ConfigurationProtection = "Password" }, `cpu.configurationProtection: "Password" is not one of password, none, keep`},
	{"cpu access control empty", func(s *tiad.Station) { s.CPU.AccessControl = "" }, "cpu.accessControl is empty (one of fullAccessUser, keep)"},
	// The manifest's spelling is not the wire's.
	{"cpu access control unknown", func(s *tiad.Station) { s.CPU.AccessControl = "full-access-user" }, `cpu.accessControl: "full-access-user" is not one of fullAccessUser, keep`},
	{"cpu station name upper", func(s *tiad.Station) { s.CPU.ProfinetName = "PLC_1" }, `cpu.profinetName: "PLC_1": only lowercase letters, digits, hyphens and dots are allowed`},
	{"device name empty", func(s *tiad.Station) { s.Profinet.Devices[0].Name = "" }, "profinet.devices[0].name: empty (names are always explicit)"},
	{"device name ip-shaped", func(s *tiad.Station) { s.Profinet.Devices[0].Name = "1.2.3.4" }, `"1.2.3.4" has the form n.n.n.n (n = 0-999), which PROFINET forbids`},
	{"device name n.n.n.n beyond 255", func(s *tiad.Station) { s.Profinet.Devices[0].Name = "300.1.1.1" }, "n.n.n.n"},
	{"device name n.n.n.n leading zero", func(s *tiad.Station) { s.Profinet.Devices[0].Name = "01.2.3.4" }, "n.n.n.n"},
	{"device name port-NNN", func(s *tiad.Station) { s.Profinet.Devices[0].Name = "port-001" }, `"port-001": a first label of the form port-xyz or port-xyz-abcde is reserved by PROFINET`},
	{"device name port-xyz-abcde", func(s *tiad.Station) { s.Profinet.Devices[0].Name = "port-001-00002.x" }, "port-xyz"},
	{"device name hyphen", func(s *tiad.Station) { s.Profinet.Devices[0].Name = "-dev" }, `"-dev" has a label starting or ending with a hyphen`},
	{"device name empty label", func(s *tiad.Station) { s.Profinet.Devices[0].Name = "a..b" }, `"a..b" has a label that is empty or longer than 63 characters`},
	{"device name long label", func(s *tiad.Station) { s.Profinet.Devices[0].Name = strings.Repeat("a", 64) }, "longer than 63 characters"},
	{"device name too long", func(s *tiad.Station) {
		s.Profinet.Devices[0].Name = strings.Repeat(strings.Repeat("a", 60)+".", 4) + "a"
	}, "is longer than 240 characters"},
	{"device name unicode digit", func(s *tiad.Station) { s.Profinet.Devices[0].Name = "dev١" }, `"dev١": only lowercase letters`},
	// The quoting is Go's %q, escapes and all.
	{"device name tab", func(s *tiad.Station) { s.Profinet.Devices[0].Name = "dev\tice" }, `"dev\tice": only lowercase letters`},
	{"device name newline", func(s *tiad.Station) { s.Profinet.Devices[0].Name = "dev\nice" }, `"dev\nice": only lowercase letters`},
	{"device name bell", func(s *tiad.Station) { s.Profinet.Devices[0].Name = "dev\aice" }, `"dev\aice": only lowercase letters`},
	{"device name unit separator", func(s *tiad.Station) { s.Profinet.Devices[0].Name = "dev\x1fice" }, `"dev\x1fice": only lowercase letters`},
	{"device name soft hyphen", func(s *tiad.Station) { s.Profinet.Devices[0].Name = "dev\u00adice" }, `"dev\u00adice": only lowercase letters`},
	{"device name quote", func(s *tiad.Station) { s.Profinet.Devices[0].Name = `dev"ice` }, `"dev\"ice": only lowercase letters`},
	// A character both Unicode tables know (6.1). Go's tables are newer
	// than .NET Framework's (and Mono's older still), so a character
	// assigned later prints raw from Go and escaped from the agent: only
	// the quoting of an already refused name differs.
	{"device name emoji", func(s *tiad.Station) { s.Profinet.Devices[0].Name = "dev😀" }, `"dev😀": only lowercase letters`},
	{"device name private use", func(s *tiad.Station) { s.Profinet.Devices[0].Name = "dev\ue000" }, `"dev\ue000": only lowercase letters`},
	{"device name dup of cpu", func(s *tiad.Station) { s.Profinet.Devices[0].Name = "plc-1" }, `profinet.devices[0].name "plc-1" is already used in this station`},
	{"device address dup of cpu", func(s *tiad.Station) { s.Profinet.Devices[0].Address = benchCPU }, "profinet.devices[0].address 192.0.2.10 is already used in this station"},
	{"device address", func(s *tiad.Station) { s.Profinet.Devices[0].Address = "1.2" }, `profinet.devices[0].address "1.2" is not an IPv4 address`},
	{"gsdml empty", func(s *tiad.Station) { s.Profinet.Devices[0].GSDML = "" }, "profinet.devices[0].gsdml is empty"},
	{"dap empty", func(s *tiad.Station) { s.Profinet.Devices[0].DAP = "" }, "profinet.devices[0].dap is empty"},
	{"subslot zero", func(s *tiad.Station) {
		s.Profinet.Devices[0].Modules = []tiad.DeviceModule{{Slot: 1, Subslot: 0, Module: "M"}}
	}, "profinet.devices[0].modules[0]: slot 1 subslot 0 is out of range"},
	{"subslot twice", func(s *tiad.Station) {
		s.Profinet.Devices[0].Modules = []tiad.DeviceModule{{Slot: 1, Subslot: 1, Module: "M"}, {Slot: 1, Subslot: 1, Module: "N"}}
	}, "profinet.devices[0].modules[1]: slot 1 subslot 1 twice"},
	{"module id empty", func(s *tiad.Station) {
		s.Profinet.Devices[0].Modules = []tiad.DeviceModule{{Slot: 1, Subslot: 1}}
	}, "profinet.devices[0].modules[0].module is empty"},
	{"device output start negative", func(s *tiad.Station) {
		s.Profinet.Devices[0].Modules = []tiad.DeviceModule{{Slot: 1, Subslot: 1, Module: "M", OutputStart: intp(-2)}}
	}, "profinet.devices[0].modules[0].outputStart -2 is negative"},
	{"local slot twice", func(s *tiad.Station) { s.Modules = append(s.Modules, s.Modules[0]) }, "modules: slot 2 twice"},
	{"local slot 1 is the cpu", func(s *tiad.Station) { s.Modules[0].Slot = 1 }, "modules[0].slot 1: slot 1 is the CPU's own; modules go in slot 2 and up"},
	{"local slot negative", func(s *tiad.Station) { s.Modules[0].Slot = -1 }, "modules[0].slot -1"},
	{"local order empty", func(s *tiad.Station) { s.Modules[0].Order = "" }, "modules[0].order is empty"},
	{"negative start", func(s *tiad.Station) { s.Modules[0].InputStart = intp(-1) }, "modules[0].inputStart -1 is negative"},
	{"unknown right", func(s *tiad.Station) { s.WebServer.Users[0].Rights = []string{"admin"} }, `webServer.users[0]: unknown right "admin" (one of read, write, mode, diagnostics)`},
	{"web user unnamed", func(s *tiad.Station) { s.WebServer.Users[0].Name = "" }, "webServer.users[0].name is empty"},
	{"web user twice", func(s *tiad.Station) {
		s.WebServer.Users = append(s.WebServer.Users, s.WebServer.Users[0])
	}, `webServer.users: "naut" twice`},
	// Update times: Go's time.ParseDuration grammar, and its String in
	// the message.
	{"update time too short", func(s *tiad.Station) { s.Profinet.Devices[0].UpdateTime = "8ms" }, "profinet.devices[0].updateTime: 8ms is below the 16ms minimum"},
	{"update time just short", func(s *tiad.Station) { s.Profinet.UpdateTime = "15.999ms" }, "profinet.updateTime: 15.999ms is below the 16ms minimum"},
	{"update time micro", func(s *tiad.Station) { s.Profinet.UpdateTime = "999µs" }, "999µs is below the 16ms minimum"},
	{"update time negative", func(s *tiad.Station) { s.Profinet.UpdateTime = "-16ms" }, "-16ms is below the 16ms minimum"},
	{"update time zero", func(s *tiad.Station) { s.Profinet.UpdateTime = "0" }, "0s is below the 16ms minimum"},
	{"update time junk", func(s *tiad.Station) { s.Profinet.UpdateTime = "fast" }, `profinet.updateTime: "fast" is not a duration`},
	{"update time no unit", func(s *tiad.Station) { s.Profinet.UpdateTime = "16" }, `"16" is not a duration`},
	{"update time space", func(s *tiad.Station) { s.Profinet.UpdateTime = "16 ms" }, `"16 ms" is not a duration`},
	{"update time upper", func(s *tiad.Station) { s.Profinet.UpdateTime = "16MS" }, `"16MS" is not a duration`},
	{"update time exponent", func(s *tiad.Station) { s.Profinet.UpdateTime = "1e3ms" }, `"1e3ms" is not a duration`},
	{"update time bare point", func(s *tiad.Station) { s.Profinet.UpdateTime = ".s" }, `".s" is not a duration`},
	{"update time two points", func(s *tiad.Station) { s.Profinet.UpdateTime = "1.5.2s" }, `"1.5.2s" is not a duration`},
	{"update time sign only", func(s *tiad.Station) { s.Profinet.UpdateTime = "+" }, `"+" is not a duration`},
	{"update time overflow", func(s *tiad.Station) { s.Profinet.UpdateTime = "9999999999h" }, "is not a duration"},
	// 5124096h is past the largest duration, and a product that wraps
	// would come out near 25 minutes.
	{"update time wraps", func(s *tiad.Station) { s.Profinet.UpdateTime = "5124096h" }, `"5124096h" is not a duration`},
}

// UpdateTimesAccepted are update times both copies of the rule take.
var UpdateTimesAccepted = []string{
	"16ms", "0.016s", "16000us", "16000µs", "16000μs", ".5s", "1m", "+16ms",
	"1h2m3s", "16.0ms", "16.ms", "16000000ns", "1h", "2562047h",
}

func contractTIA(t *testing.T, start Starter) {
	t.Run("GSDML", func(t *testing.T) { contractGSDML(t, start) })
	t.Run("Projects", func(t *testing.T) { contractProjects(t, start) })
	t.Run("ProjectNames", func(t *testing.T) { contractProjectNames(t, start) })
	t.Run("StationRules", func(t *testing.T) { contractStationRules(t, start) })
	t.Run("StrictJSON", func(t *testing.T) { contractStrictJSON(t, start) })
	t.Run("Sessions", func(t *testing.T) { contractSessions(t, start) })
	t.Run("StationChanges", func(t *testing.T) { contractStationChanges(t, start) })
	t.Run("Software", func(t *testing.T) { contractSoftware(t, start) })
	t.Run("SoftwareOwnership", func(t *testing.T) { contractSoftwareOwnership(t, start) })
	t.Run("SoftwareReplacedForeign", func(t *testing.T) { contractSoftwareReplacedForeign(t, start) })
	t.Run("SoftwareDataTypes", func(t *testing.T) { contractSoftwareDataTypes(t, start) })
	t.Run("SoftwareDataTypeClash", func(t *testing.T) { contractSoftwareDataTypeClash(t, start) })
	t.Run("SoftwareDataTypeOutsideGroup", func(t *testing.T) { contractSoftwareDataTypeOutsideGroup(t, start) })
	t.Run("CompileTouchesForeign", func(t *testing.T) { contractCompileTouchesForeign(t, start) })
	t.Run("ImportTouchesForeignType", func(t *testing.T) { contractImportTouchesForeignType(t, start) })
	t.Run("SoftwarePrunePrefix", func(t *testing.T) { contractSoftwarePrunePrefix(t, start) })
	t.Run("SoftwareCyclicOBs", func(t *testing.T) { contractSoftwareCyclicOBs(t, start) })
	t.Run("SoftwareCyclicOBNotAsAsked", func(t *testing.T) { contractSoftwareCyclicOBNotAsAsked(t, start) })
	t.Run("Compile", func(t *testing.T) { contractCompile(t, start) })
	t.Run("Gates", func(t *testing.T) { contractGates(t, start) })
	t.Run("Precedence", func(t *testing.T) { contractPrecedence(t, start) })
}

// rawJSON sends a JSON body without the client's own checks.
func rawJSON(t *testing.T, c *tiad.Client, method, path string, body any) (int, []byte) {
	t.Helper()
	var b []byte
	switch v := body.(type) {
	case string:
		b = []byte(v)
	default:
		var err error
		if b, err = json.Marshal(v); err != nil {
			t.Fatal(err)
		}
	}
	h := http.Header{"Authorization": {"Bearer " + c.Token}, "Content-Type": {"application/json"}}
	return rawWith(t, c, method, path, h, b)
}

func errMessage(b []byte) string {
	var env struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(b, &env)
	return env.Error.Message
}

// installGSD puts and installs the bench GSDML.
func installGSD(t *testing.T, c *tiad.Client) {
	t.Helper()
	if _, err := c.PutFile(ctx, "gsd/"+benchGSDML, []byte("<ISO15745Profile/>")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.InstallGSD(ctx, "gsd/"+benchGSDML); err != nil {
		t.Fatal(err)
	}
}

func contractGSDML(t *testing.T, start Starter) {
	c := start(t, Config{}).Client()
	if _, err := c.PutFile(ctx, "gsd/"+benchGSDML, []byte("<ISO15745Profile/>")); err != nil {
		t.Fatal(err)
	}
	if r, err := c.InstallGSD(ctx, "gsd/"+benchGSDML); err != nil || r.AlreadyInstalled || r.Path != "gsd/"+benchGSDML {
		t.Fatalf("install: %+v %v", r, err)
	}
	if r, err := c.InstallGSD(ctx, "gsd/"+benchGSDML); err != nil || !r.AlreadyInstalled {
		t.Fatalf("second install: %+v %v", r, err)
	}
	if _, err := c.InstallGSD(ctx, "gsd/GSDML-missing.xml"); tiad.Kind(err) != tiad.KindNotFound {
		t.Fatalf("missing file: %v", err)
	}
	if _, err := c.PutFile(ctx, "gsd/device.xml", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.InstallGSD(ctx, "gsd/device.xml"); tiad.Kind(err) != tiad.KindBadRequest || !strings.Contains(err.Error(), "GSDML-*.xml") {
		t.Fatalf("a file not named GSDML-*.xml: %v", err)
	}
	// TIA's importer never sees a DOCTYPE (no XXE, no entity expansion),
	// or anything that is not well-formed XML.
	refused := map[string]string{
		"doctype":         `<?xml version="1.0"?><!DOCTYPE ISO15745Profile><ISO15745Profile/>`,
		"external entity": `<?xml version="1.0"?><!DOCTYPE x [<!ENTITY e SYSTEM "file:///etc/passwd">]><x>&e;</x>`,
		"entity bomb":     `<!DOCTYPE x [<!ENTITY a "aaaa"><!ENTITY b "&a;&a;&a;&a;">]><x>&b;</x>`,
		"not xml":         "x",
		"empty":           "",
		"unclosed":        "<ISO15745Profile>",
	}
	for name, body := range refused {
		p := "gsd/GSDML-V2.4-Bad-" + strings.ReplaceAll(name, " ", "-") + ".xml"
		if _, err := c.PutFile(ctx, p, []byte(body)); err != nil {
			t.Fatal(err)
		}
		if _, err := c.InstallGSD(ctx, p); tiad.Kind(err) != tiad.KindBadRequest {
			t.Errorf("GSDML %s: %v, want bad_request", name, err)
		}
	}
	ok := "gsd/GSDML-V2.4-Good-20261118.xml"
	if _, err := c.PutFile(ctx, ok, []byte("<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<!-- a comment -->\n<ISO15745Profile xmlns=\"http://www.profibus.com/GSDML/2003/11/DeviceProfile\"><a/></ISO15745Profile>\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.InstallGSD(ctx, ok); err != nil {
		t.Errorf("a well-formed GSDML: %v", err)
	}
	// The sandbox rule holds for a path in a body as in a URL, and there
	// every agent answers 400 itself.
	for _, p := range append(append([]string{}, RefusedPaths...), "../x", "a/../../x", "/abs/x") {
		status, b := rawJSON(t, c, "POST", "/v1/gsd/install", map[string]string{"path": p})
		if status != http.StatusBadRequest || envelopeKind(b) != tiad.KindBadRequest {
			t.Errorf("install %q = HTTP %d %s, want 400 bad_request", p, status, b)
		}
	}
}

func contractProjects(t *testing.T, start Starter) {
	c := start(t, Config{}).Client()
	if _, err := c.CreateProject(ctx, "bench", BenchStation(), BenchSecrets); tiad.Kind(err) != tiad.KindBadRequest || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("uninstalled GSDML: %v", err)
	}
	installGSD(t, c)
	if _, err := c.CreateProject(ctx, "bench", BenchStation(), tiad.Secrets{FullAccessPassword: BenchFullAccessPassword}); tiad.Kind(err) != tiad.KindBadRequest || !strings.Contains(err.Error(), "web user naut has no password") {
		t.Fatalf("missing web password: %v", err)
	}
	s, err := c.CreateProject(ctx, "bench", BenchStation(), BenchSecrets)
	if err != nil || s.Project != "projects/bench/bench.ap21" || s.ID == "" {
		t.Fatalf("create: %+v %v", s, err)
	}
	listed := false
	for _, f := range files(t, c) {
		listed = listed || f.Path == s.Project
	}
	if !listed {
		t.Fatalf("the project file is not in the work dir: %+v", files(t, c))
	}
	if h, err := c.Health(ctx); err != nil || h.Sessions != 1 {
		t.Fatalf("health with one session: %+v %v", h, err)
	}
	// One project, one writer, whatever the case of its name: the work
	// dir is on Windows.
	for _, name := range []string{"bench", "BENCH", "Bench"} {
		if _, err := c.CreateProject(ctx, name, BenchStation(), BenchSecrets); tiad.Kind(err) != tiad.KindBusy {
			t.Errorf("create %q while bench is open: %v, want busy", name, err)
		}
	}
	if _, err := c.Open(ctx, "projects/bench/bench.ap21"); tiad.Kind(err) != tiad.KindBusy {
		t.Fatalf("open while created: %v", err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	// A closed project is rebuilt from the spec: every deploy starts fresh.
	s2, err := c.CreateProject(ctx, "bench", BenchStation(), BenchSecrets)
	if err != nil || s2.ID == s.ID {
		t.Fatalf("re-create: %+v %v", s2, err)
	}

	// TIA opens one project at a time (#616): with bench open, a create of
	// an existing project that is not open is refused before anything is
	// touched, naming the open session, and so is an open; the project's
	// files stay, and it opens once bench is closed.
	if _, err := c.PutFile(ctx, "projects/foo/other.ap21", []byte("another project")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"foo", "FOO"} {
		status, b := rawJSON(t, c, "POST", "/v1/projects", map[string]any{"name": name, "station": BenchStation(), "secrets": BenchSecrets})
		var e struct {
			Data struct {
				OpenSession string `json:"openSession"`
				OpenProject string `json:"openProject"`
			} `json:"data"`
		}
		if status != http.StatusConflict || envelopeKind(b) != tiad.KindBusy || json.Unmarshal(b, &e) != nil ||
			e.Data.OpenSession != s2.ID || e.Data.OpenProject != "projects/bench/bench.ap21" ||
			!strings.Contains(errMessage(b), "TIA opens one project at a time, and session "+s2.ID+" has projects/bench/bench.ap21 open: close it first (DELETE /v1/sessions/"+s2.ID+"); nothing was touched") {
			t.Errorf("create %q while bench is open: HTTP %d %s, want 409 busy naming %s", name, status, b, s2.ID)
		}
	}
	if b, err := c.GetFile(ctx, "projects/foo/other.ap21"); err != nil || string(b) != "another project" {
		t.Fatalf("a refused create touched projects/foo/: %q %v", b, err)
	}
	if _, err := c.Open(ctx, "projects/foo/other.ap21"); tiad.Kind(err) != tiad.KindBusy || !strings.Contains(err.Error(), "session "+s2.ID) {
		t.Fatalf("open while bench is open: %v, want busy naming %s", err, s2.ID)
	}
	if err := s2.Close(ctx); err != nil {
		t.Fatal(err)
	}
	other, err := c.Open(ctx, "projects/foo/other.ap21")
	if err != nil {
		t.Fatalf("open once bench is closed: %v", err)
	}
	if err := other.Close(ctx); err != nil {
		t.Fatal(err)
	}
	foo, err := c.CreateProject(ctx, "foo", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatalf("create foo once nothing is open: %v", err)
	}
	_ = foo.Close(ctx)
}

func contractProjectNames(t *testing.T, start Starter) {
	c := start(t, Config{}).Client()
	installGSD(t, c)
	for _, n := range []string{"", "CON", "nul.txt", "com¹", "x.", "a\x01b", "..", ".", "a/b", "a/../b", `a\b`, "c:x", "a b ", "a|b"} {
		status, b := rawJSON(t, c, "POST", "/v1/projects", map[string]any{"name": n, "station": BenchStation(), "secrets": BenchSecrets})
		if status != http.StatusBadRequest || !strings.Contains(errMessage(b), "project name") {
			t.Errorf("project name %q = HTTP %d %s, want 400 naming the project name", n, status, b)
		}
	}
	for _, n := range []string{"bench-1", "Line 2", "ünï"} {
		s, err := c.CreateProject(ctx, n, BenchStation(), BenchSecrets)
		if err != nil {
			t.Errorf("project name %q: %v", n, err)
			continue
		}
		_ = s.Close(ctx)
	}
}

// stationBody is a station as the JSON the agent reads, for edits the
// typed Station cannot make.
func stationBody(t *testing.T, st tiad.Station) map[string]any {
	t.Helper()
	b, _ := json.Marshal(st)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func contractStationRules(t *testing.T, start Starter) {
	c := start(t, Config{}).Client()
	installGSD(t, c)
	s, err := c.CreateProject(ctx, "rules", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/sessions/" + s.ID + "/station"
	for _, sc := range StationCases {
		st := BenchStation()
		sc.Edit(&st)
		status, b := rawJSON(t, c, "PUT", path, map[string]any{"station": st, "secrets": BenchSecrets})
		if status != http.StatusBadRequest || !strings.Contains(errMessage(b), sc.Want) {
			t.Errorf("%s: HTTP %d %q, want 400 containing %q", sc.Name, status, errMessage(b), sc.Want)
		}
	}
	for _, ut := range UpdateTimesAccepted {
		st := BenchStation()
		st.Profinet.UpdateTime = ut
		if status, b := rawJSON(t, c, "PUT", path, map[string]any{"station": st, "secrets": BenchSecrets}); status != http.StatusOK {
			t.Errorf("update time %q: HTTP %d %s", ut, status, errMessage(b))
		}
	}
}

func contractStrictJSON(t *testing.T, start Starter) {
	c := start(t, Config{}).Client()
	installGSD(t, c)
	s, err := c.CreateProject(ctx, "strict", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/sessions/" + s.ID + "/station"
	body := func(edit func(st map[string]any, root map[string]any)) map[string]any {
		st := stationBody(t, BenchStation())
		root := map[string]any{"station": st, "secrets": map[string]any{"webUsers": map[string]any{"naut": BenchWebPassword}, "fullAccessPassword": BenchFullAccessPassword}}
		edit(st, root)
		return root
	}
	refused := map[string]any{
		"unknown top field":     body(func(_, r map[string]any) { r["extra"] = 1 }),
		"unknown station field": body(func(st, _ map[string]any) { st["extra"] = 1 }),
		"unknown cpu field":     body(func(st, _ map[string]any) { st["cpu"].(map[string]any)["extra"] = 1 }),
		// Go's encoding/json would match these case-insensitively; the
		// agent matches exactly, so a misspelt field is never taken.
		"a key in the wrong case": body(func(st, _ map[string]any) {
			cpu := st["cpu"].(map[string]any)
			cpu["subnetmask"] = cpu["subnetMask"]
			delete(cpu, "subnetMask")
		}),
		"a top key in the wrong case": body(func(st, r map[string]any) {
			r["Station"] = st
			delete(r, "station")
		}),
		"unknown module field": body(func(st, _ map[string]any) {
			st["modules"].([]any)[0].(map[string]any)["slott"] = 2
		}),
		"unknown secrets field": body(func(_, r map[string]any) { r["secrets"].(map[string]any)["admin"] = "x" }),
		"slot as a string":      body(func(st, _ map[string]any) { st["modules"].([]any)[0].(map[string]any)["slot"] = "2" }),
		"slot as a fraction":    body(func(st, _ map[string]any) { st["modules"].([]any)[0].(map[string]any)["slot"] = 2.5 }),
		"enabled as a string":   body(func(st, _ map[string]any) { st["webServer"].(map[string]any)["enabled"] = "true" }),
		"rights as a string": body(func(st, _ map[string]any) {
			st["webServer"].(map[string]any)["users"].([]any)[0].(map[string]any)["rights"] = "read"
		}),
		"password as a number": body(func(_, r map[string]any) { r["secrets"].(map[string]any)["webUsers"] = map[string]any{"naut": 5} }),
		"another password as a number": body(func(_, r map[string]any) {
			r["secrets"].(map[string]any)["webUsers"] = map[string]any{"naut": BenchWebPassword, "other": 5}
		}),
		"station as an array":  body(func(_, r map[string]any) { r["station"] = []any{} }),
		"modules as an object": body(func(st, _ map[string]any) { st["modules"] = map[string]any{} }),
		"body an array":        "[]",
		"body empty":           "",
		"body cut short":       `{"station":`,
		"body not json":        "station=1",
	}
	// What a lenient parser (JavaScriptSerializer) would take and Go never
	// does. Each one is the valid body, wrong only in its syntax.
	valid, _ := json.Marshal(body(func(_, _ map[string]any) {}))
	if status, rb := rawJSON(t, c, "PUT", path, string(valid)); status != http.StatusOK {
		t.Fatalf("the valid body: HTTP %d %s", status, rb)
	}
	syntax := map[string][2]string{
		"single quotes":          {`"secrets"`, `'secrets'`},
		"a bare key":             {`"secrets"`, `secrets`},
		"a trailing comma":       {`"slot":2}`, `"slot":2,}`},
		"a comment":              {`"slot":2`, `"slot":/* two */2`},
		"NaN":                    {`"inputStart":0`, `"inputStart":NaN`},
		"a leading zero":         {`"slot":2`, `"slot":02`},
		"hex":                    {`"slot":2`, `"slot":0x2`},
		"a plus sign":            {`"slot":2`, `"slot":+2`},
		"a trailing point":       {`"slot":2`, `"slot":2.`},
		"a bare point":           {`"inputStart":0`, `"inputStart":.0`},
		"undefined":              {`"inputStart":0`, `"inputStart":undefined`},
		"a raw tab in a string":  {`"` + BenchWebPassword + `"`, "\"Bench\tweb\""},
		"a bad escape":           {`"` + BenchWebPassword + `"`, `"Bench\qweb"`},
		"a single-quoted string": {`"` + BenchWebPassword + `"`, `'` + BenchWebPassword + `'`},
		"a key twice":            {`"slot":2`, `"slot":1,"slot":2`},
		"a key twice, escaped":   {`"slot":2`, `"slot":1,"sl\u006ft":2`},
	}
	for name, sub := range syntax {
		if !strings.Contains(string(valid), sub[0]) {
			t.Fatalf("%s: %q is not in the valid body %s", name, sub[0], valid)
		}
		b := strings.Replace(string(valid), sub[0], sub[1], 1)
		refused[name] = b
	}
	refused["two documents"] = string(valid) + " {}"
	for name, b := range refused {
		if status, rb := rawJSON(t, c, "PUT", path, b); status != http.StatusBadRequest || envelopeKind(rb) != tiad.KindBadRequest {
			t.Errorf("%s: HTTP %d %s, want 400 bad_request", name, status, rb)
		}
	}
	// null is the same as absent, as in Go.
	accepted := map[string]any{
		"null router":  body(func(st, _ map[string]any) { st["cpu"].(map[string]any)["router"] = nil }),
		"null modules": body(func(st, _ map[string]any) { st["modules"] = nil }),
		"null start":   body(func(st, _ map[string]any) { st["modules"].([]any)[0].(map[string]any)["inputStart"] = nil }),
	}
	for name, b := range accepted {
		if status, rb := rawJSON(t, c, "PUT", path, b); status != http.StatusOK {
			t.Errorf("%s: HTTP %d %s, want 200", name, status, rb)
		}
	}
}

func contractSessions(t *testing.T, start Starter) {
	c := start(t, Config{}).Client()
	installGSD(t, c)
	s, err := c.CreateProject(ctx, "p", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if status, b := rawJSON(t, c, "DELETE", "/v1/sessions/"+s.ID, struct{}{}); status != http.StatusNotFound || envelopeKind(b) != tiad.KindNotFound {
		t.Fatalf("closing a closed session = HTTP %d %s, want 404", status, b)
	}
	o, err := c.Open(ctx, "projects/p/p.ap21")
	if err != nil || o.Project != "projects/p/p.ap21" {
		t.Fatalf("open: %+v %v", o, err)
	}
	if _, err := c.Open(ctx, "projects/p/p.ap21"); tiad.Kind(err) != tiad.KindBusy {
		t.Fatalf("second open: %v", err)
	}
	if _, err := c.Open(ctx, "projects/none/none.ap21"); tiad.Kind(err) != tiad.KindNotFound {
		t.Fatalf("open a missing project: %v", err)
	}
	ss, err := c.Sessions(ctx)
	if err != nil || len(ss) != 1 || ss[0].ID != o.ID || ss[0].Project != o.Project {
		t.Fatalf("sessions: %+v %v", ss, err)
	}
	for _, p := range append(append([]string{}, RefusedPaths...), "../x") {
		if status, b := rawJSON(t, c, "POST", "/v1/sessions", map[string]string{"path": p}); status != http.StatusBadRequest || envelopeKind(b) != tiad.KindBadRequest {
			t.Errorf("open %q = HTTP %d %s, want 400 bad_request", p, status, b)
		}
	}
	// A project opened from a path has no station spec yet; applying one
	// is the first change.
	if r, err := o.ApplyStation(ctx, BenchStation(), BenchSecrets); err != nil || !reflect.DeepEqual(r.Changes, []string{"station created"}) {
		t.Fatalf("first station on an opened project: %+v %v", r, err)
	}
	for _, op := range []string{"station", "software", "compile"} {
		method := "POST"
		if op == "station" {
			method = "PUT"
		}
		if status, b := rawJSON(t, c, method, "/v1/sessions/s999/"+op, struct{}{}); status != http.StatusNotFound || envelopeKind(b) != tiad.KindNotFound {
			t.Errorf("%s on no session = HTTP %d %s, want 404", op, status, b)
		}
	}
}

func contractStationChanges(t *testing.T, start Starter) {
	c := start(t, Config{}).Client()
	installGSD(t, c)
	mod := tiad.DeviceModule{Slot: 1, Subslot: 1, Module: "ID_Mod_In4Out2", InputStart: intp(100), OutputStart: intp(100)}
	s, err := c.CreateProject(ctx, "bench", BenchStation(mod), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	apply := func(st tiad.Station, want ...string) {
		t.Helper()
		if want == nil {
			want = []string{}
		}
		r, err := s.ApplyStation(ctx, st, BenchSecrets)
		if err != nil || !reflect.DeepEqual(r.Changes, want) {
			t.Fatalf("apply: %+v %v, want %q", r, err, want)
		}
	}
	apply(BenchStation(mod))
	mod.Module = "ID_Mod_In8Out8"
	apply(BenchStation(mod), "device io-device-1 changed")
	st := BenchStation(mod)
	st.Modules = append(st.Modules, tiad.Module{Slot: 3, Order: "6ES7 132-6BH01-0BA0"})
	apply(st, "module slot 3 added")
	st.Modules = st.Modules[:1]
	st.CPU.Router = "192.0.2.1"
	apply(st, "cpu PLC_1 changed", "module slot 3 removed")
	st.WebServer.Users[0].Rights = []string{"read"}
	st.Profinet.UpdateTime = "32ms"
	apply(st, "web server changed", "IO system changed")
	st.Profinet.Devices = nil
	apply(st, "device io-device-1 removed")
}

const programSCL = `FUNCTION_BLOCK "naut_Main"
VERSION : 0.1
   VAR
      count : Int;
   END_VAR
BEGIN
   #count := #count + 1;
END_FUNCTION_BLOCK

DATA_BLOCK "naut_Main_DB"
"naut_Main"
BEGIN
END_DATA_BLOCK
`

func blockNames(bs []tiad.Block) string {
	var n []string
	for _, b := range bs {
		n = append(n, b.Kind+":"+b.Name)
	}
	return strings.Join(n, ",")
}

// contractSoftwareOwnership: an import never touches a program block
// outside its group (#789, Ownership.cs). A source that declares one (TIA
// would replace it: the TIA VM replaced the project's Main [OB1] with an
// FB) refuses the whole call with foreign_block, naming the block, before
// anything is imported: quoted or bare, any case. An OB source while
// another OB holds number 1 fails as TIA fails it.
func contractSoftwareOwnership(t *testing.T, start Starter) {
	c := start(t, Config{}).Client()
	installGSD(t, c)
	s, err := c.CreateProject(ctx, "own", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	put := func(p, body string) {
		t.Helper()
		if _, err := c.PutFile(ctx, p, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	put("src/Main.scl", programSCL)
	put("src/Clash.scl", "FUNCTION_BLOCK \"Main\"\nVERSION : 0.1\nBEGIN\nEND_FUNCTION_BLOCK\n")
	put("src/Bare.scl", "(* a comment *)\nfunction_block MAIN\nBEGIN\nEND_FUNCTION_BLOCK\n")
	put("src/OB.scl", "ORGANIZATION_BLOCK \"OB_main\"\nTITLE = \"Cyclic interrupt, 100ms\"\nBEGIN\nEND_ORGANIZATION_BLOCK\n")
	put("src/NextLine.scl", "END_FUNCTION FUNCTION_BLOCK\r\n  \"Main\"\r\nBEGIN\r\nEND_FUNCTION_BLOCK\r\n")
	put("src/Recipe.udt", "TYPE \"naut_Recipe\"\nVERSION : 0.1\n   STRUCT\n      speed : Real;\n   END_STRUCT;\nEND_TYPE\n")
	if _, err := s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/Main.scl"}}); err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{"src/Clash.scl", "src/Bare.scl", "src/NextLine.scl"} {
		// With an owned source beside it: nothing of the call is imported.
		_, err := s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{src, "src/Main.scl"}, Prune: true})
		if tiad.Kind(err) != tiad.KindForeignBlock || !strings.Contains(err.Error(), src+` declares "Main" [OB1] in the program blocks' root`) ||
			!strings.Contains(err.Error(), "Nothing was imported") || !strings.Contains(err.Error(), "Rename it in the source") {
			t.Fatalf("%s: %v, want foreign_block naming Main [OB1]", src, err)
		}
	}
	// A source with a data type and a block: each goes to its own group.
	put("src/Mixed.scl", "TYPE \"naut_R2\"\nVERSION : 0.1\n   STRUCT\n      x : Int;\n   END_STRUCT;\nEND_TYPE\n\nFUNCTION \"naut_F\" : Void\nBEGIN\nEND_FUNCTION\n")
	if _, err := s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/Main.scl", "src/Mixed.scl"}}); tiad.Kind(err) != tiad.KindBadRequest ||
		!strings.Contains(err.Error(), "src/Mixed.scl declares a data type and a block") || !strings.Contains(err.Error(), "Nothing was imported") {
		t.Fatalf("a data type and a block in one source: %v", err)
	}
	// A group is one user group under the root.
	for _, g := range []string{"nautilus/sub", `a\b`} {
		if _, err := s.Software(ctx, tiad.SoftwareRequest{Group: g, Sources: []string{"src/Main.scl"}}); tiad.Kind(err) != tiad.KindBadRequest || !strings.Contains(err.Error(), "path separator") {
			t.Fatalf("group %q: %v", g, err)
		}
	}
	// The refused calls changed nothing: the owned blocks are as before.
	r, err := s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/Main.scl"}})
	if err != nil || blockNames(r.Blocks) != "FB:naut_Main,DB:naut_Main_DB" {
		t.Fatalf("after the refusals: %s %v", blockNames(r.Blocks), err)
	}
	// Into another group, the blocks nautilus imported are foreign too.
	if _, err := s.Software(ctx, tiad.SoftwareRequest{Group: "other", Sources: []string{"src/Main.scl"}}); tiad.Kind(err) != tiad.KindForeignBlock ||
		!strings.Contains(err.Error(), `"naut_Main" [FB1] in group "nautilus"`) {
		t.Fatalf("a block of another group: %v", err)
	}
	// An OB source with no obs entry would be a program cycle OB.
	if _, err := s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/OB.scl"}}); tiad.Kind(err) != tiad.KindBadRequest ||
		!strings.Contains(err.Error(), `src/OB.scl declares OB "OB_main", which obs does not name`) {
		t.Fatalf("an OB source with no obs entry: %v", err)
	}
}

// recipeUDT is a writer-shaped data type; recipeFB uses it as a pin.
const (
	recipeUDT = "TYPE \"naut_Recipe\"\nVERSION : 0.1\n   STRUCT\n      speed : Real := 1.5;\n   END_STRUCT;\n\nEND_TYPE\n"
	recipeFB  = "FUNCTION_BLOCK \"naut_Mix\"\nVERSION : 0.1\n   VAR_INPUT\n      r : \"naut_Recipe\";\n   END_VAR\nBEGIN\nEND_FUNCTION_BLOCK\n"
)

func foreignNames(l []tiad.ForeignBlock) string {
	var n []string
	for _, b := range l {
		k := "block"
		if b.DataType {
			k = "type"
		}
		n = append(n, k+":"+b.Group+"/"+b.Label())
	}
	return strings.Join(n, ",")
}

// contractSoftwareDataTypes (D1b, #1301): a source of data types imports
// into the data types' group named like the block group, beside the
// blocks that use them; the result lists them (UDT), the customer's data
// types are on the foreign list after the blocks, a regenerated type of
// nautilus's (and the blocks the compile then touches) is no change, and
// a prune removes a type no source declares any more.
func contractSoftwareDataTypes(t *testing.T, start Starter) {
	a := start(t, Config{})
	c := a.Client()
	a.SetTypes(t, TypesScript{Foreign: []ForeignType{{Name: "Cust", Group: ""}, {Name: "Deep", Group: "custg/sub"}}})
	installGSD(t, c)
	s, err := c.CreateProject(ctx, "types", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	for p, body := range map[string]string{"src/naut_Recipe.udt": recipeUDT, "src/naut_Mix.scl": recipeFB, "src/Main.scl": programSCL,
		"src/naut_Old.udt": "TYPE \"naut_Old\"\nVERSION : 0.1\n   STRUCT\n      x : Int;\n   END_STRUCT;\n\nEND_TYPE\n"} {
		if _, err := c.PutFile(ctx, p, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/naut_Recipe.udt", "src/naut_Old.udt", "src/naut_Mix.scl", "src/Main.scl"}, Prune: true, PrunePrefix: "naut_"})
	if err != nil || blockNames(r.Blocks) != "FB:naut_Main,DB:naut_Main_DB,FB:naut_Mix,UDT:naut_Old,UDT:naut_Recipe" {
		t.Fatalf("import with data types: %s %v", blockNames(r.Blocks), err)
	}
	if got := foreignNames(r.Foreign); got != "block:/Main [OB1],type:/Cust,type:custg/sub/Deep" {
		t.Fatalf("foreign list: %s, want the blocks, then the customer's data types", got)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatalf("compile: %v", err)
	}
	// Again, naut_Old gone from the sources: the type is regenerated in
	// place, the prune removes naut_Old, and nothing outside changed.
	r, err = s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/naut_Recipe.udt", "src/naut_Mix.scl", "src/Main.scl"}, Prune: true, PrunePrefix: "naut_"})
	if err != nil || blockNames(r.Blocks) != "FB:naut_Main,DB:naut_Main_DB,FB:naut_Mix,UDT:naut_Recipe" {
		t.Fatalf("re-import: %s %v", blockNames(r.Blocks), err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatalf("compile after a regenerated type: %v", err)
	}
	if _, err := s.Save(ctx, false); err != nil {
		t.Fatalf("save: %v", err)
	}
}

// contractSoftwareDataTypeClash (D1b): TIA regenerates a same-named data
// type where it sits, and keeps one name for blocks and data types. So a
// declared data type or block that names a customer's data type (the
// root or a nested user group, any case), or a declared data type that
// names a customer's block, refuses the whole call before anything is
// imported, and the session stays usable.
func contractSoftwareDataTypeClash(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	c := a.Client()
	a.SetTypes(t, TypesScript{Foreign: []ForeignType{{Name: "Cust", Group: ""}, {Name: "Deep", Group: "custg/sub/subsub"}}})
	for _, tc := range []struct{ file, body, named string }{
		{"src/Cust.udt", "TYPE \"CUST\"\nVERSION : 0.1\n   STRUCT\n      x : Int;\n   END_STRUCT;\n\nEND_TYPE\n", `src/Cust.udt declares data type "Cust" in the PLC data types' root`},
		{"src/Deep.udt", "TYPE \"deep\"\nVERSION : 0.1\n   STRUCT\n      x : Int;\n   END_STRUCT;\n\nEND_TYPE\n", `src/Deep.udt declares data type "Deep" in data types group "custg/sub/subsub"`},
		{"src/CustFB.scl", "FUNCTION_BLOCK \"Cust\"\nVERSION : 0.1\nBEGIN\nEND_FUNCTION_BLOCK\n", `src/CustFB.scl declares data type "Cust" in the PLC data types' root`},
		{"src/MainT.udt", "TYPE \"Main\"\nVERSION : 0.1\n   STRUCT\n      x : Int;\n   END_STRUCT;\n\nEND_TYPE\n", `src/MainT.udt declares "Main" [OB1] in the program blocks' root`},
	} {
		if _, err := c.PutFile(ctx, tc.file, []byte(tc.body)); err != nil {
			t.Fatal(err)
		}
		_, err := s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{tc.file, "src/Main.scl"}, Prune: true, PrunePrefix: "naut_"})
		if tiad.Kind(err) != tiad.KindForeignBlock || !strings.Contains(err.Error(), tc.named) || !strings.Contains(err.Error(), "Nothing was imported") {
			t.Fatalf("%s: %v, want foreign_block naming %s", tc.file, err, tc.named)
		}
	}
	// Nothing was imported and the session stays usable: it downloads.
	a.SetQuestions(t, nil)
	if _, err := s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/Main.scl"}, Prune: true, PrunePrefix: "naut_"}); err != nil {
		t.Fatalf("an import after the refusals: %v", err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(ctx, false); err != nil {
		t.Fatalf("a save after the refusals: %v", err)
	}
}

// contractSoftwareDataTypeOutsideGroup (D1b): TIA hands back a data type
// outside the group (it regenerated a customer's type the agent's
// reading never listed, where it sits): the import fails naming it and
// the recovery, and the session is never saved, archived for a deploy,
// downloaded or imported into again, though no reading shows a change.
func contractSoftwareDataTypeOutsideGroup(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	c := a.Client()
	a.SetTypes(t, TypesScript{Foreign: []ForeignType{{Name: "Cust", Group: "custg"}}, Hidden: true})
	if _, err := c.PutFile(ctx, "src/Cust.udt", []byte("TYPE \"Cust\"\nVERSION : 0.1\n   STRUCT\n      y : Real;\n   END_STRUCT;\n\nEND_TYPE\n")); err != nil {
		t.Fatal(err)
	}
	_, err := s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/Cust.udt", "src/Main.scl"}, Prune: true, PrunePrefix: "naut_"})
	if tiad.Kind(err) != tiad.KindOperationFailed || !strings.Contains(err.Error(), `src/Cust.udt: TIA generated data type "Cust" outside`) ||
		!strings.Contains(err.Error(), "Close it without saving, then open or adopt the project again") {
		t.Fatalf("an import TIA answered outside the group: %v", err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Download(ctx, benchSoftwareLoad); tiad.Kind(err) != tiad.KindUncleanSession || tiad.DeployIDOf(err) != "" {
		t.Fatalf("a download after it: %v, want unclean_session", err)
	}
	if l := a.Loaded(t); len(l) != 0 {
		t.Fatalf("a download after it loaded %+v", l)
	}
	if l := deploys(t, c, ""); len(l.Deploys) != 0 {
		t.Fatalf("a download after it left a record: %+v", l)
	}
	if _, err := s.Save(ctx, false); tiad.Kind(err) != tiad.KindUncleanSession {
		t.Fatalf("a save after it: %v, want unclean_session", err)
	}
	if _, err := s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/Main.scl"}}); tiad.Kind(err) != tiad.KindUncleanSession {
		t.Fatalf("another import after it: %v, want unclean_session", err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// contractCompileTouchesForeign (D1b): after an import, a compile that
// moves a customer object's date (a customer type that uses one of
// nautilus's) makes the session unusable, naming it; a compile with no
// import before it checks nothing.
func contractCompileTouchesForeign(t *testing.T, start Starter) {
	a := start(t, Config{Targets: []string{benchCPU}})
	c := a.Client()
	installGSD(t, c)
	a.SetTypes(t, TypesScript{Foreign: []ForeignType{{Name: "Cust", Group: "custg"}}, CompileTouchesForeign: true})
	fresh, err := c.CreateProject(ctx, "noimport", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.Compile(ctx, true, true); err != nil {
		t.Fatalf("a compile with no import: %v", err)
	}
	if err := fresh.Close(ctx); err != nil {
		t.Fatal(err)
	}
	s, err := c.CreateProject(ctx, "touch", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutFile(ctx, "src/Main.scl", []byte(programSCL)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/Main.scl"}, Prune: true, PrunePrefix: "naut_"}); err != nil {
		t.Fatal(err)
	}
	_, err = s.Compile(ctx, true, true)
	if tiad.Kind(err) != tiad.KindUncleanSession || !strings.Contains(err.Error(), `the compile changed program blocks or data types outside group "nautilus" (data type "Cust" in data types group "custg" was modified)`) ||
		!strings.Contains(err.Error(), "close it without saving") || !strings.Contains(err.Error(), "needs the project changed outside nautilus") {
		t.Fatalf("a compile that moved a customer type: %v", err)
	}
	// A clean compile after it does not make it usable again.
	a.SetTypes(t, TypesScript{Foreign: []ForeignType{{Name: "Cust", Group: "custg"}}})
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatalf("a compile that moves nothing: %v", err)
	}
	if _, err := s.Download(ctx, benchSoftwareLoad); tiad.Kind(err) != tiad.KindUncleanSession {
		t.Fatalf("a download after it: %v, want unclean_session", err)
	}
	if l := a.Loaded(t); len(l) != 0 {
		t.Fatalf("a download after it loaded %+v", l)
	}
	if _, err := s.Save(ctx, false); tiad.Kind(err) != tiad.KindUncleanSession {
		t.Fatalf("a save after it: %v, want unclean_session", err)
	}
}

// contractImportTouchesForeignType (D1b): an import that moves a
// customer data type's date, though TIA hands nothing back outside the
// group, fails naming it, and the session is never saved or downloaded.
func contractImportTouchesForeignType(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	a.SetTypes(t, TypesScript{Foreign: []ForeignType{{Name: "Cust", Group: "custg"}}, ImportTouchesForeign: true})
	_, err := s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/Main.scl"}, Prune: true, PrunePrefix: "naut_"})
	if tiad.Kind(err) != tiad.KindOperationFailed || !strings.Contains(err.Error(), `outside group "nautilus" (data type "Cust" in data types group "custg" was modified)`) {
		t.Fatalf("an import that moved a customer type: %v", err)
	}
	a.SetTypes(t, TypesScript{Foreign: []ForeignType{{Name: "Cust", Group: "custg"}}})
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Download(ctx, benchSoftwareLoad); tiad.Kind(err) != tiad.KindUncleanSession {
		t.Fatalf("a download after it: %v, want unclean_session", err)
	}
	if _, err := s.Save(ctx, false); tiad.Kind(err) != tiad.KindUncleanSession {
		t.Fatalf("a save after it: %v, want unclean_session", err)
	}
}

// contractSoftwarePrunePrefix (D1b): with prunePrefix, a prune refuses
// when the group holds a block or data type that no source declares and
// that lacks the prefix (a customer's object there would be deleted),
// naming it, before anything is imported; with none, it prunes as before.
func contractSoftwarePrunePrefix(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	c := a.Client()
	if _, err := c.PutFile(ctx, "src/Theirs.scl", []byte("FUNCTION \"Theirs\" : Void\nVERSION : 0.1\nBEGIN\nEND_FUNCTION\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutFile(ctx, "src/Shared.udt", []byte("TYPE \"Shared\"\nVERSION : 0.1\n   STRUCT\n      x : Int;\n   END_STRUCT;\n\nEND_TYPE\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/Shared.udt", "src/Theirs.scl", "src/Main.scl"}}); err != nil {
		t.Fatal(err)
	}
	_, err := s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/Main.scl"}, Prune: true, PrunePrefix: "naut_"})
	if tiad.Kind(err) != tiad.KindForeignBlock || !strings.Contains(err.Error(), `: "Theirs" [FC1] in group "nautilus"; data type "Shared" in data types group "nautilus": not named with the prefix "naut_"`) ||
		!strings.Contains(err.Error(), "Nothing was imported") {
		t.Fatalf("a prune over objects without the prefix: %v", err)
	}
	r, err := s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/Shared.udt", "src/Theirs.scl", "src/Main.scl"}, Prune: true, PrunePrefix: "naut_"})
	if err != nil || blockNames(r.Blocks) != "UDT:Shared,FC:Theirs,FB:naut_Main,DB:naut_Main_DB" {
		t.Fatalf("declared ones are kept: %s %v", blockNames(r.Blocks), err)
	}
	// No prefix (an older caller): the blocks are pruned as before, the
	// data types never.
	r, err = s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/Main.scl"}, Prune: true})
	if err != nil || blockNames(r.Blocks) != "UDT:Shared,FB:naut_Main,DB:naut_Main_DB" {
		t.Fatalf("no prefix: blocks pruned as before, data types kept: %s %v", blockNames(r.Blocks), err)
	}
}

// contractSoftwareReplacedForeign: when TIA changes a block outside the
// group anyway (a header the agent's reading missed), the import fails
// naming the change, and the session is never saved, archived for a
// deploy, downloaded or imported into again: only closed.
func contractSoftwareReplacedForeign(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	a.SetImportReplacesForeign(t, true)
	_, err := s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/Main.scl"}, Prune: true})
	if tiad.Kind(err) != tiad.KindOperationFailed ||
		!strings.Contains(err.Error(), `"Main" [OB1] in the program blocks' root is now "Main" [FB1] in the program blocks' root`) ||
		!strings.Contains(err.Error(), "Close it without saving") {
		t.Fatalf("an import that replaced Main: %v", err)
	}
	a.SetImportReplacesForeign(t, false)
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Download(ctx, benchSoftwareLoad); tiad.Kind(err) != tiad.KindUncleanSession || tiad.DeployIDOf(err) != "" {
		t.Fatalf("a download after it: %v, want unclean_session", err)
	}
	if l := a.Loaded(t); len(l) != 0 {
		t.Fatalf("a download after it loaded %+v", l)
	}
	if l := deploys(t, a.Client(), ""); len(l.Deploys) != 0 {
		t.Fatalf("a download after it left a record: %+v", l)
	}
	if _, err := s.Save(ctx, false); tiad.Kind(err) != tiad.KindUncleanSession {
		t.Fatalf("a save after it: %v, want unclean_session", err)
	}
	if _, err := s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/Main.scl"}}); tiad.Kind(err) != tiad.KindUncleanSession {
		t.Fatalf("another import after it: %v, want unclean_session", err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// obSCL is a writer-shaped cyclic interrupt OB source calling the
// program's instance DB.
func obSCL(name string) string {
	return "ORGANIZATION_BLOCK \"" + name + "\"\nTITLE = \"Cyclic interrupt\"\n{ S7_Optimized_Access := 'TRUE' }\nVERSION : 0.1\n\nBEGIN\n\t\"naut_Main_DB\"();\nEND_ORGANIZATION_BLOCK\n"
}

func obSteps(l []tiad.OBStep) string {
	var s []string
	for _, st := range l {
		x := st.Name + " " + st.Action
		if st.Was != "" {
			x += " was " + st.Was
		}
		if st.Now != "" {
			x += " now " + st.Now
		}
		s = append(s, x)
	}
	return strings.Join(s, "; ")
}

// contractSoftwareCyclicOBs: the agent makes each cyclic interrupt OB
// (obs) before the sources, and reports what it did: created, kept,
// recreated with the cycle it had and the one it has, removed with prune.
// It refuses an entry out of range, an OB source with no entry, an entry
// with no source, a number another OB of the group holds without prune,
// and a block named like TIA's system constant OB_<name> of an OB.
func contractSoftwareCyclicOBs(t *testing.T, start Starter) {
	c := start(t, Config{}).Client()
	installGSD(t, c)
	s, err := c.CreateProject(ctx, "obs", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	put := func(p, body string) {
		t.Helper()
		if _, err := c.PutFile(ctx, p, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	put("src/Main.scl", programSCL)
	put("src/OB_main.scl", obSCL("naut_OB_main"))
	put("src/OB_fast.scl", obSCL("naut_OB_fast"))
	put("src/Clash.scl", "FUNCTION_BLOCK \"OB_main\"\nBEGIN\nEND_FUNCTION_BLOCK\n")
	src := []string{"src/Main.scl", "src/OB_main.scl"}
	main100 := []tiad.CyclicOB{{Name: "naut_OB_main", Number: 30, CycleMS: 100}}

	r, err := s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: src, OBs: main100, Prune: true})
	if err != nil || obSteps(r.OBs) != "naut_OB_main created now OB30, cyclic interrupt, 100 ms" {
		t.Fatalf("first import: %s %v", obSteps(r.OBs), err)
	}
	if got := blockNames(r.Blocks); got != "FB:naut_Main,DB:naut_Main_DB,OB:naut_OB_main" {
		t.Fatalf("blocks = %s", got)
	}
	// Every program block outside the group, which a download loads too
	// (#1326): here the project's own Main at the root, never the group's.
	if want := []tiad.ForeignBlock{{Name: "Main", Kind: "OB", Number: 1, Group: ""}}; !reflect.DeepEqual(r.Foreign, want) {
		t.Fatalf("foreign = %+v, want %+v", r.Foreign, want)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	r, err = s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: src, OBs: main100, Prune: true})
	if err != nil || obSteps(r.OBs) != "naut_OB_main kept now OB30, cyclic interrupt, 100 ms" {
		t.Fatalf("the same again: %s %v", obSteps(r.OBs), err)
	}
	r, err = s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: src, OBs: []tiad.CyclicOB{{Name: "naut_OB_main", Number: 30, CycleMS: 50}}, Prune: true})
	if err != nil || obSteps(r.OBs) != "naut_OB_main recreated was OB30, cyclic interrupt, 100 ms now OB30, cyclic interrupt, 50 ms" {
		t.Fatalf("a new cycle: %s %v", obSteps(r.OBs), err)
	}

	refused := []struct {
		why, kind, want string
		req             tiad.SoftwareRequest
	}{
		{"a number out of range", tiad.KindBadRequest, "number 39 is no cyclic interrupt OB number",
			tiad.SoftwareRequest{Group: "nautilus", Sources: src, OBs: []tiad.CyclicOB{{Name: "naut_OB_main", Number: 39, CycleMS: 100}}}},
		{"a cycle over 60 s", tiad.KindBadRequest, "outside 1 ms to 60 s",
			tiad.SoftwareRequest{Group: "nautilus", Sources: src, OBs: []tiad.CyclicOB{{Name: "naut_OB_main", Number: 30, CycleMS: 60001}}}},
		{"an OB source with no entry", tiad.KindBadRequest, `src/OB_main.scl declares OB "naut_OB_main", which obs does not name`,
			tiad.SoftwareRequest{Group: "nautilus", Sources: src}},
		{"an entry with no source", tiad.KindBadRequest, `obs names "naut_OB_fast", which no source declares`,
			tiad.SoftwareRequest{Group: "nautilus", Sources: src, OBs: append(append([]tiad.CyclicOB{}, main100...), tiad.CyclicOB{Name: "naut_OB_fast", Number: 31, CycleMS: 10})}},
		{"OB30 held by another OB of the group, no prune", tiad.KindBadRequest, `"naut_OB_main" [OB30] in the group holds it`,
			tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/Main.scl", "src/OB_fast.scl"}, OBs: []tiad.CyclicOB{{Name: "naut_OB_fast", Number: 30, CycleMS: 10}}}},
		{"a block named like Main's system constant", tiad.KindBadRequest, `src/Clash.scl declares "OB_main", the name of TIA's system constant for OB "main"`,
			tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/Clash.scl"}}},
	}
	for _, x := range refused {
		if _, err := s.Software(ctx, x.req); tiad.Kind(err) != x.kind || !strings.Contains(err.Error(), x.want) {
			t.Fatalf("%s: %v, want %s naming %q", x.why, err, x.kind, x.want)
		}
	}
	// The refusals changed nothing.
	r, err = s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: src, OBs: []tiad.CyclicOB{{Name: "naut_OB_main", Number: 30, CycleMS: 50}}, Prune: true})
	if err != nil || obSteps(r.OBs) != "naut_OB_main kept now OB30, cyclic interrupt, 50 ms" {
		t.Fatalf("after the refusals: %s %v", obSteps(r.OBs), err)
	}
	// Two OBs trade numbers: every delete comes before any import.
	put("src/OB_b.scl", obSCL("naut_OB_b"))
	pair := []string{"src/Main.scl", "src/OB_main.scl", "src/OB_b.scl"}
	if _, err := s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: pair, OBs: []tiad.CyclicOB{{Name: "naut_OB_main", Number: 30, CycleMS: 50}, {Name: "naut_OB_b", Number: 31, CycleMS: 10}}, Prune: true}); err != nil {
		t.Fatal(err)
	}
	r, err = s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: pair, OBs: []tiad.CyclicOB{{Name: "naut_OB_main", Number: 31, CycleMS: 50}, {Name: "naut_OB_b", Number: 30, CycleMS: 10}}, Prune: true})
	if err != nil || obSteps(r.OBs) != "naut_OB_main recreated was OB30, cyclic interrupt, 50 ms now OB31, cyclic interrupt, 50 ms; naut_OB_b recreated was OB31, cyclic interrupt, 10 ms now OB30, cyclic interrupt, 10 ms" {
		t.Fatalf("a swap: %s %v", obSteps(r.OBs), err)
	}
	r, err = s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: src, OBs: []tiad.CyclicOB{{Name: "naut_OB_main", Number: 30, CycleMS: 50}}, Prune: true})
	if err != nil || obSteps(r.OBs) != "naut_OB_b removed was OB30, cyclic interrupt, 10 ms; naut_OB_main recreated was OB31, cyclic interrupt, 50 ms now OB30, cyclic interrupt, 50 ms" {
		t.Fatalf("back to one (prune removes the OB that holds 30): %s %v", obSteps(r.OBs), err)
	}
	// With prune, an OB of the group that holds the number goes first.
	r, err = s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/Main.scl", "src/OB_fast.scl"}, OBs: []tiad.CyclicOB{{Name: "naut_OB_fast", Number: 30, CycleMS: 10}}, Prune: true})
	if err != nil || obSteps(r.OBs) != "naut_OB_main removed was OB30, cyclic interrupt, 50 ms; naut_OB_fast created now OB30, cyclic interrupt, 10 ms" ||
		blockNames(r.Blocks) != "FB:naut_Main,DB:naut_Main_DB,OB:naut_OB_fast" {
		t.Fatalf("prune: %s %s %v", obSteps(r.OBs), blockNames(r.Blocks), err)
	}
}

// contractSoftwareCyclicOBNotAsAsked: when TIA's OB import does not keep
// the class asked, the check after the import fails the call, and the
// session is never saved or downloaded.
func contractSoftwareCyclicOBNotAsAsked(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	c := a.Client()
	if _, err := c.PutFile(ctx, "src/OB_main.scl", []byte(obSCL("naut_OB_main"))); err != nil {
		t.Fatal(err)
	}
	a.SetOBImportIgnoresClass(t, true)
	_, err := s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/Main.scl", "src/OB_main.scl"}, OBs: []tiad.CyclicOB{{Name: "naut_OB_main", Number: 30, CycleMS: 100}}, Prune: true})
	if tiad.Kind(err) != tiad.KindOperationFailed || !strings.Contains(err.Error(), `"naut_OB_main" is OB30, ProgramCycle, not OB30, cyclic interrupt, 100 ms`) {
		t.Fatalf("an OB TIA made as a program cycle OB: %v", err)
	}
	a.SetOBImportIgnoresClass(t, false)
	if _, err := s.Save(ctx, false); tiad.Kind(err) != tiad.KindUncleanSession {
		t.Fatalf("a save after it: %v", err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Download(ctx, benchSoftwareLoad); tiad.Kind(err) != tiad.KindUncleanSession {
		t.Fatalf("a download after it: %v", err)
	}
	// A failed OB source fails the import too, naming TIA's error, on a
	// session of its own.
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	s2, err := c.CreateProject(ctx, "obbad", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutFile(ctx, "src/OB_bad.scl", []byte(strings.Replace(obSCL("naut_OB_main"), "BEGIN", "BEGIN\n\tFAKE_SOURCE_ERROR", 1))); err != nil {
		t.Fatal(err)
	}
	_, err = s2.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/Main.scl", "src/OB_bad.scl"}, OBs: []tiad.CyclicOB{{Name: "naut_OB_main", Number: 30, CycleMS: 100}}})
	if tiad.Kind(err) != tiad.KindOperationFailed || !strings.Contains(err.Error(), `the source of OB "naut_OB_main" (src/OB_bad.scl) did not import: `) || !strings.Contains(err.Error(), "syntax error") {
		t.Fatalf("a failed OB source: %v", err)
	}
	if _, err := s2.Save(ctx, false); tiad.Kind(err) != tiad.KindUncleanSession {
		t.Fatalf("a save after a failed OB source: %v", err)
	}
	if err := s2.Close(ctx); err != nil {
		t.Fatal(err)
	}

	if l := a.Loaded(t); len(l) != 0 {
		t.Fatalf("loaded %+v", l)
	}
}

func contractSoftware(t *testing.T, start Starter) {
	c := start(t, Config{}).Client()
	installGSD(t, c)
	s, err := c.CreateProject(ctx, "sw", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	put := func(p, body string) {
		t.Helper()
		if _, err := c.PutFile(ctx, p, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	put("src/Main.scl", programSCL)
	put("src/Helpers.scl", "\t FUNCTION \"Helper\" : Void\r\nBEGIN\r\nEND_FUNCTION\r\n")
	put("src/none.scl", "// nothing here\nFUNCTION_BLOCKX \"No\"\n")
	put("tags/Tags.xml", "<TagTable/>")

	r, err := s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/Main.scl", "src/Helpers.scl"}, TagTables: []string{"tags/Tags.xml"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := blockNames(r.Blocks); got != "FC:Helper,FB:naut_Main,DB:naut_Main_DB" {
		t.Fatalf("blocks = %s", got)
	}
	if len(r.Results) != 3 || !r.Results[0].OK || r.Results[2].Path != "tags/Tags.xml" {
		t.Fatalf("results = %+v", r.Results)
	}
	// Without prune, blocks from earlier imports stay; with it, only what
	// the sources produced is left.
	r, err = s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/Main.scl"}})
	if err != nil || blockNames(r.Blocks) != "FC:Helper,FB:naut_Main,DB:naut_Main_DB" {
		t.Fatalf("no prune: %s %v", blockNames(r.Blocks), err)
	}
	r, err = s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/Main.scl"}, Prune: true})
	if err != nil || blockNames(r.Blocks) != "FB:naut_Main,DB:naut_Main_DB" {
		t.Fatalf("prune: %s %v", blockNames(r.Blocks), err)
	}
	r, err = s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/none.scl"}})
	if err != nil || len(r.Results) != 1 || r.Results[0].OK || len(r.Results[0].Messages) == 0 {
		t.Fatalf("a source with no block: %+v %v", r, err)
	}
	if _, err := s.Software(ctx, tiad.SoftwareRequest{Group: "", Sources: []string{"src/Main.scl"}}); tiad.Kind(err) != tiad.KindBadRequest {
		t.Fatalf("empty group: %v", err)
	}
	if _, err := s.Software(ctx, tiad.SoftwareRequest{Group: "g", Sources: []string{"src/missing.scl"}}); tiad.Kind(err) != tiad.KindNotFound {
		t.Fatalf("missing source: %v", err)
	}
	if _, err := s.Software(ctx, tiad.SoftwareRequest{Group: "g", TagTables: []string{"tags/missing.xml"}}); tiad.Kind(err) != tiad.KindNotFound {
		t.Fatalf("missing tag table: %v", err)
	}
	for _, p := range RefusedPaths {
		status, b := rawJSON(t, c, "POST", "/v1/sessions/"+s.ID+"/software", map[string]any{"group": "g", "sources": []string{p}})
		if status != http.StatusBadRequest || envelopeKind(b) != tiad.KindBadRequest {
			t.Errorf("source %q = HTTP %d %s, want 400", p, status, b)
		}
	}
}

func contractCompile(t *testing.T, start Starter) {
	a := start(t, Config{})
	c := a.Client()
	installGSD(t, c)
	s, err := c.CreateProject(ctx, "p", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := s.Compile(ctx, true, true); err != nil || r.Result != "success" || r.Errors != 0 || r.Messages == nil {
		t.Fatalf("compile: %+v %v", r, err)
	}
	if _, err := s.Compile(ctx, false, false); tiad.Kind(err) != tiad.KindBadRequest {
		t.Fatalf("compile nothing: %v", err)
	}
	warn := tiad.CompileResult{Result: "warning", Warnings: 1, Messages: []tiad.CompileMessage{{Object: "PLC_1/Main", Severity: "warning", Text: "unused"}}}
	a.SetCompileResult(t, warn)
	if r, err := s.Compile(ctx, true, false); err != nil || !reflect.DeepEqual(r, warn) {
		t.Fatalf("warnings: %+v %v", r, err)
	}
	// As TIA V21 gives an SCL error (#789): the leaf's object is the line
	// in the block's body, and its parents, the block last, ride along.
	bad := tiad.CompileResult{Result: "error", Errors: 1, Messages: []tiad.CompileMessage{
		{Object: "4", Severity: "error", Text: "Tag #x not defined", Path: []string{"PLC_1", "Program blocks", "nautilus", "naut_Main (FB1)"}},
		{Object: "", Severity: "error", Text: "Compiling finished (errors: 1; warnings: 0)"}}}
	a.SetCompileResult(t, bad)
	_, err = s.Compile(ctx, true, true)
	var te *tiad.Error
	r, ok := tiad.CompileErrors(err)
	if !ok || !errorsAs(err, &te) || te.Status != http.StatusUnprocessableEntity || !reflect.DeepEqual(r, bad) {
		t.Fatalf("compile_failed: %v %+v", err, r)
	}
}

func errorsAs(err error, te **tiad.Error) bool {
	e, ok := err.(*tiad.Error)
	if ok {
		*te = e
	}
	return ok
}

func contractGates(t *testing.T, start Starter) {
	a := start(t, Config{})
	c := a.Client()
	installGSD(t, c)
	s, err := c.CreateProject(ctx, "p", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	a.SetGates(t, []tiad.Gate{{Name: "licence", State: tiad.GateFailed, Detail: "trial expired"}})
	calls := []struct {
		name string
		do   func() error
	}{
		{"gsd/install", func() error { _, err := c.InstallGSD(ctx, "gsd/"+benchGSDML); return err }},
		{"projects", func() error { _, err := c.CreateProject(ctx, "q", BenchStation(), BenchSecrets); return err }},
		{"sessions", func() error { _, err := c.Open(ctx, "projects/p/p.ap21"); return err }},
		{"station", func() error { _, err := s.ApplyStation(ctx, BenchStation(), BenchSecrets); return err }},
		{"software", func() error { _, err := s.Software(ctx, tiad.SoftwareRequest{Group: "g"}); return err }},
		{"compile", func() error { _, err := s.Compile(ctx, true, true); return err }},
	}
	for _, call := range calls {
		err := call.do()
		var te *tiad.Error
		if !errorsAs(err, &te) || te.Kind != tiad.KindGate || te.Status != http.StatusPreconditionFailed || !strings.Contains(te.Message, "licence") || !strings.Contains(te.Message, "trial expired") {
			t.Errorf("%s with a failed gate: %v, want 412 gate naming it", call.name, err)
		}
	}
	// What never needs TIA still answers.
	if _, err := c.Sessions(ctx); err != nil {
		t.Errorf("sessions list with a failed gate: %v", err)
	}
	if err := s.Close(ctx); err != nil {
		t.Errorf("close with a failed gate: %v", err)
	}
	a.SetGates(t, nil)
	if _, err := c.Open(ctx, "projects/p/p.ap21"); err != nil {
		t.Fatalf("open after the gate passes again: %v", err)
	}
}

// contractPrecedence: a request wrong in two ways fails the way the
// earlier check says. The client would catch most of these, so they go
// over the wire raw.
func contractPrecedence(t *testing.T, start Starter) {
	a := start(t, Config{})
	c := a.Client()
	installGSD(t, c)
	s, err := c.CreateProject(ctx, "p", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	badStation := BenchStation()
	badStation.CPU.Address = "x"
	cases := []struct {
		name       string
		gateFails  bool
		method     string
		path       string
		body       any
		wantStatus int
		wantKind   string
		wantMsg    string
	}{
		{"gate before no session", true, "PUT", "/v1/sessions/s999/station", map[string]any{}, 412, tiad.KindGate, ""},
		{"gate before a bad body", true, "POST", "/v1/projects", "{", 412, tiad.KindGate, ""},
		{"no session before a bad body", false, "POST", "/v1/sessions/s999/compile", "{", 404, tiad.KindNotFound, ""},
		{"bad body before a bad name", false, "POST", "/v1/projects", map[string]any{"name": "CON", "extra": 1}, 400, tiad.KindBadRequest, "unknown field"},
		{"name before station", false, "POST", "/v1/projects", map[string]any{"name": "a/b", "station": badStation, "secrets": BenchSecrets}, 400, tiad.KindBadRequest, "project name"},
		{"station before busy", false, "POST", "/v1/projects", map[string]any{"name": "p", "station": badStation, "secrets": BenchSecrets}, 400, tiad.KindBadRequest, "cpu.address"},
		{"station before password", false, "POST", "/v1/projects", map[string]any{"name": "q", "station": badStation}, 400, tiad.KindBadRequest, "cpu.address"},
		{"bad body before nothing selected", false, "POST", "/v1/sessions/" + s.ID + "/compile", map[string]any{"hardwar": true}, 400, tiad.KindBadRequest, "unknown field"},
	}
	for _, tc := range cases {
		if tc.gateFails {
			a.SetGates(t, []tiad.Gate{{Name: "licence", State: tiad.GateFailed, Detail: "trial expired"}})
		}
		status, b := rawJSON(t, c, tc.method, tc.path, tc.body)
		if tc.gateFails {
			a.SetGates(t, nil)
		}
		if status != tc.wantStatus || envelopeKind(b) != tc.wantKind || !strings.Contains(errMessage(b), tc.wantMsg) {
			t.Errorf("%s: HTTP %d %s, want %d %s %q", tc.name, status, b, tc.wantStatus, tc.wantKind, tc.wantMsg)
		}
	}
	// The token comes before everything, a failing gate included.
	a.SetGates(t, []tiad.Gate{{Name: "licence", State: tiad.GateFailed}})
	h := http.Header{"Content-Type": {"application/json"}}
	if status, b := rawWith(t, c, "POST", "/v1/projects", h, []byte("{")); status != http.StatusUnauthorized {
		t.Errorf("no token with a failed gate = HTTP %d %s, want 401", status, b)
	}
	a.SetGates(t, nil)
}
