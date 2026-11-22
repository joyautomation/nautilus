package project

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/siemens/station"
)

const siemensManifest = `name: bench
tasks:
  - program: main.st
    scan: 100ms
target:
  siemens:
    agent: http://127.0.0.1:8190
    station:
      cpu:
        name: PLC_1
        order: 6ES7 510-1DK03-0AB0
        firmware: V3.1
        address: 192.0.2.10
        subnet-mask: 255.255.255.0
        profinet-name: plc-1
      web-server:
        enabled: true
        users: [{name: naut, rights: [read, write, mode]}]
      modules:
        - {slot: 2, order: 6ES7 131-6BH01-0BA0, input-start: 0}
      profinet:
        update-time: 16ms
        devices:
          - profinet-manifest: profinet_device.yaml
            address: 192.0.2.20
          - gsdml: GSDML-V2.4-Example-Device-20261117.xml
            dap: DAP_1
            name: io-device-2
            address: 192.0.2.21
            modules: [{slot: 1, subslot: 1, module: ID_Mod_In4Out2}]
    online:
      user: naut
`

func readSiemens(t *testing.T, manifest string) (*Manifest, error) {
	t.Helper()
	return ReadManifest(fstest.MapFS{"nautilus.yaml": {Data: []byte(manifest)}}, "")
}

func TestSiemensTargetParses(t *testing.T) {
	m, err := readSiemens(t, siemensManifest)
	if err != nil {
		t.Fatal(err)
	}
	s := m.Target.Siemens
	if s == nil || s.Station.CPU.Order != "6ES7 510-1DK03-0AB0" || s.Online.User != "naut" {
		t.Fatalf("target.siemens: %+v", s)
	}
	devs := s.Station.Profinet.Devices
	if !devs[0].IsReference() || devs[1].IsReference() {
		t.Fatalf("devices: %+v", devs)
	}
	if s.Station.Modules[0].Slot != 2 || *s.Station.Modules[0].InputStart != 0 {
		t.Fatalf("modules: %+v", s.Station.Modules)
	}
	if ps := m.Target.Problems(); len(ps) != 0 {
		t.Fatalf("problems in a good target: %v", ps)
	}
}

// TestSiemensTargetProblems: a broken or unfinished station still loads
// (the editor keeps every tag), and Problems names the YAML path, with the
// manifest's own device index and kebab-case keys.
func TestSiemensTargetProblems(t *testing.T) {
	cases := []struct{ name, from, to, path, want string }{
		{"both kinds", "target:\n  siemens:", "target:\n  logix: {}\n  siemens:", "target", "one kind at a time"},
		{"station rule", "profinet-name: plc-1", "profinet-name: PLC_1", "target.siemens.station.cpu.profinet-name", "lowercase"},
		{"kebab key", "subnet-mask: 255.255.255.0", "subnet-mask: banana", "target.siemens.station.cpu.subnet-mask", "not an IPv4 mask"},
		{"slot 1", "{slot: 2, order", "{slot: 1, order", "target.siemens.station.modules[0].slot", "slot 1 is the CPU's own"},
		{"explicit device index", "name: io-device-2", "name: IO", "target.siemens.station.profinet.devices[1].name", "lowercase"},
		{"reference with a name", "          - profinet-manifest: profinet_device.yaml\n", "          - profinet-manifest: profinet_device.yaml\n            name: x\n", "target.siemens.station.profinet.devices[0]", "takes only address"},
		{"reference outside", "profinet-manifest: profinet_device.yaml", "profinet-manifest: ../other/profinet_device.yaml", "target.siemens.station.profinet.devices[0].profinet-manifest", "outside the project"},
		{"reference without address", "            address: 192.0.2.20\n", "", "target.siemens.station.profinet.devices[0].address", "not an IPv4 address"},
		{"reference address junk", "address: 192.0.2.20", "address: banana", "target.siemens.station.profinet.devices[0].address", "not an IPv4 address"},
		{"reference address is the cpu's", "address: 192.0.2.20", "address: 192.0.2.10", "target.siemens.station.profinet.devices[0].address", "the CPU's"},
		{"two devices one address", "address: 192.0.2.21", "address: 192.0.2.20", "target.siemens.station.profinet.devices[1].address", "devices[0]'s"},
		{"agent with credentials", "agent: http://127.0.0.1:8190", "agent: https://u:secret@host:8190", "target.siemens.agent", "user info"},
		{"agent not a url", "agent: http://127.0.0.1:8190", "agent: tiad-box", "target.siemens.agent", "not an http(s) URL"},
		{"cert not hex", "      user: naut", "      user: naut\n      cert-sha256: abc", "target.siemens.online.cert-sha256", "not a SHA-256"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(siemensManifest, c.from) {
				t.Fatalf("fixture lacks %q", c.from)
			}
			m, err := readSiemens(t, strings.Replace(siemensManifest, c.from, c.to, 1))
			if err != nil {
				t.Fatalf("a station problem must not fail loading: %v", err)
			}
			for _, p := range m.Target.Problems() {
				if p.Path == c.path && strings.Contains(p.Message, c.want) {
					return
				}
			}
			t.Fatalf("want %s: …%s…, got %v", c.path, c.want, m.Target.Problems())
		})
	}
}

func TestSiemensUnknownFieldStillFailsLoading(t *testing.T) {
	_, err := readSiemens(t, strings.Replace(siemensManifest, "      user: naut", "      user: naut\n      password: secret", 1))
	if err == nil || !strings.Contains(err.Error(), "password") {
		t.Fatalf("a password: key must be refused by the strict decoder: %v", err)
	}
}

func TestSiemensCertPinAcceptsColons(t *testing.T) {
	pin := strings.Repeat("ab:", 31) + "ab"
	m, err := readSiemens(t, strings.Replace(siemensManifest, "      user: naut", "      user: naut\n      cert-sha256: \""+pin+"\"", 1))
	if err != nil {
		t.Fatal(err)
	}
	if ps := m.Target.Problems(); len(ps) != 0 {
		t.Fatalf("%v", ps)
	}
}

// TestSiemensSchemaMatchesLoader keeps the editor's schema for
// target.siemens in step with what the loader accepts, as
// TestSchemaMatchesLoader does for the rest of the manifest.
func TestSiemensSchemaMatchesLoader(t *testing.T) {
	s := loadSchema(t)
	var target schemaNode
	if err := json.Unmarshal(s.Properties["target"], &target); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		what   string
		got    []string
		schema []string
	}{
		{"target", goKeys(TargetConfig{}), schemaKeys(target.Properties)},
		{"target.siemens", goKeys(SiemensTarget{}), schemaKeys(s.Definitions["siemens-target"].Properties)},
		{"station", goKeys(station.Station{}), schemaKeys(s.Definitions["siemens-station"].Properties)},
		{"cpu", goKeys(station.CPU{}), schemaKeys(s.Definitions["siemens-cpu"].Properties)},
		{"web-server", goKeys(station.WebServer{}), schemaKeys(s.Definitions["siemens-web-server"].Properties)},
		{"web user", goKeys(station.WebUser{}), schemaKeys(s.Definitions["siemens-web-user"].Properties)},
		{"module", goKeys(station.Module{}), schemaKeys(s.Definitions["siemens-module"].Properties)},
		{"profinet", goKeys(station.Profinet{}), schemaKeys(s.Definitions["siemens-profinet"].Properties)},
		{"device", goKeys(station.Device{}), schemaKeys(s.Definitions["siemens-device"].Properties)},
		{"device module", goKeys(station.DeviceModule{}), schemaKeys(s.Definitions["siemens-device-module"].Properties)},
		{"online", goKeys(SiemensOnline{}), schemaKeys(s.Definitions["siemens-online"].Properties)},
	} {
		if !reflect.DeepEqual(tc.got, tc.schema) {
			t.Errorf("%s: the loader accepts %v, the schema declares %v; update %s", tc.what, tc.got, tc.schema, schemaPath)
		}
	}
}

func TestSiemensBlockPrefixRules(t *testing.T) {
	str := func(s string) *string { return &s }
	if got := (*SiemensTarget)(nil).Prefix(); got != DefaultSiemensBlockPrefix || DefaultSiemensBlockPrefix != "naut_" {
		t.Fatalf("default %q", got)
	}
	if got := (&SiemensTarget{}).Prefix(); got != "naut_" {
		t.Fatalf("unset %q", got)
	}
	for p, ok := range map[string]bool{"naut_": true, "_x1": true, "A": true, "": false, "1a": false, "a b": false, "a-b": false, `a"`: false} {
		if err := CheckBlockPrefix(p); (err == nil) != ok {
			t.Errorf("CheckBlockPrefix(%q) = %v, want ok=%v", p, err, ok)
		}
	}
	tc := &TargetConfig{Siemens: &SiemensTarget{BlockPrefix: str("")}}
	found := false
	for _, p := range tc.Problems() {
		found = found || p.Path == "target.siemens.block-prefix"
	}
	if !found {
		t.Fatal("empty block-prefix not reported")
	}
}
