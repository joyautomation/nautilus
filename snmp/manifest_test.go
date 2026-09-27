package snmp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const goodManifest = `
sources:
  - id: SW1
    host: 192.0.2.2
    version: 2c
    community-env: SNMP_SW1_COMMUNITY
    timeout: 2s
    retries: 1
    max-repetitions: 10
    interval: 5s
    stale-after: 30s
    enable: CFG_PollSW1
  - id: UPS1
    host: 192.0.2.9
    port: 1161
    version: 3
    user: nautilus
    auth: sha256
    auth-env: SNMP_UPS1_AUTH
    priv: aes128
    priv-file: /run/secrets/ups1-priv
tags:
  - name: SW1_Port01
    type: SwitchPort
    source: SW1
    members:
      Index:     {const: 1}
      Name:      {oid: 1.3.6.1.2.1.31.1.1.1.1.1, scan-class: slow}
      AdminUp:   {oid: 1.3.6.1.2.1.2.2.1.7.1, eq: 1}
      OperUp:    {oid: .1.3.6.1.2.1.2.2.1.8.1, eq: 1}
      Down:      {derived: "AdminUp && !OperUp"}
      InBps:     {oid: 1.3.6.1.2.1.31.1.1.1.6.1, rate: true, width: 64, scale: 8}
  - name: UPS1
    type: UPS
    source: UPS1
    members:
      LowBattery: {oid: 1.3.6.1.2.1.33.1.2.1.0, map: {"1": false, "2": false, "3": true, "4": true}}
      InputV:     {oid: 1.3.6.1.2.1.33.1.3.3.1.3.1, scale: 0.1, offset: -1.5}
writes:
  - {name: UPS1_Cmd, tag: UPS1, member: OnBattery, oid: 1.3.6.1.2.1.33.1.8.1.0, set: {true: 1, false: 2}}
`

func TestManifestDecodes(t *testing.T) {
	m, err := ParseManifest([]byte(goodManifest))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	sw, ups := m.Sources[0], m.Sources[1]
	if sw.Timeout != 2*time.Second || *sw.Retries != 1 || sw.MaxRepetitions != 10 || sw.Interval != 5*time.Second ||
		sw.StaleAfter != 30*time.Second || sw.Enable != "CFG_PollSW1" || sw.Addr() != "192.0.2.2:161" {
		t.Errorf("SW1 = %+v", sw)
	}
	// "version: 3" is a YAML int; it must land as the string "3".
	if ups.version() != V3 || ups.Auth != AuthSHA256 || ups.PrivFile != "/run/secrets/ups1-priv" || ups.Addr() != "192.0.2.9:1161" {
		t.Errorf("UPS1 = %+v", ups)
	}
	port := m.Tags[0].Members
	if port["Index"].Const != 1 || port["Name"].ScanClass != "slow" || port["AdminUp"].Eq != 1 ||
		!port["InBps"].Rate || port["InBps"].Width != 64 || port["InBps"].Scale != 8 || port["Down"].Derived == "" {
		t.Errorf("port members = %+v", port)
	}
	if lb := m.Tags[1].Members["LowBattery"].Map; lb["3"] != true || lb["2"] != false {
		t.Errorf("map = %#v", lb)
	}
	// set: {true: 1, false: 2} — YAML bool keys land as "true"/"false".
	if w := m.Writes[0]; w.Set["true"] != 1 || w.Set["false"] != 2 {
		t.Errorf("set = %#v", w.Set)
	}
	if _, err := New(m, WithScanClass("slow", time.Minute)); err != nil {
		t.Fatalf("New: %v", err)
	}
}

// A typo anywhere is an error with a line, including inside a member —
// where the keys come from the embedded hw.Binding.
func TestManifestRejectsUnknownKeys(t *testing.T) {
	for _, tc := range []struct{ name, edit, want string }{
		{"source key", "    retries: 1\n", "    retires: 1\n"},
		{"member key", "eq: 1}\n      OperUp", "equals: 1}\n      OperUp"},
		{"binding key", "rate: true, width: 64", "rate: true, wdith: 64"},
		{"top level", "writes:\n", "write:\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := strings.Replace(goodManifest, tc.edit, tc.want, 1)
			if src == goodManifest {
				t.Fatal("edit did not apply")
			}
			_, err := ParseManifest([]byte(src))
			if err == nil || !strings.Contains(err.Error(), "line") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestManifestValidate(t *testing.T) {
	base := func() Manifest {
		m, err := ParseManifest([]byte(goodManifest))
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	for _, tc := range []struct {
		name string
		edit func(m *Manifest)
		want string
	}{
		{"no host", func(m *Manifest) { m.Sources[0].Host = "" }, "missing host"},
		{"dup source", func(m *Manifest) { m.Sources[1].ID = "SW1" }, "duplicate source"},
		{"bad version", func(m *Manifest) { m.Sources[0].Version = "1" }, `version "1"`},
		{"v2c no community", func(m *Manifest) { m.Sources[0].CommunityEnv = "" }, "needs community-env or community-file"},
		{"v2c both", func(m *Manifest) { m.Sources[0].CommunityFile = "/x" }, "alternatives"},
		{"v2c with v3 keys", func(m *Manifest) { m.Sources[0].User = "u" }, "SNMPv3 keys"},
		{"v3 community", func(m *Manifest) { m.Sources[1].CommunityEnv = "X" }, "v2c keys"},
		{"v3 no user", func(m *Manifest) { m.Sources[1].User = "" }, "needs user"},
		{"v3 auth unnamed", func(m *Manifest) { m.Sources[1].AuthEnv = "" }, "auth: sha256 needs auth-env or auth-file"},
		{"v3 auth both", func(m *Manifest) { m.Sources[1].AuthFile = "/x" }, "auth-env and auth-file are alternatives"},
		{"v3 priv without auth", func(m *Manifest) { m.Sources[1].Auth, m.Sources[1].AuthEnv = "", "" }, "priv without auth"},
		{"v3 bad auth", func(m *Manifest) { m.Sources[1].Auth = "sha999" }, `auth "sha999"`},
		{"v3 bad priv", func(m *Manifest) { m.Sources[1].Priv = "3des" }, `priv "3des"`},
		{"v3 priv key without priv", func(m *Manifest) { m.Sources[1].Priv = "" }, "priv-env/priv-file without priv"},
		{"negative retries", func(m *Manifest) { n := -1; m.Sources[0].Retries = &n }, "retries"},
		{"unknown source", func(m *Manifest) { m.Tags[0].Source = "SW9" }, `unknown source "SW9"`},
		{"unknown type", func(m *Manifest) { m.Tags[0].Type = "Router" }, `unknown type "Router"`},
		{"unknown member", func(m *Manifest) { m.Tags[0].Members["Speed"] = Member{OID: "1.3.6.1.2.1.2.2.1.5.1"} }, `no member "Speed"`},
		{"missing oid", func(m *Manifest) { mb := m.Tags[0].Members["OperUp"]; mb.OID = ""; m.Tags[0].Members["OperUp"] = mb }, "needs an oid"},
		{"oid with const", func(m *Manifest) {
			mb := m.Tags[0].Members["Index"]
			mb.OID = "1.3.6.1.2.1.2.2.1.1.1"
			m.Tags[0].Members["Index"] = mb
		}, "static member polls nothing"},
		{"bad oid", func(m *Manifest) {
			mb := m.Tags[0].Members["OperUp"]
			mb.OID = "ifOperStatus.1"
			m.Tags[0].Members["OperUp"] = mb
		}, "no MIB names"},
		{"eq on a real", func(m *Manifest) {
			m.Tags[1].Members["InputV"] = Member{OID: "1.3.6.1.2.1.33.1.3.3.1.3.1"}
			mb := m.Tags[1].Members["InputV"]
			mb.Eq = 1
			m.Tags[1].Members["InputV"] = mb
		}, "eq: makes a BOOL"},
		{"write without oid", func(m *Manifest) { m.Writes[0].OID = "" }, "needs an oid"},
		{"dup tag", func(m *Manifest) { m.Tags[1].Name = "SW1_Port01" }, "duplicate tag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := base()
			tc.edit(&m)
			err := m.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// Credentials: never an error at New (check runs on laptops without the
// secrets), a warning naming the VARIABLE; resolved at connect time, and
// no message ever carries the value.
func TestCredentials(t *testing.T) {
	const secret = "s3cr3t-community-value"
	dir := t.TempDir()
	file := filepath.Join(dir, "priv")
	m, err := ParseManifest([]byte(strings.Replace(goodManifest, "/run/secrets/ups1-priv", file, 1)))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SNMP_SW1_COMMUNITY", "")
	os.Unsetenv("SNMP_SW1_COMMUNITY")
	t.Setenv("SNMP_UPS1_AUTH", "")
	os.Unsetenv("SNMP_UPS1_AUTH")

	if _, err := New(m, WithScanClass("slow", time.Minute)); err != nil {
		t.Fatalf("New must not need the secrets: %v", err)
	}
	warn := strings.Join(m.Warnings(), "\n")
	for _, want := range []string{"$SNMP_SW1_COMMUNITY is not set", "$SNMP_UPS1_AUTH is not set", file + " is not readable here (does not exist)"} {
		if !strings.Contains(warn, want) {
			t.Errorf("warnings missing %q:\n%s", want, warn)
		}
	}

	c := m.Sources[0].credentials()[0]
	if _, err := c.resolve("SW1"); err == nil || !strings.Contains(err.Error(), "SNMP_SW1_COMMUNITY") {
		t.Errorf("unset resolve = %v", err)
	}
	t.Setenv("SNMP_SW1_COMMUNITY", secret)
	if v, err := c.resolve("SW1"); err != nil || v != secret {
		t.Errorf("resolve = %q, %v", v, err)
	}
	if err := os.WriteFile(file, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pc := m.Sources[1].credentials()[1]
	if v, err := pc.resolve("UPS1"); err != nil || v != secret {
		t.Errorf("file resolve = %q, %v (trailing newline must be trimmed)", v, err)
	}
	if err := os.WriteFile(file, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := pc.resolve("UPS1"); err == nil || !strings.Contains(err.Error(), "is empty") {
		t.Errorf("empty file = %v", err)
	}
	t.Setenv("SNMP_UPS1_AUTH", secret)
	if w := strings.Join(m.Warnings(), "\n"); strings.Contains(w, secret) || strings.Contains(w, "SNMP_SW1_COMMUNITY") {
		t.Errorf("warnings after setting: %s", w)
	}

	// MD5/DES and noAuthNoPriv are warnings, not errors.
	m.Sources[1].Auth, m.Sources[1].Priv = AuthMD5, PrivDES
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	w := strings.Join(m.Warnings(), "\n")
	if !strings.Contains(w, "md5") || !strings.Contains(w, "des") {
		t.Errorf("legacy protocol warnings missing:\n%s", w)
	}
}
