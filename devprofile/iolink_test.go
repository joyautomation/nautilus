package devprofile

import (
	"strings"
	"testing"
)

func TestBuiltinIOLinkPorts(t *testing.T) {
	for id, want := range map[string]int{"pepperl-fuchs-ice3": 8, "ifm-al1590": 8, "sick-sig300": 0, "abb-acs355-fmbt21": 0} {
		p, err := loadBuiltin(id)
		if err != nil {
			t.Fatal(err)
		}
		if got := p.IOLinkPorts(); got != want {
			t.Errorf("%s: IOLinkPorts() = %d, want %d", id, got, want)
		}
	}
	pf, _ := loadBuiltin("pepperl-fuchs-ice3")
	s := pf.IOLink
	if s.PDIn.Form != FormU32 || PortPoint(s.PDIn.Point, 3) != "Port3_PdiU32" || PortPoint(s.VendorID, 8) != "Port8_VendorID" || s.PDOut == nil {
		t.Errorf("pepperl-fuchs-ice3 iolink: %+v", s)
	}
}

// iolinkProfile is a two-port master with every kind of point an iolink:
// block can name; block is its iolink: block.
func iolinkProfile(block string) string {
	return `profile: test-master
protocol: opcua
opcua: { namespaces: [urn:virtual:test] }
iolink:
` + block + `
points:
  - { name: "P{n}_In",     type: UDINT,  repeat: 2, node-id: "ns=1;s=P{n}/In", path: "P{n}/In",  unverified: true }
  - { name: "P{n}_Out",    type: UDINT,  repeat: 2, node-id: "ns=1;s=P{n}/Out", path: "P{n}/Out", writable: true, unverified: true }
  - { name: "P{n}_Hex",    type: STRING, repeat: 2, node-id: "ns=1;s=P{n}/Hex", path: "P{n}/Hex", unverified: true }
  - { name: "P{n}_Valid",  type: BOOL,   repeat: 2, node-id: "ns=1;s=P{n}/V", path: "P{n}/V",   unverified: true }
  - { name: "P{n}_Vendor", type: UINT,   repeat: 2, node-id: "ns=1;s=P{n}/Ven", path: "P{n}/Ven", unverified: true }
  - { name: "P{n}_Device", type: UDINT,  repeat: 2, node-id: "ns=1;s=P{n}/Dev", path: "P{n}/Dev", unverified: true }
`
}

func TestIOLinkBlock(t *testing.T) {
	ok := `  ports: 2
  pd-in: { point: "P{n}_In", form: u32 }
  pd-out: { point: "P{n}_Out", form: u32 }
  pd-valid: "P{n}_Valid"
  vendor-id: "P{n}_Vendor"
  device-id: "P{n}_Device"
  status: "P{n}_Hex"`
	p, err := ParseProfile([]byte(iolinkProfile(ok)), "test")
	if err != nil {
		t.Fatal(err)
	}
	if p.IOLinkPorts() != 2 {
		t.Errorf("IOLinkPorts() = %d", p.IOLinkPorts())
	}
	for name, c := range map[string]struct{ old, new, want string }{
		"no ports":               {"ports: 2", "ports: 0", "ports 0 is not 1-64"},
		"more ports than points": {"ports: 2", "ports: 3", "port 3's point P3_In is not in the profile"},
		"no pd-in":               {`  pd-in: { point: "P{n}_In", form: u32 }` + "\n", "", "pd-in is required"},
		"no form":                {`{ point: "P{n}_In", form: u32 }`, `{ point: "P{n}_In" }`, "pd-in: form is required"},
		"unknown form":           {`{ point: "P{n}_In", form: u32 }`, `{ point: "P{n}_In", form: bytes }`, `form "bytes" is not u32 or hex`},
		"no {n}":                 {`point: "P{n}_In"`, `point: "P1_In"`, "has no {n}"},
		"u32 on a STRING":        {`{ point: "P{n}_In", form: u32 }`, `{ point: "P{n}_Hex", form: u32 }`, "point P1_Hex is STRING"},
		"hex on a UDINT":         {`{ point: "P{n}_In", form: u32 }`, `{ point: "P{n}_In", form: hex }`, "point P1_In is UDINT"},
		"pd-in writable":         {`{ point: "P{n}_In", form: u32 }`, `{ point: "P{n}_Out", form: u32 }`, "pd-in: point P1_Out is writable"},
		"pd-out read-only":       {`{ point: "P{n}_Out", form: u32 }`, `{ point: "P{n}_In", form: u32 }`, "pd-out: point P1_In is not writable"},
		"pd-valid not BOOL":      {`pd-valid: "P{n}_Valid"`, `pd-valid: "P{n}_In"`, "pd-valid: point P1_In is UDINT"},
		"vendor without device":  {`  device-id: "P{n}_Device"` + "\n", "", "vendor-id and device-id go together"},
		"device id in 16 bits":   {`device-id: "P{n}_Device"`, `device-id: "P{n}_Vendor"`, "device-id: point P1_Vendor is UINT"},
		"unknown key":            {"ports: 2", "ports: 2\n  isdu: none", "field isdu not found"},
	} {
		if !strings.Contains(ok, c.old) {
			t.Fatalf("%s: the block has no %q", name, c.old)
		}
		_, err := ParseProfile([]byte(iolinkProfile(strings.Replace(ok, c.old, c.new, 1))), "test")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}
