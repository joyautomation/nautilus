package snmp_test

// foreign_test.go is the foreign-implementation suite: the real Driver
// against snmpsim (LeXtudio's maintained fork, on pysnmp) replaying our own
// fixture walks, instead of against our own agent. The agent and the client
// share gosnmp's codec, so they can only disagree where gosnmp disagrees
// with itself; snmpsim is somebody else's BER, somebody else's GetBulk and
// somebody else's USM — the parts a site's switch will exercise.
//
// Gated on NAUTILUS_SNMP_SIM=host:port (scripts/snmp-sim.sh starts the sim
// and prints the exact line), like modbus' NAUTILUS_MODBUS_SIM — a normal
// `go test ./...` skips it. The data the sim serves is committed under
// testdata/sim/ and pinned to the walks by TestSimDataMatchesWalks, which
// always runs: `go test ./snmp/ -run TestSimData -args -update` rewrites it.
//
// What it must catch (brief §9.5): decode agreement on every binding kind,
// a GetBulk across a table end and into endOfMibView, a v3 authentication
// failure as a legible error (not a timeout), and a counter that moves →
// the right rate.

import (
	"context"
	"flag"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/hw"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/snmp"
	"github.com/joyautomation/nautilus/snmp/agent"
	"github.com/joyautomation/nautilus/snmp/codegen"
)

var update = flag.Bool("update", false, "rewrite testdata/sim from the walks")

// The sim's USM user (scripts/snmp-sim.sh passes the same). Test keys, not
// secrets — and still handed to the driver through the environment, the
// only way it takes credentials.
const (
	simUser    = "nautilus"
	simAuthKey = "nautilus-auth-key"
	simPrivKey = "nautilus-priv-key"
)

// simData: which walk each data file replays (the file name is the v2c
// community and the v3 context), and the OIDs snmpsim's numeric variation
// module moves with the wall clock: value = initial + rate × seconds since
// the sim started, initial being the walk's own value ({value}).
var simData = []struct {
	file, walk string
	variations map[string]string
}{
	{"switch", "switch.snmpwalk", map[string]string{
		"1.3.6.1.2.1.31.1.1.1.6.1": "70:numeric|rate=125000,initial={value}", // ifHCInOctets.1: 1 Mb/s
		"1.3.6.1.2.1.2.2.1.14.3":   "65:numeric|rate=40,initial={value}",     // ifInErrors.3: 40 errors/s
	}},
	{"ups", "ups.snmpwalk", nil},
	{"pdu", "pdu.snmpwalk", nil},
}

// The committed .snmprec files are exactly the walks, plus the variations —
// so the foreign stack serves what the in-repo tests assert against.
func TestSimDataMatchesWalks(t *testing.T) {
	for _, d := range simData {
		w := loadWalk(t, d.walk)
		vars := map[string]string{}
		for oid, v := range d.variations {
			vb, ok := w.Get(oid)
			if !ok {
				t.Fatalf("variation on %s, which %s does not hold", oid, d.walk)
			}
			vars[oid] = strings.ReplaceAll(v, "{value}", vb.ValueString())
		}
		got := w.Snmprec(vars)
		path := "testdata/sim/" + d.file + ".snmprec"
		if *update {
			if err := os.MkdirAll("testdata/sim", 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%v (go test ./snmp/ -run TestSimData -args -update)", err)
		}
		if string(want) != string(got) {
			t.Errorf("%s drifted from %s — regenerate with -update", path, d.walk)
		}
	}
}

func simAddr(t *testing.T) (string, int) {
	t.Helper()
	addr := os.Getenv("NAUTILUS_SNMP_SIM")
	if addr == "" {
		t.Skip("set NAUTILUS_SNMP_SIM=host:port (see scripts/snmp-sim.sh) to run the foreign-stack test")
	}
	return splitAddr(t, addr)
}

// simManifest imports a fixture pointed at the sim, as v2c with the data
// file's community or as v3 authPriv (SHA-256/AES-128) in its context.
func simManifest(t *testing.T, file, fixture, tag string, v3 bool) snmp.Manifest {
	t.Helper()
	host, port := simAddr(t)
	o := codegen.Options{Tag: tag, Host: host, Port: port}
	if v3 {
		o.Version, o.User, o.Auth, o.Priv, o.Context = snmp.V3, simUser, snmp.AuthSHA256, snmp.PrivAES128, file
	}
	out, err := codegen.Generate(loadWalk(t, fixture), o)
	if err != nil {
		t.Fatal(err)
	}
	m := out.Manifest
	s := &m.Sources[0]
	one := 1
	s.Timeout, s.Retries, s.Interval, s.StaleAfter = 500*time.Millisecond, &one, 100*time.Millisecond, 2*time.Second
	s.MaxRepetitions = 7 // several PDUs per column, so continuation is exercised
	if v3 {
		t.Setenv(s.AuthEnv, simAuthKey)
		t.Setenv(s.PrivEnv, simPrivKey)
	} else {
		t.Setenv(s.CommunityEnv, file)
	}
	return m
}

// kindsTag binds one member per wire type and binding kind the profiles do
// not already cover — hex OCTET STRING, OBJECT IDENTIFIER, offset, a
// Gauge32 at its maximum, a Counter64 read as a value — plus rows past a
// table's end and past the end of the MIB view.
func kindsTag(src string) []snmp.Tag {
	return []snmp.Tag{{Name: src + "_Kinds", Type: "Switch", Source: src, Members: map[string]snmp.Member{
		"Name":    {OID: "1.3.6.1.2.1.2.2.1.6.1"},                                          // Hex-STRING MAC
		"Model":   {OID: "1.3.6.1.2.1.1.2.0"},                                              // OBJECT IDENTIFIER
		"Serial":  {OID: "1.3.6.1.2.1.1.1.0"},                                              // long DisplayString
		"UptimeS": {OID: "1.3.6.1.2.1.1.3.0", Binding: hw.Binding{Scale: 0.01, Offset: 5}}, // TimeTicks, scale+offset
		"CpuPct":  {OID: "1.3.6.1.2.1.2.2.1.5.25", Binding: hw.Binding{Scale: 1e-9}},       // Gauge32 4294967295
		"MemPct":  {OID: "1.3.6.1.2.1.31.1.1.1.10.26"},                                     // Counter64 as a value
		"TempC":   {OID: "1.3.6.1.2.1.99.1.1.1.4.3", Binding: hw.Binding{Scale: 0.1}},      // INTEGER, scaled
		"Fault":   {OID: "1.3.6.1.2.1.2.2.1.8.20", Binding: hw.Binding{Map: map[string]any{"1": false, "2": true}}},
	}}, {
		// Four rows past the last ifAlias (ifIndex 2001): the column walk runs
		// off the end of ifXTable into ENTITY-MIB and must stop there.
		Name: src + "_PastTable", Type: "SwitchPort", Source: src, Members: map[string]snmp.Member{
			"Alias": {OID: "1.3.6.1.2.1.31.1.1.1.18.3001"}, "Name": {OID: "1.3.6.1.2.1.31.1.1.1.18.3002"},
		},
	}, {
		Name: src + "_PastTable2", Type: "SwitchPort", Source: src, Members: map[string]snmp.Member{
			"Alias": {OID: "1.3.6.1.2.1.31.1.1.1.18.3003"}, "Name": {OID: "1.3.6.1.2.1.31.1.1.1.18.3004"},
		},
	}, {
		// Rows past the last PoE row — the last OID the sim holds: the walk
		// ends in endOfMibView.
		Name: src + "_PastEnd", Type: "SwitchPort", Source: src, Members: map[string]snmp.Member{
			"OperUp": {OID: "1.3.6.1.2.1.105.1.1.1.6.1.9", Binding: hw.Binding{Eq: 3}},
			"PoeOn":  {OID: "1.3.6.1.2.1.105.1.1.1.6.1.10", Binding: hw.Binding{Eq: 3}},
		},
	}, {
		Name: src + "_PastEnd2", Type: "SwitchPort", Source: src, Members: map[string]snmp.Member{
			"OperUp": {OID: "1.3.6.1.2.1.105.1.1.1.6.1.11", Binding: hw.Binding{Eq: 3}},
			"PoeOn":  {OID: "1.3.6.1.2.1.105.1.1.1.6.1.12", Binding: hw.Binding{Eq: 3}},
		},
	}}
}

func startDriver(t *testing.T, m snmp.Manifest) *snmp.Driver {
	t.Helper()
	d, err := snmp.New(m, snmp.WithLogger(quiet()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.Start(ctx)
	t.Cleanup(func() { cancel(); d.Stop() })
	return d
}

// agentTwin serves the same walk from the in-repo agent, with the same
// manifest re-pointed at it: the reference the sim's answers must match.
func agentTwin(t *testing.T, m snmp.Manifest, fixture string) *snmp.Driver {
	t.Helper()
	a := agent.New(loadWalk(t, fixture), "twin")
	a.SetLogger(quiet())
	if err := a.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Stop)
	host, port := splitAddr(t, a.Addr())
	twin := m
	twin.Sources = append([]snmp.Source(nil), m.Sources...)
	s := &twin.Sources[0]
	s.Host, s.Port, s.Version = host, port, snmp.V2c
	s.User, s.Auth, s.Priv, s.Context, s.AuthEnv, s.PrivEnv = "", "", "", "", "", ""
	s.CommunityEnv = "NAUTILUS_TWIN_COMMUNITY"
	t.Setenv("NAUTILUS_TWIN_COMMUNITY", "twin")
	return startDriver(t, twin)
}

func settled(t *testing.T, d *snmp.Driver, id string) nio.Values {
	t.Helper()
	waitFor(t, id+" online", func() bool { return online(d, id) })
	waitPolls(t, d, id, 2)
	return read(t, d)
}

// Every member of every tag, off snmpsim, equals the same member off the
// in-repo agent serving the same walk — for all three profiles, over v2c
// and v3 — except the members the sim's variation module moves.
func TestForeignDecodeAgreement(t *testing.T) {
	simAddr(t)
	for _, c := range []struct {
		file, fixture, tag string
		v3                 bool
	}{
		{"switch", "switch.snmpwalk", "SW1", false},
		{"switch", "switch.snmpwalk", "SW1", true},
		{"ups", "ups.snmpwalk", "UPS1", false},
		{"pdu", "pdu.snmpwalk", "PDU1", true},
	} {
		name := c.file + "/v2c"
		if c.v3 {
			name = c.file + "/v3"
		}
		t.Run(name, func(t *testing.T) {
			m := simManifest(t, c.file, c.fixture, c.tag, c.v3)
			if c.file == "switch" {
				m.Tags = append(m.Tags, kindsTag(c.tag)...)
			}
			simDrv := startDriver(t, m)
			sim := settled(t, simDrv, c.tag)
			ref := settled(t, agentTwin(t, m, c.fixture), c.tag)
			moving := map[string]bool{"SW1_Port01.InBps": true, "SW1_Port01.InPct": true, "SW1_Port03.InErrors": true, "SW1_Port03.ErrorRate": true}
			compared := 0
			for _, tg := range m.Tags {
				sv, sok := sim[tg.Name].(ir.Value)
				rv, rok := ref[tg.Name].(ir.Value)
				if sok != rok {
					t.Errorf("%s delivered by sim=%v agent=%v", tg.Name, sok, rok)
					continue
				}
				if !sok {
					continue
				}
				for i, f := range sv.Struct.Fields {
					if moving[tg.Name+"."+f.Name] {
						continue
					}
					compared++
					if !sameValue(sv.Fld[i], rv.Fld[i]) {
						t.Errorf("%s.%s: sim %v, agent %v", tg.Name, f.Name, show(sv.Fld[i]), show(rv.Fld[i]))
					}
				}
			}
			if compared == 0 {
				t.Fatal("nothing compared")
			}
			if c.file != "switch" {
				return
			}
			// The kinds, against the walk itself.
			k := c.tag + "_Kinds"
			for mname, want := range map[string]any{
				"Name":    "00:00:5E:00:53:01",
				"Model":   "1.3.6.1.4.1.32473.1.28",
				"UptimeS": int64(1234573),
				"CpuPct":  4.294967295,
				"TempC":   28.4,
				"Fault":   true, // port 20 is admin down, so oper down(2)
			} {
				got := member(t, sim, k, mname)
				if !sameValue(got, want) {
					t.Errorf("%s.%s = %v, want %v", k, mname, show(got), want)
				}
			}
			// Off the end of a table and of the MIB view: those tags Bad,
			// everything else Good, the source online.
			q := simDrv.Quality()
			for _, bad := range []string{"_PastTable", "_PastTable2", "_PastEnd", "_PastEnd2"} {
				if q[c.tag+bad] != nio.NotConnected && q[c.tag+bad] != nio.Bad {
					t.Errorf("%s quality = %v, want Bad/NotConnected", c.tag+bad, q[c.tag+bad])
				}
			}
			for name, qq := range q {
				if !strings.Contains(name, "_Past") {
					t.Errorf("%s is %v on a clean sim", name, qq)
				}
			}
		})
	}
}

func sameValue(a ir.Value, b any) bool {
	switch w := b.(type) {
	case ir.Value:
		if a.Kind != w.Kind {
			return false
		}
		switch a.Kind {
		case ir.TypeReal:
			return a.F == w.F || math.Abs(a.F-w.F) < 1e-9*math.Max(1, math.Abs(w.F))
		case ir.TypeInt:
			return a.I == w.I
		case ir.TypeBool:
			return a.B == w.B
		case ir.TypeString:
			return a.S == w.S
		}
	case string:
		return a.Kind == ir.TypeString && a.S == w
	case bool:
		return a.Kind == ir.TypeBool && a.B == w
	case int64:
		return a.Kind == ir.TypeInt && a.I == w
	case float64:
		return a.Kind == ir.TypeReal && math.Abs(a.F-w) < 1e-9*math.Max(1, math.Abs(w))
	}
	return false
}

func show(v ir.Value) any {
	switch v.Kind {
	case ir.TypeReal:
		return v.F
	case ir.TypeInt:
		return v.I
	case ir.TypeBool:
		return v.B
	}
	return v.S
}

// A counter the sim moves with the wall clock reads as its rate.
func TestForeignCounterRate(t *testing.T) {
	m := simManifest(t, "switch", "switch.snmpwalk", "SW1", false)
	drv := startDriver(t, m)
	waitFor(t, "SW1 online", func() bool { return online(drv, "SW1") })
	waitPolls(t, drv, "SW1", 5)
	vals := read(t, drv)
	if in := member(t, vals, "SW1_Port01", "InBps").F; math.Abs(in-1e6)/1e6 > 0.1 {
		t.Errorf("InBps = %v, want ≈ 1e6 (125000 octets/s × 8)", in)
	}
	if er := member(t, vals, "SW1_Port03", "ErrorRate").F; math.Abs(er-40) > 8 {
		t.Errorf("ErrorRate = %v, want ≈ 40", er)
	}
}

// Every privacy flavour the manifest accepts interoperates with pysnmp's:
// aes256 is the Blumenthal key extension (pysnmp AES256BLMT, net-snmp's
// AES-256), aes256c the Reeder one (pysnmp AES256, Cisco's) — two
// incompatible algorithms that devices both call "AES-256" — and the
// legacy MD5/DES pair that is accepted with a warning.
func TestForeignPrivacyFlavours(t *testing.T) {
	for _, c := range []struct{ user, auth, priv string }{
		{"nautilus-aes256", snmp.AuthSHA1, snmp.PrivAES256},
		{"nautilus-aes256c", snmp.AuthSHA1, snmp.PrivAES256C},
		{"nautilus-legacy", snmp.AuthMD5, snmp.PrivDES},
	} {
		t.Run(c.priv, func(t *testing.T) {
			m := simManifest(t, "ups", "ups.snmpwalk", "UPS1", true)
			s := &m.Sources[0]
			s.User, s.Auth, s.Priv = c.user, c.auth, c.priv
			drv := startDriver(t, m)
			waitFor(t, "UPS1 online", func() bool { return online(drv, "UPS1") })
			if v := member(t, read(t, drv), "UPS1", "InputV").F; v != 121 {
				t.Errorf("InputV = %v", v)
			}
		})
	}
	// And the two AES-256s are NOT interchangeable once a key extension
	// runs (SHA-1's 20-byte key → 32): the diagnosis names privacy, the
	// half that differs. (Under SHA-256 they coincide — a site that
	// "works with either" is telling you its auth protocol, not that the
	// flavours are the same.)
	m := simManifest(t, "ups", "ups.snmpwalk", "UPS1", true)
	m.Sources[0].User, m.Sources[0].Auth, m.Sources[0].Priv = "nautilus-aes256", snmp.AuthSHA1, snmp.PrivAES256C
	drv := startDriver(t, m)
	waitFor(t, "error state", func() bool { return drv.Health().Sources[0].State == "error" })
	if e := drv.Health().Sources[0].LastError; !strings.Contains(e, "privacy failed") {
		t.Errorf("aes256c against a Blumenthal user: %s", e)
	}
}

// Wrong credentials are legible errors. v3: the agent answers a Report
// (usmStatsWrongDigests / usmStatsUnknownUserNames), so the error arrives
// in one round trip and names the cause — not a timeout. v2c: the agent
// drops the request (RFC 3584 §5.2.1 has no error for it), so it IS a
// timeout, and the message says what a timeout from a live agent means.
func TestForeignWrongCredentials(t *testing.T) {
	for _, c := range []struct {
		name  string
		edit  func(t *testing.T, m *snmp.Manifest)
		v3    bool
		want  string
		quick bool
	}{
		{"v3 wrong auth key", func(t *testing.T, m *snmp.Manifest) { t.Setenv(m.Sources[0].AuthEnv, "not-the-auth-key") }, true, "authentication failed", true},
		{"v3 unknown user", func(t *testing.T, m *snmp.Manifest) { m.Sources[0].User = "mallory" }, true, `does not know user "mallory"`, true},
		{"v3 wrong priv key", func(t *testing.T, m *snmp.Manifest) { t.Setenv(m.Sources[0].PrivEnv, "not-the-priv-key") }, true, "privacy failed", true},
		{"v2c wrong community", func(t *testing.T, m *snmp.Manifest) { t.Setenv(m.Sources[0].CommunityEnv, "nope") }, false, "wrong community", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := simManifest(t, "ups", "ups.snmpwalk", "UPS1", c.v3)
			c.edit(t, &m)
			t0 := time.Now()
			drv := startDriver(t, m)
			waitFor(t, "error state", func() bool { return drv.Health().Sources[0].State == "error" })
			e := drv.Health().Sources[0].LastError
			t.Logf("after %v: %s", time.Since(t0).Round(time.Millisecond), e)
			if c.want != "" && !strings.Contains(e, c.want) {
				t.Errorf("LastError = %q, want %q", e, c.want)
			}
			if c.quick && strings.Contains(e, "no answer within") {
				t.Errorf("a v3 auth failure must not read as a timeout: %s", e)
			}
			for _, secret := range []string{simAuthKey, simPrivKey, "not-the-auth-key", "not-the-priv-key", "nope"} {
				if strings.Contains(e, secret) {
					t.Errorf("LastError leaks a credential: %s", e)
				}
			}
			if online(drv, "UPS1") {
				t.Error("online with the wrong credentials")
			}
		})
	}
}
