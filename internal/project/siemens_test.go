package project

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
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
	st := s.Station.ExplicitStation()
	if len(st.Profinet.Devices) != 1 || st.Profinet.Devices[0].Name != "io-device-2" {
		t.Fatalf("explicit station keeps only explicit devices: %+v", st.Profinet.Devices)
	}
	if st.Modules[0].Slot != 2 || *st.Modules[0].InputStart != 0 {
		t.Fatalf("modules: %+v", st.Modules)
	}
}

func TestSiemensTargetRefusals(t *testing.T) {
	cases := []struct{ name, from, to, want string }{
		{"both kinds", "target:\n  siemens:", "target:\n  logix: {}\n  siemens:", "one kind at a time"},
		{"station rule", "profinet-name: plc-1", "profinet-name: PLC_1", "lowercase"},
		{"slot 1", "{slot: 2, order", "{slot: 1, order", "slot 1 is the CPU's own"},
		{"reference with a name", "          - profinet-manifest: profinet_device.yaml\n", "          - profinet-manifest: profinet_device.yaml\n            name: x\n", "takes only address"},
		{"reference outside", "profinet-manifest: profinet_device.yaml", "profinet-manifest: ../other/profinet_device.yaml", "outside the project"},
		{"reference without address", "            address: 192.0.2.20\n", "", "needs address"},
		{"unknown field", "      user: naut", "      user: naut\n      password: secret", "password"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(siemensManifest, c.from) {
				t.Fatalf("fixture lacks %q", c.from)
			}
			_, err := readSiemens(t, strings.Replace(siemensManifest, c.from, c.to, 1))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to contain %q", err, c.want)
			}
		})
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
		{"station", goKeys(SiemensStation{}), schemaKeys(s.Definitions["siemens-station"].Properties)},
		{"cpu", goKeys(SiemensCPU{}), schemaKeys(s.Definitions["siemens-cpu"].Properties)},
		{"web-server", goKeys(SiemensWebServer{}), schemaKeys(s.Definitions["siemens-web-server"].Properties)},
		{"web user", goKeys(SiemensWebUser{}), schemaKeys(s.Definitions["siemens-web-user"].Properties)},
		{"module", goKeys(SiemensModule{}), schemaKeys(s.Definitions["siemens-module"].Properties)},
		{"profinet", goKeys(SiemensProfinet{}), schemaKeys(s.Definitions["siemens-profinet"].Properties)},
		{"device", goKeys(SiemensDevice{}), schemaKeys(s.Definitions["siemens-device"].Properties)},
		{"device module", goKeys(SiemensDeviceModule{}), schemaKeys(s.Definitions["siemens-device-module"].Properties)},
		{"online", goKeys(SiemensOnline{}), schemaKeys(s.Definitions["siemens-online"].Properties)},
	} {
		if !reflect.DeepEqual(tc.got, tc.schema) {
			t.Errorf("%s: the loader accepts %v, the schema declares %v; update %s", tc.what, tc.got, tc.schema, schemaPath)
		}
	}
}
