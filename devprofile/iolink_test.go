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
docs: [{ id: man, title: "The test master's manual" }]
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
  pd-in: { point: "P{n}_In", form: u32, doc: "man p. 1" }
  pd-out: { point: "P{n}_Out", form: u32, unverified: true }
  pd-valid: "P{n}_Valid"
  vendor-id: "P{n}_Vendor"
  device-id: "P{n}_Device"
  status: "P{n}_Hex"`
	p, err := ParseProfile([]byte(iolinkProfile(ok)), "test")
	if err != nil {
		t.Fatal(err)
	}
	if p.IOLinkPorts() != 2 || p.IOLink.PDIn.Doc != "man p. 1" || !p.IOLink.PDOut.Unverified {
		t.Errorf("iolink: %+v", p.IOLink)
	}
	const in, out = `{ point: "P{n}_In", form: u32, doc: "man p. 1" }`, `{ point: "P{n}_Out", form: u32, unverified: true }`
	for _, c := range []struct{ name, old, new, want string }{
		{"no ports", "ports: 2", "ports: 0", "ports 0 is not 1-64"},
		{"more ports than points", "ports: 2", "ports: 3", "port 3's point P3_In is not in the profile"},
		{"no pd-in", "  pd-in: " + in + "\n", "", "pd-in is required"},
		{"no form", in, `{ point: "P{n}_In", doc: "man p. 1" }`, "pd-in: form is required"},
		{"unknown form", in, `{ point: "P{n}_In", form: bytes, doc: "man p. 1" }`, `form "bytes" is not u32 or hex`},
		{"no {n}", `point: "P{n}_In"`, `point: "P1_In"`, "has no {n}"},
		{"u32 on a STRING", in, `{ point: "P{n}_Hex", form: u32, doc: "man p. 1" }`, "point P1_Hex is STRING"},
		{"hex on a UDINT", in, `{ point: "P{n}_In", form: hex, doc: "man p. 1" }`, "point P1_In is UDINT"},
		{"pd-in writable", in, `{ point: "P{n}_Out", form: u32, doc: "man p. 1" }`, "pd-in: point P1_Out is writable"},
		{"pd-out read-only", out, `{ point: "P{n}_In", form: u32, unverified: true }`, "pd-out: point P1_In is not writable"},
		{"pd-valid not BOOL", `pd-valid: "P{n}_Valid"`, `pd-valid: "P{n}_In"`, "pd-valid: point P1_In is UDINT"},
		{"vendor without device", `  device-id: "P{n}_Device"` + "\n", "", "vendor-id and device-id go together"},
		{"device id in 16 bits", `device-id: "P{n}_Device"`, `device-id: "P{n}_Vendor"`, "device-id: point P1_Vendor is UINT"},
		{"unknown key", "ports: 2", "ports: 2\n  isdu: none", "field isdu not found"},
		// The form is a claim of its own: cited, or marked unverified.
		{"pd-in form uncited", in, `{ point: "P{n}_In", form: u32 }`, "pd-in: cite a document for the form (doc:) or mark it unverified: true"},
		{"pd-out form uncited", out, `{ point: "P{n}_Out", form: u32 }`, "pd-out: cite a document for the form"},
		{"form cites no document", in, `{ point: "P{n}_In", form: u32, doc: "other p. 1" }`, `pd-in: doc "other" is not the id of a document under docs`},
	} {
		if !strings.Contains(ok, c.old) {
			t.Fatalf("%s: the block has no %q", c.name, c.old)
		}
		_, err := ParseProfile([]byte(iolinkProfile(strings.Replace(ok, c.old, c.new, 1))), "test")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}
