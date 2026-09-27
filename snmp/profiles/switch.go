package profiles

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/hw"
	"github.com/joyautomation/nautilus/snmp"
	"github.com/joyautomation/nautilus/snmp/walk"
)

// IF-MIB (RFC 2863).
const (
	ifTable       = "1.3.6.1.2.1.2.2.1"
	ifIndexCol    = ifTable + ".1"  // ifIndex
	ifDescr       = ifTable + ".2"  // ifDescr
	ifType        = ifTable + ".3"  // ifType (IANAifType: ethernetCsmacd(6))
	ifSpeed       = ifTable + ".5"  // ifSpeed, Gauge32 bit/s
	ifAdminStatus = ifTable + ".7"  // ifAdminStatus up(1) down(2) testing(3)
	ifOperStatus  = ifTable + ".8"  // ifOperStatus up(1) down(2) …
	ifInOctets    = ifTable + ".10" // ifInOctets, Counter32
	ifInDiscards  = ifTable + ".13" // ifInDiscards, Counter32
	ifInErrors    = ifTable + ".14" // ifInErrors, Counter32
	ifOutOctets   = ifTable + ".16" // ifOutOctets, Counter32
	ifOutDiscards = ifTable + ".19" // ifOutDiscards, Counter32
	ifOutErrors   = ifTable + ".20" // ifOutErrors, Counter32

	ifXTable      = "1.3.6.1.2.1.31.1.1.1"
	ifName        = ifXTable + ".1"  // ifName
	ifHCInOctets  = ifXTable + ".6"  // ifHCInOctets, Counter64
	ifHCOutOctets = ifXTable + ".10" // ifHCOutOctets, Counter64
	ifHighSpeed   = ifXTable + ".15" // ifHighSpeed, Gauge32 Mb/s
	ifAlias       = ifXTable + ".18" // ifAlias

	ethernetCsmacd = 6
	// The IANAifType values a physical ethernet port shows up as. FSOS
	// (an FS S3900 on the office bench) reports its 24 copper ports as
	// gigabitEthernet(117) and only the four SFP+ ports as
	// ethernetCsmacd(6); other agents use fastEther(62) / fastEtherFX(69).
	fastEther       = 62
	fastEtherFX     = 69
	gigabitEthernet = 117
)

// ENTITY-MIB (RFC 6933) entPhysicalTable.
const (
	entPhysicalTable     = "1.3.6.1.2.1.47.1.1.1.1"
	entPhysicalClass     = entPhysicalTable + ".5"  // chassis(3) … sensor(8)
	entPhysicalDescr     = entPhysicalTable + ".2"  // entPhysicalDescr
	entPhysicalName      = entPhysicalTable + ".7"  // entPhysicalName
	entPhysicalSerialNum = entPhysicalTable + ".11" // entPhysicalSerialNum
	entPhysicalModelName = entPhysicalTable + ".13" // entPhysicalModelName

	classChassis = 3
)

// ENTITY-SENSOR-MIB (RFC 3433) entPhySensorTable, indexed by entPhysicalIndex.
const (
	entPhySensorTable      = "1.3.6.1.2.1.99.1.1.1"
	entPhySensorType       = entPhySensorTable + ".1" // EntitySensorDataType: celsius(8)
	entPhySensorScale      = entPhySensorTable + ".2" // EntitySensorDataScale: units(9)
	entPhySensorPrecision  = entPhySensorTable + ".3" // digits after the decimal point
	entPhySensorValue      = entPhySensorTable + ".4" // EntitySensorValue
	entPhySensorOperStatus = entPhySensorTable + ".5" // ok(1) unavailable(2) nonoperational(3)

	sensorCelsius = 8
	scaleUnits    = 9
)

// POWER-ETHERNET-MIB (RFC 3621) pethPsePortTable, indexed by
// {pethPsePortGroupIndex, pethPsePortIndex}.
const (
	pethPsePortDetectionStatus = "1.3.6.1.2.1.105.1.1.1.6" // deliveringPower(3)
	poeDelivering              = 3
)

func switchProfile() Profile {
	return Profile{
		Name:     "switch",
		Desc:     "managed switch: IF-MIB ifTable/ifXTable ports, ENTITY-MIB chassis, ENTITY-SENSOR-MIB temperatures, POWER-ETHERNET-MIB",
		Verified: false,
		Probe:    func(w walk.Walk) bool { return len(w.Subtree(ifTable)) > 0 },
		Build:    buildSwitch,
	}
}

// buildSwitch expands a walk into one Switch root, one SwitchPort per
// selected interface, and one TempSensor per ENTITY-SENSOR-MIB celsius row.
//
// Ports are named by POSITION among the selected interfaces in ifIndex
// order (Port01 … PortN, zero-padded to at least two digits), not by
// ifIndex: a real FS S3900 numbers its 28 physical ports ifIndex 165…192,
// which would have made the front panel's first port SW1_Port165. The
// ifIndex itself is the Index member, so nothing is lost, and a fixed-port
// switch's positions are exactly its front-panel numbering.
func buildSwitch(w walk.Walk, o Options) (Result, error) {
	b := &builder{w: w}
	res := Result{Profile: "switch"}

	// Ports: every ifIndex row, filtered.
	var ports []int
	for _, suffix := range rows(w, ifIndexCol) {
		idx, err := strconv.Atoi(suffix)
		if err != nil {
			continue
		}
		if o.Ports != nil {
			if o.Ports.Has(idx) {
				ports = append(ports, idx)
			}
			continue
		}
		if t, ok := intAt(w, ifType+"."+suffix); ok && isEthernetType(t) {
			ports = append(ports, idx)
		}
	}
	sortInts(ports)
	if len(ports) == 0 {
		if o.Ports != nil {
			return res, fmt.Errorf("--ports %s selects no ifIndex in the walk", o.Ports)
		}
		return res, fmt.Errorf("no ethernet ports (ifType 6/62/69/117) in the walk — pass --ports to pick interfaces by ifIndex")
	}
	width := max(2, padWidth(len(ports)))

	missing := map[string]int{} // note once per object, not once per port
	for pos, idx := range ports {
		i := strconv.Itoa(idx)
		m := map[string]snmp.Member{
			"Index":   cnst(idx),
			"AdminUp": bind(ifAdminStatus+"."+i, hw.Binding{Eq: 1}),
			"OperUp":  bind(ifOperStatus+"."+i, hw.Binding{Eq: 1}),
			"Down":    derived("AdminUp && !OperUp"),
			"InPct":   derived("100 * InBps / (SpeedMbps * 1000000)"),
			"OutPct":  derived("100 * OutBps / (SpeedMbps * 1000000)"),
		}
		pick := func(member string, prefer, fallback string, pb, fb hw.Binding, what string) {
			switch {
			case has(w, prefer+"."+i):
				m[member] = bind(prefer+"."+i, pb)
			case fallback != "" && has(w, fallback+"."+i):
				m[member] = bind(fallback+"."+i, fb)
				missing[what+" — fell back to "+nameOf(fallback)]++
			default:
				missing[member+": "+what]++
			}
		}
		pick("Name", ifName, ifDescr, hw.Binding{}, hw.Binding{}, "ifName absent")
		pick("Alias", ifAlias, "", hw.Binding{}, hw.Binding{}, "ifAlias absent, left empty")
		pick("SpeedMbps", ifHighSpeed, ifSpeed, hw.Binding{}, hw.Binding{Scale: 1e-6}, "ifHighSpeed absent")
		pick("InBps", ifHCInOctets, ifInOctets, hw.Binding{Rate: true, Width: 64, Scale: 8}, hw.Binding{Rate: true, Width: 32, Scale: 8}, "ifHCInOctets absent (32-bit counter wraps every ~34 s at 1 Gb/s)")
		pick("OutBps", ifHCOutOctets, ifOutOctets, hw.Binding{Rate: true, Width: 64, Scale: 8}, hw.Binding{Rate: true, Width: 32, Scale: 8}, "ifHCOutOctets absent")
		pick("InErrors", ifInErrors, "", hw.Binding{}, hw.Binding{}, "ifInErrors absent")
		pick("OutErrors", ifOutErrors, "", hw.Binding{}, hw.Binding{}, "ifOutErrors absent")
		pick("InDiscards", ifInDiscards, "", hw.Binding{}, hw.Binding{}, "ifInDiscards absent")
		pick("OutDiscards", ifOutDiscards, "", hw.Binding{}, hw.Binding{}, "ifOutDiscards absent")
		// ErrorRate is the rate of ifInErrors ONLY: the contract member says
		// "in plus out", but a rate over a sum needs a hidden member the
		// contract does not have, so out-errors are visible in OutErrors
		// (counter) and not in this rate. Recorded in the notes.
		pick("ErrorRate", ifInErrors, "", hw.Binding{Rate: true, Width: 32}, hw.Binding{}, "ifInErrors absent")
		// PoE: RFC 3621 indexes ports by {group, port}; the ifIndex mapping
		// is not standardised. Bound only when group 1 has a row whose port
		// index equals this ifIndex — the common single-unit layout.
		if poe := pethPsePortDetectionStatus + ".1." + i; has(w, poe) {
			m["PoeOn"] = bind(poe, hw.Binding{Eq: poeDelivering})
		}
		res.Instances = append(res.Instances, Instance{Suffix: "_Port" + padded(pos+1, width), Type: "SwitchPort", Members: m})
	}
	for _, k := range sortedKeys(missing) {
		b.notes = append(b.notes, fmt.Sprintf("SwitchPort.%s (%d port(s))", k, missing[k]))
	}
	b.notes = append(b.notes,
		"SwitchPort.ErrorRate is the rate of ifInErrors only — out-errors are in OutErrors (a rate over a sum needs a member the contract does not have)")

	// Root.
	root := map[string]snmp.Member{
		"PortsTotal": cnst(len(ports)),
	}
	b.opt(root, "Name", sysName, hw.Binding{}, "sysName.0")
	b.opt(root, "UptimeS", sysUpTime, hw.Binding{Scale: 0.01}, "sysUpTime.0")
	// The chassis row: model from entPhysicalModelName, else entPhysicalName,
	// else entPhysicalDescr — FSOS has no ModelName column at all, and its
	// Name and Descr both read "S3900-24T4S-R" — and the serial from the same
	// row whichever of those named it.
	if c, ok := chassisRow(w); ok && (nonEmpty(w, entPhysicalModelName+"."+c) || nonEmpty(w, entPhysicalName+"."+c) || nonEmpty(w, entPhysicalDescr+"."+c)) {
		switch {
		case nonEmpty(w, entPhysicalModelName+"."+c):
			root["Model"] = bind(entPhysicalModelName+"."+c, hw.Binding{})
		case nonEmpty(w, entPhysicalName+"."+c):
			root["Model"] = bind(entPhysicalName+"."+c, hw.Binding{})
		default:
			root["Model"] = bind(entPhysicalDescr+"."+c, hw.Binding{})
		}
		if nonEmpty(w, entPhysicalSerialNum+"."+c) {
			root["Serial"] = bind(entPhysicalSerialNum+"."+c, hw.Binding{})
		} else {
			b.notes = append(b.notes, "Switch.Serial: the ENTITY-MIB chassis row has no entPhysicalSerialNum — left empty")
		}
	} else {
		b.opt(root, "Model", sysDescr, hw.Binding{}, "sysDescr.0 (no ENTITY-MIB chassis row)")
		b.notes = append(b.notes, "Switch.Serial: no ENTITY-MIB chassis row — left empty")
	}

	// Temperatures.
	temps, err := celsiusSensors(w, b)
	if err != nil {
		return res, err
	}
	if len(temps) > 0 {
		root["TempC"] = temps[0].Members["Value"]
		b.notes = append(b.notes, fmt.Sprintf("Switch.TempC reads the first celsius sensor (%s)", strings.TrimPrefix(temps[0].Suffix, "_")))
	} else {
		b.notes = append(b.notes, "Switch.TempC: no ENTITY-SENSOR-MIB celsius row — left at 0")
	}
	res.Instances = append(res.Instances, temps...)

	b.notes = append(b.notes,
		"Switch.PortsUp is left 0: count OperUp across the port tags in the project's ST",
		"Switch.Online is not a wire value: interlock on the "+"__Online companion tag")
	for _, h := range vendorHooks {
		if vb, ok := w.Get(sysObjectID); ok && walk.HasPrefix(vb.Str, h.prefix) {
			h.extend(w, root, b)
		}
	}
	if _, bound := root["CpuPct"]; !bound {
		b.notes = append(b.notes, "Switch.CpuPct/MemPct: vendor MIB objects only — left at 0 for this device")
	}
	res.Instances = append([]Instance{{Suffix: "", Type: "Switch", Members: root}}, res.Instances...)
	res.Notes = b.notes
	return res, nil
}

// chassisRow finds the ENTITY-MIB row whose entPhysicalClass is chassis(3),
// lowest index first.
func chassisRow(w walk.Walk) (string, bool) {
	for _, suffix := range rows(w, entPhysicalClass) {
		if c, _ := intAt(w, entPhysicalClass+"."+suffix); c == classChassis {
			return suffix, true
		}
	}
	return "", false
}

func nonEmpty(w walk.Walk, oid string) bool {
	s, ok := textAt(w, oid)
	return ok && strings.TrimSpace(s) != ""
}

// celsiusSensors turns every ENTITY-SENSOR-MIB celsius row into a
// TempSensor named after its entPhysicalName. The value's scaling is
// codegen-time knowledge read from the walk: EntitySensorDataScale (an SI
// prefix, units(9) = 10^0, each step 10^3) and entPhySensorPrecision
// (digits after the point).
func celsiusSensors(w walk.Walk, b *builder) ([]Instance, error) {
	var out []Instance
	seen := map[string]string{}
	for _, idx := range rows(w, entPhySensorType) {
		t, _ := intAt(w, entPhySensorType+"."+idx)
		if t != sensorCelsius || !has(w, entPhySensorValue+"."+idx) {
			continue
		}
		name, ok := textAt(w, entPhysicalName+"."+idx)
		if !ok || strings.TrimSpace(name) == "" {
			name = "Sensor" + idx
		}
		clean := Sanitise(name)
		if clean == "" {
			clean = "Sensor" + Sanitise(idx)
		}
		if prev, dup := seen[clean]; dup {
			return nil, fmt.Errorf("temperature sensors %s and %s both sanitise to _Temp_%s — rename one on the device or drop one with --profile edits", prev, idx, clean)
		}
		seen[clean] = idx
		scale := int64(scaleUnits)
		if s, ok := intAt(w, entPhySensorScale+"."+idx); ok {
			scale = s
		}
		prec, _ := intAt(w, entPhySensorPrecision+"."+idx)
		factor := math.Pow(10, float64(3*(scale-scaleUnits))-float64(prec))
		vb := hw.Binding{}
		if factor != 1 {
			vb.Scale = roundFactor(factor)
		}
		m := map[string]snmp.Member{
			"Name":     cnst(name),
			"Value":    bind(entPhySensorValue+"."+idx, vb),
			"High":     derived("HighSP > 0 && Value >= HighSP"),
			"HighHigh": derived("HighHighSP > 0 && Value >= HighHighSP"),
		}
		if has(w, entPhySensorOperStatus+"."+idx) {
			m["Fault"] = bind(entPhySensorOperStatus+"."+idx, hw.Binding{Map: map[string]any{"1": false, "2": true, "3": true}})
		}
		out = append(out, Instance{Suffix: "_Temp_" + clean, Type: "TempSensor", Members: m})
	}
	if len(out) > 0 {
		b.notes = append(b.notes, "TempSensor.HighSP/HighHighSP: ENTITY-SENSOR-MIB carries no thresholds — 0 (High/HighHigh stay false) until the manifest sets const: values")
	}
	return out, nil
}

// roundFactor strips float noise from 10^n (10^-1 is 0.1, not
// 0.10000000000000002), so the manifest reads as a human would write it.
func roundFactor(f float64) float64 {
	v, _ := strconv.ParseFloat(strconv.FormatFloat(f, 'g', 12, 64), 64)
	return v
}

// nameOf names the IF-MIB column for notes.
func nameOf(col string) string {
	switch col {
	case ifDescr:
		return "ifDescr"
	case ifSpeed:
		return "ifSpeed/1e6"
	case ifInOctets:
		return "ifInOctets (width 32)"
	case ifOutOctets:
		return "ifOutOctets (width 32)"
	}
	return col
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// isEthernetType reports whether an IANAifType is a physical ethernet port.
func isEthernetType(t int64) bool {
	switch t {
	case ethernetCsmacd, fastEther, fastEtherFX, gigabitEthernet:
		return true
	}
	return false
}
