package devprofile

import (
	"strings"
	"testing"
)

func TestBuiltinIOLinkPorts(t *testing.T) {
	for id, want := range map[string]int{"pepperl-fuchs-ice3": 8, "ifm-al1590": 8, "sick-sig300": 8, "abb-acs355-fmbt21": 0} {
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
	if s.PDIn.Form != FormBytes || PortPoint(s.PDIn.Point, 3) != "Port3_PdiBytes" || PortPoint(s.VendorID, 8) != "Port8_VendorID" || s.PDOut == nil {
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
  - { name: "P{n}_Oct",    type: STRING, wire: ByteString, repeat: 2, node-id: "ns=1;s=P{n}/Oct", path: "P{n}/Oct", unverified: true }
  - { name: "P{n}_OctOut", type: STRING, wire: ByteArray, repeat: 2, writable: true, node-id: "ns=1;s=P{n}/OctOut", path: "P{n}/OctOut", unverified: true }
  - { name: "P{n}_OutValid", type: BOOL, repeat: 2, writable: true, node-id: "ns=1;s=P{n}/OV", path: "P{n}/OV", unverified: true }
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
	bytes := strings.NewReplacer(`point: "P{n}_In", form: u32`, `point: "P{n}_Oct", form: bytes`, `point: "P{n}_Out", form: u32`, `point: "P{n}_OctOut", form: bytes`).Replace(ok)
	if bp, err := ParseProfile([]byte(iolinkProfile(bytes)), "test"); err != nil || bp.IOLink.PDIn.Form != FormBytes {
		t.Fatalf("bytes carriers: %v", err)
	}
	// pd-out-valid, with pd-out, is a writable BOOL per port.
	withOV := strings.Replace(ok, `  pd-valid:`, `  pd-out-valid: "P{n}_OutValid"`+"\n"+`  pd-valid:`, 1)
	if ov, err := ParseProfile([]byte(iolinkProfile(withOV)), "test"); err != nil || PortPoint(ov.IOLink.PDOutValid, 2) != "P2_OutValid" {
		t.Fatalf("pd-out-valid: %v", err)
	}
	// A wire is for a STRING point, and one of the two byte types.
	for name, bad := range map[string]string{
		"wire on a BOOL":  strings.Replace(iolinkProfile(ok), `name: "P{n}_Valid",  type: BOOL,`, `name: "P{n}_Valid",  type: BOOL, wire: ByteString,`, 1),
		"an unknown wire": strings.Replace(iolinkProfile(ok), `wire: ByteString,`, `wire: Blob,`, 1),
	} {
		if _, err := ParseProfile([]byte(bad), "test"); err == nil || !strings.Contains(err.Error(), "only a STRING point can be carried as ByteString or ByteArray") {
			t.Errorf("%s: %v", name, err)
		}
	}
	const in, out = `{ point: "P{n}_In", form: u32, doc: "man p. 1" }`, `{ point: "P{n}_Out", form: u32, unverified: true }`
	for _, c := range []struct{ name, old, new, want string }{
		{"no ports", "ports: 2", "ports: 0", "ports 0 is not 1-64"},
		{"more ports than points", "ports: 2", "ports: 3", "port 3's point P3_In is not in the profile"},
		{"no pd-in", "  pd-in: " + in + "\n", "", "pd-in is required"},
		{"no form", in, `{ point: "P{n}_In", doc: "man p. 1" }`, "pd-in: form is required"},
		{"unknown form", in, `{ point: "P{n}_In", form: base64, doc: "man p. 1" }`, `form "base64" is not u32, hex or bytes`},
		{"bytes on a point with no wire", in, `{ point: "P{n}_Hex", form: bytes, doc: "man p. 1" }`, "point P1_Hex is not carried as octets: form bytes needs wire"},
		{"bytes on a UDINT", in, `{ point: "P{n}_In", form: bytes, doc: "man p. 1" }`, "point P1_In is UDINT"},
		{"no {n}", `point: "P{n}_In"`, `point: "P1_In"`, "has no {n}"},
		{"u32 on a STRING", in, `{ point: "P{n}_Hex", form: u32, doc: "man p. 1" }`, "point P1_Hex is STRING"},
		{"hex on a UDINT", in, `{ point: "P{n}_In", form: hex, doc: "man p. 1" }`, "point P1_In is UDINT"},
		{"pd-in writable", in, `{ point: "P{n}_Out", form: u32, doc: "man p. 1" }`, "pd-in: point P1_Out is writable"},
		{"pd-out read-only", out, `{ point: "P{n}_In", form: u32, unverified: true }`, "pd-out: point P1_In is not writable"},
		{"pd-out-valid not BOOL", `pd-valid: "P{n}_Valid"`, `pd-out-valid: "P{n}_Out"` + "\n  " + `pd-valid: "P{n}_Valid"`, "pd-out-valid: point P1_Out is UDINT"},
		{"pd-out-valid read-only", `pd-valid: "P{n}_Valid"`, `pd-out-valid: "P{n}_Valid"` + "\n  " + `pd-valid: "P{n}_Valid"`, "pd-out-valid: point P1_Valid is not writable"},
		{"pd-out-valid without pd-out", "  pd-out: " + out + "\n", `  pd-out-valid: "P{n}_OutValid"` + "\n", "pd-out-valid goes with pd-out"},
		{"pd-valid not BOOL", `pd-valid: "P{n}_Valid"`, `pd-valid: "P{n}_In"`, "pd-valid: point P1_In is UDINT"},
		{"vendor without device", `  device-id: "P{n}_Device"` + "\n", "", "vendor-id and device-id go together"},
		{"device id in 16 bits", `device-id: "P{n}_Device"`, `device-id: "P{n}_Vendor"`, "device-id: point P1_Vendor is UINT"},
		{"unknown key", "ports: 2", "ports: 2\n  isdo: none", "field isdo not found"},
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

// An isdu: block names the method node ids with a {n} and a citation, in a
// namespace the profile has; the mode and data form agree, and a length is
// bounded.
func TestISDUBlock(t *testing.T) {
	const pre = "  ports: 2\n  pd-in: { point: \"P{n}_In\", form: u32, doc: \"man p. 1\" }\n  isdu: "
	good := `{ mode: opc-30120, object: "ns=1;s=P{n}", read: "ns=1;s=P{n}/R", write: "ns=1;s=P{n}/W", data: bytes, doc: "man p. 3" }`
	p, err := ParseProfile([]byte(iolinkProfile(pre+good)), "test")
	if err != nil {
		t.Fatal(err)
	}
	if p.IOLink.ISDU == nil || p.IOLink.ISDU.Octets() != MaxISDUOctets || p.IOLink.ISDU.Mode != ISDUOPC30120 {
		t.Errorf("isdu: %+v", p.IOLink.ISDU)
	}
	// A master's own methods in hex, limited, with no write method, unverified.
	own := `{ mode: methods, object: "ns=1;s=P{n}", read: "ns=1;s=P{n}/R", data: hex, max-octets: 32, unverified: true }`
	q, err := ParseProfile([]byte(iolinkProfile(pre+own)), "test")
	if err != nil || q.IOLink.ISDU.Octets() != 32 || q.IOLink.ISDU.Write != "" {
		t.Fatalf("methods: %v %+v", err, q.IOLink.ISDU)
	}
	for _, c := range []struct{ name, old, new, want string }{
		{"no mode", "mode: opc-30120, ", "", "isdu: mode is required"},
		{"unknown mode", "mode: opc-30120", "mode: rest", `mode "rest" is not`},
		{"no data", "data: bytes, ", "", "isdu: data is required"},
		{"unknown data", "data: bytes", "data: base64", `data "base64" is not`},
		{"opc-30120 in hex", "data: bytes", "data: hex", "carries a value as a Byte[]"},
		{"too long", `doc: "man p. 3"`, `max-octets: 233, doc: "man p. 3"`, "max-octets 233 is not 1-232"},
		{"no object", `object: "ns=1;s=P{n}", `, "", "object and read are required"},
		{"no {n}", `read: "ns=1;s=P{n}/R"`, `read: "ns=1;s=P/R"`, "read \"ns=1;s=P/R\" has no {n}"},
		{"namespace the profile lacks", `write: "ns=1;s=P{n}/W"`, `write: "ns=7;s=P{n}/W"`, "write:"},
		{"uncited", `, doc: "man p. 3"`, "", "cite a document"},
		{"cites no document", `doc: "man p. 3"`, `doc: "other p. 3"`, `doc "other" is not the id`},
	} {
		if !strings.Contains(good, c.old) {
			t.Fatalf("%s: the block has no %q", c.name, c.old)
		}
		_, err := ParseProfile([]byte(iolinkProfile(pre+strings.Replace(good, c.old, c.new, 1))), "test")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}
