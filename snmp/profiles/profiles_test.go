package profiles

import (
	"os"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/snmp/walk"
)

func fixture(t *testing.T, name string) walk.Walk {
	t.Helper()
	raw, err := os.ReadFile("../testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	w, err := walk.ParseBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestPick(t *testing.T) {
	for _, tc := range []struct {
		fixture, force, want string
	}{
		{"switch.snmpwalk", "", "switch"},      // enterprise 32473 unknown → ifTable probe
		{"ups.snmpwalk", "", "ups-rfc1628"},    // UPS-MIB probe
		{"pdu.snmpwalk", "", "pdu-cyberpower"}, // enterprise 3808 → ePDU probe
		{"ups.snmpwalk", "ups-rfc1628", "ups-rfc1628"},
	} {
		p, err := Pick(fixture(t, tc.fixture), tc.force)
		if err != nil || p.Name != tc.want {
			t.Errorf("%s: Pick = %s, %v; want %s", tc.fixture, p.Name, err, tc.want)
		}
	}
	if _, err := Pick(fixture(t, "ups.snmpwalk"), "router"); err == nil || !strings.Contains(err.Error(), "ups-rfc1628") {
		t.Errorf("unknown --profile = %v", err)
	}
	bare := walk.Walk{{OID: "1.3.6.1.2.1.1.2.0", Type: walk.ObjectID, Str: "1.3.6.1.4.1.9.1.1"}}
	if _, err := Pick(bare, ""); err == nil || !strings.Contains(err.Error(), "1.3.6.1.4.1.9.1.1") {
		t.Errorf("unrecognised device = %v", err)
	}
	// A CyberPower UPS under the same enterprise as the PDU.
	cpsUPSWalk := walk.Walk{
		{OID: "1.3.6.1.2.1.1.2.0", Type: walk.ObjectID, Str: "1.3.6.1.4.1.3808.1.1.1"},
		{OID: upsBaseIdentModel, Type: walk.OctetString, Bytes: []byte("OR1500")},
	}
	cpsUPSWalk.Sort()
	if p, err := Pick(cpsUPSWalk, ""); err != nil || p.Name != "ups-cyberpower" {
		t.Errorf("CPS UPS = %s, %v", p.Name, err)
	}
}

func TestSwitchPorts(t *testing.T) {
	w := fixture(t, "switch.snmpwalk")
	res, err := buildSwitch(w, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var ports, temps []string
	for _, in := range res.Instances {
		switch in.Type {
		case "SwitchPort":
			ports = append(ports, in.Suffix)
		case "TempSensor":
			temps = append(temps, in.Suffix)
		}
	}
	if len(ports) != 28 || ports[0] != "_Port01" || ports[27] != "_Port28" {
		t.Errorf("physical ports = %v", ports)
	}
	if strings.Join(temps, ",") != "_Temp_CPU,_Temp_Inlet_Air" {
		t.Errorf("temps = %v", temps)
	}
	if res.Instances[0].Type != "Switch" || res.Instances[0].Members["PortsTotal"].Const != 28 {
		t.Errorf("root = %+v", res.Instances[0])
	}

	sel, _ := ParseIndexSet("1-3,1001")
	res, err = buildSwitch(w, Options{Ports: sel})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, in := range res.Instances {
		if in.Type == "SwitchPort" {
			got = append(got, in.Suffix)
		}
	}
	// Explicit selection includes the VLAN interface; ports are named by
	// position in ifIndex order (the VLAN interface at ifIndex 1001 is the
	// fourth selected), never by ifIndex — a real S3900 numbers its front
	// panel ifIndex 165…192.
	if strings.Join(got, ",") != "_Port01,_Port02,_Port03,_Port04" {
		t.Errorf("--ports 1-3,1001 = %v", got)
	}
	none, _ := ParseIndexSet("500-600")
	if _, err := buildSwitch(w, Options{Ports: none}); err == nil {
		t.Error("an empty selection must be an error")
	}
	if _, err := ParseIndexSet("3-1"); err == nil {
		t.Error("a backwards range must be an error")
	}
}

// A walk without ifXTable falls back to the 32-bit columns, and says so.
func TestSwitchFallsBackWithoutIfX(t *testing.T) {
	var w walk.Walk
	for _, v := range fixture(t, "switch.snmpwalk") {
		if !walk.HasPrefix(v.OID, ifXTable) {
			w = append(w, v)
		}
	}
	res, err := buildSwitch(w, Options{})
	if err != nil {
		t.Fatal(err)
	}
	p := res.Instances[1].Members
	if p["InBps"].OID != ifInOctets+".1" || p["InBps"].Width != 32 || p["Name"].OID != ifDescr+".1" || p["SpeedMbps"].Scale != 1e-6 {
		t.Errorf("fallback bindings = %+v", p)
	}
	if _, ok := p["Alias"]; ok {
		t.Error("no ifAlias without ifXTable")
	}
	notes := strings.Join(res.Notes, "\n")
	for _, want := range []string{"ifInOctets (width 32)", "ifDescr", "ifAlias absent"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes missing %q:\n%s", want, notes)
		}
	}
}

// Sensor names sanitise into the tag alphabet; two that collide are an
// error naming both rows.
func TestSensorNames(t *testing.T) {
	if got := Sanitise("CPU 0 (core)"); got != "CPU_0_core" {
		t.Errorf("Sanitise = %q", got)
	}
	w := fixture(t, "switch.snmpwalk")
	for i := range w {
		if w[i].OID == entPhysicalName+".3" {
			w[i].Bytes = []byte("CPU!")
		}
	}
	if _, err := buildSwitch(w, Options{}); err == nil || !strings.Contains(err.Error(), "_Temp_CPU") {
		t.Errorf("collision = %v", err)
	}
}

// The FS.COM hook fires on its enterprise and binds nothing unconfirmed.
func TestFSHookBindsNothing(t *testing.T) {
	w := fixture(t, "switch.snmpwalk")
	for i := range w {
		if w[i].OID == sysObjectID {
			w[i].Str = "1.3.6.1.4.1.52642.2.1.45.101"
		}
	}
	w = append(w, walk.Varbind{OID: "1.3.6.1.4.1.52642.1.3507.1.2.1.0", Type: walk.Integer, Int: 43})
	w.Sort()
	p, err := Pick(w, "")
	if err != nil || p.Name != "switch" {
		t.Fatalf("Pick = %s, %v", p.Name, err)
	}
	res, err := p.Build(w, Options{})
	if err != nil {
		t.Fatal(err)
	}
	root := res.Instances[0].Members
	for _, m := range []string{"CpuPct", "MemPct"} {
		if _, ok := root[m]; ok {
			t.Errorf("%s bound from an unconfirmed FS object", m)
		}
	}
	if !strings.Contains(strings.Join(res.Notes, "\n"), "FS.COM (enterprise 52642) vendor hook") {
		t.Errorf("notes = %v", res.Notes)
	}
}

func TestPowerProfiles(t *testing.T) {
	res, err := upsRFC1628().Build(fixture(t, "ups.snmpwalk"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	m := res.Instances[0].Members
	if m["OnBattery"].Eq != 5 || m["LoadPct"].OID != upsOutputPercentLoad1 || m["Fault"].OID != upsAlarmsPresent {
		t.Errorf("UPS = %+v", m)
	}
	if _, ok := m["Serial"]; ok {
		t.Error("RFC 1628 has no serial")
	}

	res, err = pduCyberPower().Build(fixture(t, "pdu.snmpwalk"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Instances) != 9 || res.Instances[0].Members["OutletCount"].Const != 8 || res.Instances[3].Suffix != "_Outlet03" {
		t.Errorf("PDU instances = %d, root %+v", len(res.Instances), res.Instances[0].Members)
	}
	if on := res.Instances[3].Members["On"]; on.OID != ePDUOutletControlOutletCommand+".3" || on.Eq != 1 {
		t.Errorf("Outlet03.On = %+v", on)
	}

	// A walk missing objects: the members are skipped and noted.
	var thin walk.Walk
	for _, v := range fixture(t, "ups.snmpwalk") {
		if v.OID != upsBatteryTemperature {
			thin = append(thin, v)
		}
	}
	res, _ = upsRFC1628().Build(thin, Options{})
	if _, ok := res.Instances[0].Members["BatteryTempC"]; ok || !strings.Contains(strings.Join(res.Notes, "\n"), "BatteryTempC: upsBatteryTemperature.0 not in the walk") {
		t.Errorf("missing object handling: %v", res.Notes)
	}
}

func TestObjectName(t *testing.T) {
	for oid, want := range map[string]string{
		"1.3.6.1.2.1.31.1.1.1.6.3":           "ifHCInOctets.3",
		"1.3.6.1.2.1.1.5.0":                  "sysName.0",
		"1.3.6.1.4.1.3808.1.1.3.3.3.1.1.4.5": "ePDUOutletControlOutletCommand.5",
		"1.3.6.1.4.1.99999.1":                "1.3.6.1.4.1.99999.1",
	} {
		if got := ObjectName(oid); got != want {
			t.Errorf("ObjectName(%s) = %s, want %s", oid, got, want)
		}
	}
}
