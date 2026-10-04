package redfish

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const goodManifest = `
sources:
  - id: NODE1
    host: https://bmc1
    user: admin
    password-env: NODE1_BMC_PASSWORD
    tls:
      insecure: true
    timeout: 5s
    retries: 1
    interval: 15s
    stale-after: 1m
    enable: CFG_PollNode1
tags:
  - name: NODE1
    type: Server
    source: NODE1
    desc: "Example R1U"
    resource: /redfish/v1/Systems/1
    members:
      Online: {path: Id, exists: true}
      PowerOn: {path: PowerState, eq: "On"}
      Health: {path: Status.Health, map: {OK: 0, Warning: 1, Critical: 2}}
      Fault: {derived: "Health == 2"}
      MaxTempC: {resource: /redfish/v1/Chassis/1/Thermal, path: "Temperatures[*].ReadingCelsius", agg: max}
      FanCount: {const: 2}
      Model: {path: Model, scan-class: slow}
  - name: NODE1_Fan1
    type: Fan
    source: NODE1
    resource: /redfish/v1/Chassis/1/Thermal
    members:
      RPM: {path: "Fans[MemberId=0].Reading", scale: 1}
      Fault: {path: "Fans[MemberId=0].Status.Health", map: {OK: false, Warning: true, Critical: true}}
writes:
  - name: NODE1_PowerCmd
    tag: NODE1
    member: PowerOn
    target: /redfish/v1/Systems/1/Actions/ComputerSystem.Reset
`

func TestManifestParses(t *testing.T) {
	m, err := ParseManifest([]byte(goodManifest))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	s := m.Sources[0]
	if s.Timeout != 5*time.Second || s.Interval != 15*time.Second || s.StaleAfter != time.Minute || s.Retries != 1 || !s.TLS.Insecure {
		t.Fatalf("source = %+v", s)
	}
	srv := m.Tags[0].Members
	if srv["Health"].Map["Critical"] != 2 || srv["PowerOn"].Eq != "On" || srv["FanCount"].Const != 2 || srv["Model"].ScanClass != "slow" {
		t.Fatalf("hw.Binding keys did not decode inline: %+v", srv)
	}
	if e := srv["Online"].Exists; e == nil || !*e {
		t.Fatal("exists: true")
	}
	if srv["MaxTempC"].resourceOf(m.Tags[0]) != "/redfish/v1/Chassis/1/Thermal" || srv["Model"].resourceOf(m.Tags[0]) != "/redfish/v1/Systems/1" {
		t.Fatal("resource inheritance")
	}
}

func TestManifestStrictKeys(t *testing.T) {
	for _, bad := range []string{
		strings.Replace(goodManifest, "stale-after:", "staleafter:", 1),
		strings.Replace(goodManifest, "agg: max", "aggregate: max", 1),
		strings.Replace(goodManifest, "    tls:\n      insecure: true", "    tls:\n      insecure: true\n      verify: false", 1),
	} {
		if _, err := ParseManifest([]byte(bad)); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("a typo must fail decoding, got %v", err)
		}
	}
	if _, err := ParseManifest(nil); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("empty: %v", err)
	}
}

func TestManifestValidate(t *testing.T) {
	cases := []struct {
		name, from, to, want string
	}{
		{"no host", "    host: https://bmc1\n", "", "missing host"},
		{"bad scheme", "https://bmc1", "ftp://bmc1", "scheme"},
		{"path in host", "https://bmc1", "https://bmc1/redfish/v1", "implied"},
		{"user without password", "    password-env: NODE1_BMC_PASSWORD\n", "", "needs password-env"},
		{"both secrets", "    password-env: NODE1_BMC_PASSWORD\n", "    password-env: NODE1_BMC_PASSWORD\n    password-file: /run/secrets/bmc\n", "alternatives"},
		{"password in env key", "password-env: NODE1_BMC_PASSWORD", "password-env: hunter2!", "not an environment variable name"},
		{"password without user", "    user: admin\n", "", "without a user"},
		{"auth none with creds", "    user: admin\n", "    user: admin\n    auth: none\n", "auth: none with credentials"},
		{"bad auth", "    user: admin\n", "    user: admin\n    auth: digest\n", "auth \"digest\""},
		{"tls both", "      insecure: true\n", "      insecure: true\n      ca-file: /etc/bmc.pem\n", "alternatives"},
		{"bad id", "  - id: NODE1\n", "  - id: NODE-1\n", "[A-Za-z0-9_]"},
		{"negative", "retries: 1", "retries: -1", "negative retries"},
		{"unknown source", "    source: NODE1\n    desc", "    source: NODE9\n    desc", "unknown source"},
		{"unknown type", "type: Fan", "type: Blower", "unknown type"},
		{"unknown member", "      RPM:", "      Speed:", "no member \"Speed\""},
		{"no resource", "    resource: /redfish/v1/Chassis/1/Thermal\n", "", "no resource"},
		{"fragment resource", "resource: /redfish/v1/Chassis/1/Thermal\n    members:\n      RPM", "resource: /redfish/v1/Chassis/1/Thermal#/Fans/0\n    members:\n      RPM", "no fragment"},
		{"relative resource", "resource: /redfish/v1/Systems/1", "resource: Systems/1", "must start with /redfish/v1"},
		{"no path", "RPM: {path: \"Fans[MemberId=0].Reading\", scale: 1}", "RPM: {scale: 1}", "no path"},
		{"positional", "Fans[MemberId=0].Reading", "Fans[0].Reading", "positional index"},
		{"wildcard without agg", "\"Temperatures[*].ReadingCelsius\", agg: max", "\"Temperatures[*].ReadingCelsius\"", "add agg"},
		{"bad agg", "agg: max", "agg: median", "agg \"median\""},
		{"exists on real", "Online: {path: Id, exists: true}", "InletTempC: {path: Id, exists: true}", "exists: makes a BOOL"},
		{"eq on real", "RPM: {path: \"Fans[MemberId=0].Reading\", scale: 1}", "RPM: {path: \"Fans[MemberId=0].Reading\", eq: 1}", "eq: makes a BOOL"},
		{"const with path", "FanCount: {const: 2}", "FanCount: {const: 2, path: X}", "take no resource"},
		{"write on fan", "    tag: NODE1\n    member", "    tag: NODE1_Fan1\n    member", "acts on a Server"},
		{"write member", "member: PowerOn", "member: Health", "reads back through PowerOn"},
		{"write target", "Actions/ComputerSystem.Reset", "Actions/Manager.Reset", "not a ComputerSystem.Reset"},
		{"write clash", "  - name: NODE1_PowerCmd", "  - name: NODE1_Fan1", "same name as a struct tag"},
	}
	for _, c := range cases {
		src := strings.Replace(goodManifest, c.from, c.to, 1)
		if src == goodManifest {
			t.Fatalf("%s: substitution did not apply", c.name)
		}
		m, err := ParseManifest([]byte(src))
		if err != nil {
			t.Errorf("%s: parse: %v", c.name, err)
			continue
		}
		err = m.Validate()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: Validate = %v, want %q", c.name, err, c.want)
		}
	}
}

// A secret must never reach an error message, whichever way it was
// (wrongly) written into the manifest.
func TestManifestNeverEchoesSecrets(t *testing.T) {
	src := strings.Replace(goodManifest, "https://bmc1", "https://admin:hunter2@bmc1", 1)
	m, err := ParseManifest([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	err = m.Validate()
	if err == nil || strings.Contains(err.Error(), "hunter2") || !strings.Contains(err.Error(), "carries credentials") {
		t.Fatalf("userinfo in host: %v", err)
	}
	if _, err := New(m); err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("New: %v", err)
	}
}

func TestManifestWarningsAndPassword(t *testing.T) {
	m, _ := ParseManifest([]byte(goodManifest))
	t.Setenv("NODE1_BMC_PASSWORD", "")
	os.Unsetenv("NODE1_BMC_PASSWORD")
	w := m.Warnings()
	if len(w) != 1 || !strings.Contains(w[0], "NODE1_BMC_PASSWORD is not set") {
		t.Fatalf("warnings = %v", w)
	}
	// Unset is a warning, never an error: New must succeed (naut check runs
	// on a laptop without the secret).
	if _, err := New(m, WithScanClass("slow", time.Minute)); err != nil {
		t.Fatalf("New with the password unset: %v", err)
	}
	if _, err := m.Sources[0].password(); err == nil || !strings.Contains(err.Error(), "NODE1_BMC_PASSWORD is not set") {
		t.Fatalf("password() = %v", err)
	}
	t.Setenv("NODE1_BMC_PASSWORD", "s3cret")
	if len(m.Warnings()) != 0 {
		t.Fatal("set variable still warns")
	}
	if p, err := m.Sources[0].password(); err != nil || p != "s3cret" {
		t.Fatalf("password() = %q, %v", p, err)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "bmc")
	_ = os.WriteFile(f, []byte("fromfile\n"), 0o600)
	m.Sources[0].PasswordEnv, m.Sources[0].PasswordFile = "", f
	if p, err := m.Sources[0].password(); err != nil || p != "fromfile" {
		t.Fatalf("password-file: %q, %v", p, err)
	}
	m.Sources[0].PasswordFile = filepath.Join(dir, "missing")
	if w := m.Warnings(); len(w) != 1 || !strings.Contains(w[0], "not readable") {
		t.Fatalf("missing file warnings = %v", w)
	}
}
