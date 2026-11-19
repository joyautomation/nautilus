//go:build !js

package devbind

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/st"
	"github.com/joyautomation/nautilus/opcua"
)

// An OPC UA row is a driver of its own: where it answers (the profile's
// port when the row gives none), and a manifest with a read binding per
// point under its member name, its {params} substituted, and a binding
// per writable point under the command tag's name that is written and
// never polled.
func TestOPCUADevices(t *testing.T) {
	s, err := Resolve(vendored(t, "ifm-al1590", "pepperl-fuchs-ice3"), []Device{
		{Name: "MASTER1", Profile: "ifm-al1590", Endpoint: "10.0.0.20", Params: map[string]string{"location": "LINE_A"}, Points: []string{"Port1_Pdin"}},
		{Name: "PF1", Profile: "pepperl-fuchs-ice3", Endpoint: "opc.tcp://10.0.0.21:4841", Points: []string{"Port2_PdoU32", "Port2_SioInput"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Modbus() {
		t.Error("a set of OPC UA devices reports a Modbus device")
	}
	if n := len(s.ModbusManifest().Sources); n != 0 {
		t.Errorf("the OPC UA devices are %d Modbus sources", n)
	}
	devs := s.OPCUADevices()
	if len(devs) != 2 {
		t.Fatalf("devices %+v", devs)
	}
	m1, pf := devs[0], devs[1]
	if m1.Name != "MASTER1" || m1.Endpoint != "opc.tcp://10.0.0.20:4843" {
		t.Errorf("MASTER1 at %q, want the profile's port 4843", m1.Endpoint)
	}
	if pf.Endpoint != "opc.tcp://10.0.0.21:4841" {
		t.Errorf("PF1 at %q, want the row's port", pf.Endpoint)
	}
	want := []opcua.TagBinding{{
		Name: "MASTER1.Port1_Pdin", NodeID: "ns=5;s=LINE_A/iolinkmaster/port[1]/iolinkdevice/pdin", Type: "String",
		Desc: "Port 1: IO-Link process data in, as hex text",
	}}
	if !reflect.DeepEqual(m1.Manifest.Tags, want) {
		t.Errorf("MASTER1 bindings\n got %+v\nwant %+v", m1.Manifest.Tags, want)
	}
	got := map[string]opcua.TagBinding{}
	for _, b := range pf.Manifest.Tags {
		got[b.Name] = b
	}
	if len(got) != 3 {
		t.Errorf("PF1 bindings %v, want two members and one command", got)
	}
	if b := got["PF1.Port2_PdoU32"]; b.Writable || b.ScanClass != "" || b.Type != "UInt32" || b.NodeID != "ns=1;s=IOLM/Port 2/Attached Device/PDO Data Unsigned32" {
		t.Errorf("member %+v: want a polled read of the node as UInt32", b)
	}
	if b := got["PF1_Port2_PdoU32"]; !b.Writable || b.ScanClass != opcua.NoPoll || b.NodeID != got["PF1.Port2_PdoU32"].NodeID {
		t.Errorf("command %+v: want the same node, writable, never polled", b)
	}
	if b := got["PF1.Port2_SioInput"]; b.Type != "Boolean" {
		t.Errorf("SioInput as %q, want Boolean", b.Type)
	}
	for _, d := range devs {
		if _, err := opcua.New(d.Endpoint, d.Manifest); err != nil {
			t.Errorf("%s: the derived manifest does not build a driver: %v", d.Name, err)
		}
	}
}

// A row with no driver: section connects with security None; a row that
// names one leaves its security to the section (#975), and a Modbus row
// has no driver: at all.
func TestOPCUARowsDriver(t *testing.T) {
	s, err := Resolve(vendored(t, "pepperl-fuchs-ice3"), []Device{
		{Name: "PF1", Profile: "pepperl-fuchs-ice3", Endpoint: "h"},
		{Name: "PF2", Profile: "pepperl-fuchs-ice3", Endpoint: "h2", Driver: "sec"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := s.OPCUARows()
	if r := rows[0]; r.Driver != "" || r.Security != "None" || r.SecurityMode != "None" {
		t.Errorf("PF1 %+v, want None/None and no section", r)
	}
	if r := rows[1]; r.Driver != "sec" || r.Security != "" || r.SecurityMode != "" {
		t.Errorf("PF2 %+v, want the section's security, none of its own", r)
	}
	if _, err := Resolve(vendored(t, "abb-acs355-fmbt21"), []Device{{Name: "C", Profile: "abb-acs355-fmbt21", Endpoint: "h", Driver: "sec"}}); err == nil || !strings.Contains(err.Error(), "driver: is an OPC UA device's") {
		t.Errorf("driver: on a Modbus row: %v", err)
	}
}

// A profile's verified namespace URI is bound by URI, which the driver
// resolves against the server's live table; a placeholder (urn:virtual:)
// leaves the index to be trusted, and the device's driver says so.
func TestOPCUANamespaces(t *testing.T) {
	fsys := vendored(t, "pepperl-fuchs-ice3")
	rows := []Device{{Name: "PF1", Profile: "pepperl-fuchs-ice3", Endpoint: "h", Points: []string{"Port1_SioInput"}}}
	s, err := Resolve(fsys, rows)
	if err != nil {
		t.Fatal(err)
	}
	od := s.OPCUADevices()[0]
	if b := od.Manifest.Tags[0]; b.Namespace != "" {
		t.Errorf("a placeholder URI bound: %q", b.Namespace)
	}
	if !reflect.DeepEqual(od.IndexOnly, []int{1}) {
		t.Errorf("index only %v, want [1]", od.IndexOnly)
	}
	drv, err := opcua.New(od.Endpoint, od.Manifest, opcua.WithSecurity("None", "None"))
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.WrapOPCUA("PF1", drv, "None", "None")
	if err != nil {
		t.Fatal(err)
	}
	if ws := strings.Join(w.Warnings(), "\n"); !strings.Contains(ws, "device PF1 binds by namespace index 1: profile pepperl-fuchs-ice3's URI for it is a placeholder (urn:virtual:pepperl-fuchs:iolm)") {
		t.Errorf("warnings %q", ws)
	}

	raw := string(fsys["profiles/pepperl-fuchs-ice3.yaml"].Data)
	verified := strings.Replace(raw, "- urn:virtual:pepperl-fuchs:iolm", "- urn:example:pepperl-fuchs:iolm", 1)
	if verified == raw {
		t.Fatal("the fixture edit did not apply")
	}
	fsys = fstest.MapFS{"profiles/pepperl-fuchs-ice3.yaml": {Data: []byte(verified)}}
	if s, err = Resolve(fsys, rows); err != nil {
		t.Fatal(err)
	}
	od = s.OPCUADevices()[0]
	if b := od.Manifest.Tags[0]; b.Namespace != "urn:example:pepperl-fuchs:iolm" {
		t.Errorf("a verified URI not bound: %+v", b)
	}
	if len(od.IndexOnly) != 0 {
		t.Errorf("index only %v with a verified URI", od.IndexOnly)
	}
	drv, err = opcua.New(od.Endpoint, od.Manifest, opcua.WithSecurity("None", "None"))
	if err != nil {
		t.Fatal(err)
	}
	if w, err = s.WrapOPCUA("PF1", drv, "None", "None"); err != nil {
		t.Fatal(err)
	}
	if ws := strings.Join(w.Warnings(), "\n"); strings.Contains(ws, "namespace index") {
		t.Errorf("an index warning with a verified URI: %q", ws)
	}
}

// The OPC UA adapter is the device as its struct: the driver's inputs name
// the device in place of its members (the online companion stays), a
// STRING member assembles, and the commands are the driver's outputs.
func TestOPCUADriver(t *testing.T) {
	s, err := Resolve(vendored(t, "ifm-al1590"), []Device{{Name: "MASTER1", Profile: "ifm-al1590", Endpoint: "h"}})
	if err != nil {
		t.Fatal(err)
	}
	od := s.OPCUADevices()[0]
	drv, err := opcua.New(od.Endpoint, od.Manifest, opcua.WithName("MASTER1"), opcua.WithOnlineTag("MASTER1__Online"))
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.WrapOPCUA("MASTER1", drv, "None", "None")
	if err != nil {
		t.Fatal(err)
	}
	if got := w.InputNames(); !reflect.DeepEqual(got, []string{"MASTER1", "MASTER1__Online"}) {
		t.Errorf("inputs %v", got)
	}
	var tags []string
	for _, tg := range s.Tags() {
		tags = append(tags, tg.Name+":"+tg.Role+":"+tg.Type)
	}
	// The driver's one companion, as an input; no Modbus __Outages.
	// ...and a command tag per writable point: the AL1590's pdout, 8 ports.
	want := "MASTER1:input:IFM_AL1590 MASTER1__Online:input:BOOL"
	for n := 1; n <= 8; n++ {
		want += fmt.Sprintf(" MASTER1_Port%d_Pdout:output:STRING", n)
	}
	if strings.Join(tags, " ") != want {
		t.Errorf("tags %v, want %s", tags, want)
	}
	if od.Security != "None" || od.SecurityMode != "None" {
		t.Errorf("security %q/%q, want None/None", od.Security, od.SecurityMode)
	}
	if ws := strings.Join(w.Warnings(), "\n"); !strings.Contains(ws, "device MASTER1 connects with security None, anonymous: its row names no driver: section with its own security") {
		t.Errorf("warnings %q, want the unsecured session named", ws)
	}
	if got := w.InputType("MASTER1"); got != "IFM_AL1590" {
		t.Errorf("type %q", got)
	}
	vals := nio.Values{"MASTER1.Port3_Pdin": "0286", "MASTER1__Online": true}
	w.NewAssembly()(vals)
	v, ok := vals["MASTER1"].(ir.Value)
	if !ok || v.Fld[v.Struct.FieldIndex["Port3_Pdin"]].S != "0286" {
		t.Errorf("assembled %v", vals)
	}
	if _, ok := vals["MASTER1.Port3_Pdin"]; ok {
		t.Error("a member stayed in the delivery")
	}
	if _, err := s.WrapOPCUA("OTHER", drv, "None", "None"); err == nil {
		t.Error("wrapped a device the set does not have")
	}
}

// ports: is #705's shape: names share the devices' one namespace, an IODD
// is a file in iodd/, and a profile that declares no IO-Link ports
// refuses them rather than ignore them.
func TestResolvePorts(t *testing.T) {
	fsys := vendored(t, "ifm-al1590", "abb-acs355-fmbt21", "sick-sig300")
	fsys["iodd/sensor.xml"] = &fstest.MapFile{Data: []byte("<IODevice/>")}
	master := func(ports map[int]Port) []Device {
		return []Device{
			{Name: "CONV", Profile: "abb-acs355-fmbt21", Endpoint: "h"},
			{Name: "MASTER1", Profile: "ifm-al1590", Endpoint: "h", Ports: ports},
		}
	}
	for name, tc := range map[string]struct {
		ports map[int]Port
		want  string
	}{
		"not an identifier": {map[int]Port{1: {Name: "1LEVEL"}}, `port 1: name "1LEVEL" is not an IEC identifier`},
		"a device's name":   {map[int]Port{1: {Name: "conv"}}, "port 1: conv is already the name of CONV"},
		"two ports":         {map[int]Port{1: {Name: "LEVEL"}, 2: {Name: "Level"}}, "port 2: Level is already the name of LEVEL"},
		"iodd path":         {map[int]Port{1: {Name: "LEVEL", IODD: "../sensor.xml"}}, `iodd "../sensor.xml" must be a file name in iodd/`},
		"iodd missing":      {map[int]Port{1: {Name: "LEVEL", IODD: "other.xml"}}, "iodd other.xml is not in iodd/"},
		"past the ports":    {map[int]Port{9: {Name: "LEVEL", IODD: "sensor.xml"}}, "ports: profile ifm-al1590 has ports 1-8, not 9"},
		"not loaded yet":    {map[int]Port{8: {Name: "LEVEL", IODD: "sensor.xml"}}, "ports: port devices are not loaded yet (#705)"},
	} {
		if _, err := Resolve(fsys, master(tc.ports)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
	// A profile with no iolink: block has no IO-Link ports.
	drive := []Device{{Name: "MASTER1", Profile: "abb-acs355-fmbt21", Endpoint: "h", Ports: map[int]Port{1: {Name: "LEVEL", IODD: "sensor.xml"}}}}
	if _, err := Resolve(fsys, drive); err == nil || !strings.Contains(err.Error(), "ports: profile abb-acs355-fmbt21 declares no IO-Link ports") {
		t.Errorf("abb-acs355-fmbt21: %v", err)
	}
}

// A row's port numbers are a master's 1 to n.
func TestPortRange(t *testing.T) {
	for _, tc := range []struct {
		nums []int
		ok   bool
	}{
		{[]int{1, 8}, true}, {[]int{3}, true}, {[]int{0, 2}, false}, {[]int{-1}, false}, {[]int{2, 9}, false},
	} {
		if err := portRange(tc.nums, 8); (err == nil) != tc.ok {
			t.Errorf("%v of 8: %v", tc.nums, err)
		}
	}
	if err := portRange([]int{9}, 8); err == nil || err.Error() != "has ports 1-8, not 9" {
		t.Errorf("message %v", err)
	}
}

// Every type an OPC UA point may be is a struct member and an OPC UA
// built-in type, and the device type compiles: the shipped profiles use
// only some of them.
func TestOPCUAPointTypes(t *testing.T) {
	var rows strings.Builder
	types := []string{"BOOL", "BYTE", "INT", "UINT", "WORD", "DINT", "UDINT", "DWORD", "LINT", "ULINT", "REAL", "LREAL", "STRING"}
	for _, ty := range types {
		fmt.Fprintf(&rows, "  - { name: P_%s, type: %s, node-id: \"ns=1;s=%s\", path: \"Dev/%s\", unverified: true }\n", ty, ty, ty, ty)
	}
	prof := "profile: every-type\ntitle: t\nprotocol: opcua\nopcua: { namespaces: [urn:example:t] }\npoints:\n" + rows.String()
	s, err := Resolve(fstest.MapFS{"profiles/every-type.yaml": {Data: []byte(prof)}}, []Device{{Name: "D", Profile: "every-type", Endpoint: "h"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range s.OPCUADevices()[0].Manifest.Tags {
		if b.Type == "" {
			t.Errorf("%s has no OPC UA type", b.Name)
		}
	}
	src, err := s.TypesST()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Parse(src); err != nil {
		t.Errorf("the type does not parse: %v\n%s", err, src)
	}
	newAssembler(s.devices[0]) // panics on a type it cannot hold
}

// A point carried as octets on the wire (wire: ByteString / ByteArray) is
// bound as that type, not as the String its STRING would be: the driver
// then reads the octets as hex and writes hex back as octets (#705).
func TestOPCUAWireTypes(t *testing.T) {
	s, err := Resolve(vendored(t, "pepperl-fuchs-ice3", "sick-sig300"), []Device{
		{Name: "PF1", Profile: "pepperl-fuchs-ice3", Endpoint: "h", Points: []string{"Port1_PdiBytes", "Port1_PdoBytes", "Port1_Status"}},
		{Name: "SICK1", Profile: "sick-sig300", Endpoint: "h", Points: []string{"Port1_PdiBytes"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, od := range s.OPCUADevices() {
		for _, b := range od.Manifest.Tags {
			got[b.Name] = b.Type
		}
	}
	for name, want := range map[string]string{
		"PF1.Port1_PdiBytes": "ByteString", "PF1.Port1_PdoBytes": "ByteString", "PF1_Port1_PdoBytes": "ByteString",
		"PF1.Port1_Status": "String", "SICK1.Port1_PdiBytes": "ByteArray",
	} {
		if got[name] != want {
			t.Errorf("%s bound as %q, want %q (bindings %v)", name, got[name], want, got)
		}
	}
}
