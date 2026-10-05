package hw

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/stgen"
)

// Field is one UDT member: its IEC kind, engineering unit and a description
// the tag file and the Sparkplug properties carry.
type Field struct {
	Name string
	Kind ir.TypeKind // TypeBool | TypeInt | TypeReal | TypeString
	Unit string
	Desc string
}

// Type is one UDT of the contract set. Field order is the wire order of the
// struct value AND the order of the rendered TYPE block: the VM addresses a
// member by slot index, so the two must agree — and they do by construction,
// because both come from this one table.
type Type struct {
	Name   string
	Desc   string
	Fields []Field
}

// Types is the contract set of docs/design/it-drivers.md §2, in the order the
// generated hw_types.st declares them. Add members, never rename or reorder
// them: the spatial-hmi scene nodes, alarm rules and Sparkplug Templates all
// bind to these names.
var Types = []Type{
	{Name: "Server", Desc: "one physical or virtual machine (Redfish BMC, or node_exporter on the host)", Fields: []Field{
		{Name: "Online", Kind: ir.TypeBool, Desc: "source answered its last poll"},
		{Name: "PowerOn", Kind: ir.TypeBool, Desc: "system power state is On"},
		{Name: "Health", Kind: ir.TypeInt, Desc: "0 ok, 1 warning, 2 critical, 3 unknown"},
		{Name: "Fault", Kind: ir.TypeBool, Desc: "Health is critical"},
		{Name: "Warning", Kind: ir.TypeBool, Desc: "Health is warning"},
		{Name: "Model", Kind: ir.TypeString},
		{Name: "Serial", Kind: ir.TypeString},
		{Name: "UptimeS", Kind: ir.TypeInt, Unit: "s", Desc: "seconds since boot"},
		{Name: "CpuPct", Kind: ir.TypeReal, Unit: "%", Desc: "CPU busy"},
		{Name: "MemPct", Kind: ir.TypeReal, Unit: "%", Desc: "memory in use"},
		{Name: "Load1", Kind: ir.TypeReal, Desc: "1-minute load average"},
		{Name: "RootDiskPct", Kind: ir.TypeReal, Unit: "%", Desc: "root filesystem in use"},
		{Name: "InletTempC", Kind: ir.TypeReal, Unit: "°C", Desc: "inlet / ambient temperature"},
		{Name: "MaxTempC", Kind: ir.TypeReal, Unit: "°C", Desc: "hottest sensor"},
		{Name: "PowerW", Kind: ir.TypeReal, Unit: "W", Desc: "power drawn"},
		{Name: "FanCount", Kind: ir.TypeInt, Desc: "Fan children generated"},
		{Name: "PsuCount", Kind: ir.TypeInt, Desc: "PSU children generated"},
		{Name: "TempCount", Kind: ir.TypeInt, Desc: "TempSensor children generated"},
	}},
	{Name: "Fan", Desc: "one cooling fan", Fields: []Field{
		{Name: "Name", Kind: ir.TypeString},
		{Name: "Present", Kind: ir.TypeBool},
		{Name: "RPM", Kind: ir.TypeReal, Unit: "rpm"},
		{Name: "Pct", Kind: ir.TypeReal, Unit: "%", Desc: "duty, where the source reports one"},
		{Name: "Fault", Kind: ir.TypeBool, Desc: "source reports the fan unhealthy, or present and stopped"},
	}},
	{Name: "PSU", Desc: "one power supply", Fields: []Field{
		{Name: "Name", Kind: ir.TypeString},
		{Name: "Present", Kind: ir.TypeBool},
		{Name: "Fault", Kind: ir.TypeBool},
		{Name: "InputOk", Kind: ir.TypeBool, Desc: "line input present"},
		{Name: "InputV", Kind: ir.TypeReal, Unit: "V"},
		{Name: "OutputW", Kind: ir.TypeReal, Unit: "W"},
		{Name: "CapacityW", Kind: ir.TypeReal, Unit: "W"},
	}},
	{Name: "TempSensor", Desc: "one temperature sensor with the device's own thresholds", Fields: []Field{
		{Name: "Name", Kind: ir.TypeString},
		{Name: "Value", Kind: ir.TypeReal, Unit: "°C"},
		{Name: "Fault", Kind: ir.TypeBool, Desc: "reading absent or invalid on a present sensor"},
		{Name: "High", Kind: ir.TypeBool, Desc: "Value at or above HighSP"},
		{Name: "HighHigh", Kind: ir.TypeBool, Desc: "Value at or above HighHighSP"},
		{Name: "HighSP", Kind: ir.TypeReal, Unit: "°C", Desc: "warning threshold, 0 when the device has none"},
		{Name: "HighHighSP", Kind: ir.TypeReal, Unit: "°C", Desc: "critical threshold, 0 when the device has none"},
	}},
	{Name: "Switch", Desc: "the chassis of a managed switch", Fields: []Field{
		{Name: "Online", Kind: ir.TypeBool},
		{Name: "Name", Kind: ir.TypeString, Desc: "sysName"},
		{Name: "Model", Kind: ir.TypeString},
		{Name: "Serial", Kind: ir.TypeString},
		{Name: "UptimeS", Kind: ir.TypeInt, Unit: "s"},
		{Name: "CpuPct", Kind: ir.TypeReal, Unit: "%"},
		{Name: "MemPct", Kind: ir.TypeReal, Unit: "%"},
		{Name: "TempC", Kind: ir.TypeReal, Unit: "°C"},
		{Name: "Fault", Kind: ir.TypeBool, Desc: "a PSU or fan child faulted, or the chassis reports a fault"},
		{Name: "PortsTotal", Kind: ir.TypeInt, Desc: "SwitchPort children generated"},
		{Name: "PortsUp", Kind: ir.TypeInt, Desc: "children with OperUp"},
	}},
	{Name: "SwitchPort", Desc: "one interface (IF-MIB)", Fields: []Field{
		{Name: "Index", Kind: ir.TypeInt, Desc: "ifIndex"},
		{Name: "Name", Kind: ir.TypeString, Desc: "ifName"},
		{Name: "Alias", Kind: ir.TypeString, Desc: "ifAlias — the label the operator typed"},
		{Name: "AdminUp", Kind: ir.TypeBool},
		{Name: "OperUp", Kind: ir.TypeBool},
		{Name: "Down", Kind: ir.TypeBool, Desc: "AdminUp and not OperUp — an enabled port with no link"},
		{Name: "SpeedMbps", Kind: ir.TypeReal, Unit: "Mb/s"},
		{Name: "InBps", Kind: ir.TypeReal, Unit: "bit/s"},
		{Name: "OutBps", Kind: ir.TypeReal, Unit: "bit/s"},
		{Name: "InPct", Kind: ir.TypeReal, Unit: "%", Desc: "InBps over line rate"},
		{Name: "OutPct", Kind: ir.TypeReal, Unit: "%", Desc: "OutBps over line rate"},
		{Name: "InErrors", Kind: ir.TypeInt, Desc: "ifInErrors counter"},
		{Name: "OutErrors", Kind: ir.TypeInt, Desc: "ifOutErrors counter"},
		{Name: "InDiscards", Kind: ir.TypeInt},
		{Name: "OutDiscards", Kind: ir.TypeInt},
		{Name: "ErrorRate", Kind: ir.TypeReal, Unit: "1/s", Desc: "errors per second, in plus out"},
		{Name: "PoeOn", Kind: ir.TypeBool, Desc: "delivering power"},
		{Name: "PoeW", Kind: ir.TypeReal, Unit: "W"},
		{Name: "InBroadcastPps", Kind: ir.TypeReal, Unit: "1/s", Desc: "broadcast packets received per second"},
		{Name: "InMulticastPps", Kind: ir.TypeReal, Unit: "1/s", Desc: "multicast packets received per second"},
		{Name: "Pvid", Kind: ir.TypeInt, Desc: "native VLAN: the one untagged frames arriving here join (Q-BRIDGE dot1qPvid)"},
		{Name: "DiscardRate", Kind: ir.TypeReal, Unit: "1/s", Desc: "frames dropped on the way out per second (ifOutDiscards): a port pushed past its line rate"},
	}},
	{Name: "Vlan", Desc: "one VLAN on a switch (Q-BRIDGE-MIB dot1qVlanStaticTable)", Fields: []Field{
		{Name: "Id", Kind: ir.TypeInt, Desc: "VLAN ID"},
		{Name: "Name", Kind: ir.TypeString},
		{Name: "Ports", Kind: ir.TypeString, Desc: "member ports, tagged or untagged, by front-panel position: \"24,25,26\""},
		{Name: "Untagged", Kind: ir.TypeString, Desc: "the member ports sending it untagged, the same way"},
	}},
	{Name: "PDU", Desc: "a switched rack PDU", Fields: []Field{
		{Name: "Online", Kind: ir.TypeBool},
		{Name: "Name", Kind: ir.TypeString},
		{Name: "Model", Kind: ir.TypeString},
		{Name: "Serial", Kind: ir.TypeString},
		{Name: "Amps", Kind: ir.TypeReal, Unit: "A", Desc: "total load"},
		{Name: "Watts", Kind: ir.TypeReal, Unit: "W", Desc: "total load"},
		{Name: "Overload", Kind: ir.TypeBool},
		{Name: "OutletCount", Kind: ir.TypeInt},
	}},
	{Name: "PDUOutlet", Desc: "one PDU outlet; commands are a separate _Cmd output tag", Fields: []Field{
		{Name: "Index", Kind: ir.TypeInt},
		{Name: "Name", Kind: ir.TypeString},
		{Name: "On", Kind: ir.TypeBool, Desc: "read-back of the outlet state"},
		{Name: "Amps", Kind: ir.TypeReal, Unit: "A", Desc: "metered outlets only"},
		{Name: "Watts", Kind: ir.TypeReal, Unit: "W", Desc: "metered outlets only"},
	}},
	{Name: "UPS", Desc: "an uninterruptible power supply (RFC 1628 UPS-MIB)", Fields: []Field{
		{Name: "Online", Kind: ir.TypeBool},
		{Name: "Name", Kind: ir.TypeString},
		{Name: "Model", Kind: ir.TypeString},
		{Name: "Serial", Kind: ir.TypeString},
		{Name: "OnBattery", Kind: ir.TypeBool},
		{Name: "LowBattery", Kind: ir.TypeBool},
		{Name: "BatteryFault", Kind: ir.TypeBool},
		{Name: "Fault", Kind: ir.TypeBool, Desc: "any alarm present"},
		{Name: "ChargePct", Kind: ir.TypeReal, Unit: "%"},
		{Name: "RuntimeMin", Kind: ir.TypeReal, Unit: "min"},
		{Name: "LoadPct", Kind: ir.TypeReal, Unit: "%"},
		{Name: "InputV", Kind: ir.TypeReal, Unit: "V"},
		{Name: "OutputV", Kind: ir.TypeReal, Unit: "V"},
		{Name: "BatteryTempC", Kind: ir.TypeReal, Unit: "°C"},
	}},
	// The server's inside, one tag per part (Redfish Drives, Memory,
	// Processors, PCIeDevices, network ports): what a 3D chassis binds its
	// bays, sockets and slots to. A part that is pulled keeps its tag with
	// Present false; an empty bay has none.
	{Name: "Drive", Desc: "one drive in a bay or on a riser", Fields: []Field{
		{Name: "Bay", Kind: ir.TypeInt, Desc: "the bay's number on its enclosure"},
		{Name: "Name", Kind: ir.TypeString},
		{Name: "Model", Kind: ir.TypeString},
		{Name: "Serial", Kind: ir.TypeString},
		{Name: "CapacityGB", Kind: ir.TypeReal, Unit: "GB"},
		{Name: "Protocol", Kind: ir.TypeString, Desc: "NVMe, SATA, SAS"},
		{Name: "MediaType", Kind: ir.TypeString, Desc: "SSD, HDD"},
		{Name: "Health", Kind: ir.TypeInt, Desc: "0 ok, 1 warning, 2 critical"},
		{Name: "Fault", Kind: ir.TypeBool, Desc: "health warning or critical, or pulled"},
		{Name: "PredictedFailure", Kind: ir.TypeBool, Desc: "the drive predicts its own failure (SMART)"},
		{Name: "TempC", Kind: ir.TypeReal, Unit: "°C"},
		{Name: "Present", Kind: ir.TypeBool, Desc: "false: pulled from its bay"},
	}},
	{Name: "DIMM", Desc: "one memory module", Fields: []Field{
		{Name: "Locator", Kind: ir.TypeString, Desc: "the slot's silkscreen: DIMMA1"},
		{Name: "CapacityGB", Kind: ir.TypeReal, Unit: "GB"},
		{Name: "Manufacturer", Kind: ir.TypeString},
		{Name: "PartNumber", Kind: ir.TypeString},
		{Name: "Health", Kind: ir.TypeInt, Desc: "0 ok, 1 warning, 2 critical"},
		{Name: "Fault", Kind: ir.TypeBool, Desc: "health warning or critical"},
		{Name: "TempC", Kind: ir.TypeReal, Unit: "°C"},
	}},
	{Name: "CPU", Desc: "one processor socket", Fields: []Field{
		{Name: "Model", Kind: ir.TypeString},
		{Name: "Cores", Kind: ir.TypeInt},
		{Name: "Health", Kind: ir.TypeInt, Desc: "0 ok, 1 warning, 2 critical"},
		{Name: "Fault", Kind: ir.TypeBool, Desc: "health warning or critical"},
		{Name: "TempC", Kind: ir.TypeReal, Unit: "°C"},
		{Name: "Pct", Kind: ir.TypeReal, Unit: "%", Desc: "busy"},
	}},
	{Name: "PCIeDevice", Desc: "one card in a PCIe slot", Fields: []Field{
		{Name: "Slot", Kind: ir.TypeInt, Desc: "the system's slot number"},
		{Name: "Name", Kind: ir.TypeString},
		{Name: "Model", Kind: ir.TypeString},
		{Name: "Manufacturer", Kind: ir.TypeString},
		{Name: "Firmware", Kind: ir.TypeString},
		{Name: "Health", Kind: ir.TypeInt, Desc: "0 ok, 1 warning, 2 critical"},
		{Name: "Fault", Kind: ir.TypeBool, Desc: "health warning or critical"},
		{Name: "Ports", Kind: ir.TypeInt, Desc: "network ports, on a NIC"},
		{Name: "TempC", Kind: ir.TypeReal, Unit: "°C"},
	}},
	{Name: "NetPort", Desc: "one network port on a server: a NIC's, the onboard LAN's, the BMC's", Fields: []Field{
		{Name: "Name", Kind: ir.TypeString},
		{Name: "LinkUp", Kind: ir.TypeBool},
		{Name: "SpeedGbps", Kind: ir.TypeReal, Unit: "Gb/s", Desc: "negotiated, while the link is up"},
		{Name: "MAC", Kind: ir.TypeString},
	}},
	// ── the operator-view set (argonaut): what a site EXPECTS, placed ──
	// These are read from a Prometheus server rather than a device, by
	// bindings a site manifest drives; they carry the expectation members
	// (Missing, Conflict, Unexpected) that no exporter can emit, because
	// absence is not a value any exporter has.

	{Name: "VM", Desc: "one guest: a virtual machine that lives on some host and may move", Fields: []Field{
		{Name: "Online", Kind: ir.TypeBool, Desc: "its sources answered"},
		{Name: "Present", Kind: ir.TypeBool, Desc: "some host reports it"},
		{Name: "Running", Kind: ir.TypeBool},
		{Name: "Host", Kind: ir.TypeString, Desc: "the host reporting it this tick"},
		{Name: "Hosts", Kind: ir.TypeInt, Desc: "how many hosts report it: 0 missing, 1 placed, more a conflict"},
		{Name: "Missing", Kind: ir.TypeBool, Desc: "expected, and no host reports it"},
		{Name: "Conflict", Kind: ir.TypeBool, Desc: "more than one host reports it"},
		{Name: "Expected", Kind: ir.TypeString, Desc: "running | stopped | any"},
		{Name: "Unexpected", Kind: ir.TypeBool, Desc: "Running disagrees with Expected"},
		{Name: "Prefer", Kind: ir.TypeString, Desc: "the host it belongs on, if the manifest says"},
		{Name: "Displaced", Kind: ir.TypeBool, Desc: "running on a host other than Prefer"},
		{Name: "OS", Kind: ir.TypeString},
		{Name: "CpuPct", Kind: ir.TypeReal, Unit: "%"},
		{Name: "MemPct", Kind: ir.TypeReal, Unit: "%"},
		{Name: "RootDiskPct", Kind: ir.TypeReal, Unit: "%"},
		{Name: "UptimeS", Kind: ir.TypeInt, Unit: "s"},
		{Name: "ClockOffsetS", Kind: ir.TypeReal, Unit: "s"},
		{Name: "ServicesDown", Kind: ir.TypeInt, Desc: "expected services not running"},
		{Name: "Fault", Kind: ir.TypeBool, Desc: "Missing, Conflict or Unexpected"},
	}},
	{Name: "PromAlerts", Desc: "what the site's own Prometheus rules say about one entity, this tick", Fields: []Field{
		{Name: "Firing", Kind: ir.TypeInt, Desc: "alerts firing with this entity's identity label"},
		{Name: "Critical", Kind: ir.TypeInt},
		{Name: "Warning", Kind: ir.TypeInt},
		{Name: "AnyCritical", Kind: ir.TypeBool},
		{Name: "AnyWarning", Kind: ir.TypeBool},
		{Name: "Worst", Kind: ir.TypeString, Desc: "the name of one firing alert, critical first"},
	}},
	{Name: "ErpsRing", Desc: "one G.8032 ERPS ring", Fields: []Field{
		{Name: "Online", Kind: ir.TypeBool},
		{Name: "Idle", Kind: ir.TypeBool, Desc: "every member reads Idle: whole, RPL blocked"},
		{Name: "SignalFail", Kind: ir.TypeBool, Desc: "any member reports Signal Fail"},
		{Name: "RplBlocked", Kind: ir.TypeBool, Desc: "the RPL owner reports its RPL port blocked"},
		{Name: "PortsForwarding", Kind: ir.TypeInt, Desc: "ring ports forwarding across all members"},
		{Name: "Members", Kind: ir.TypeInt},
		{Name: "FactsAgeS", Kind: ir.TypeReal, Unit: "s", Desc: "age of the collector's last write"},
		{Name: "Stale", Kind: ir.TypeBool, Desc: "FactsAgeS past 15 minutes"},
		{Name: "Fault", Kind: ir.TypeBool, Desc: "not Idle"},
	}},
	{Name: "Probe", Desc: "one probed endpoint", Fields: []Field{
		{Name: "Online", Kind: ir.TypeBool},
		{Name: "Kind", Kind: ir.TypeString, Desc: "http | tcp"},
		{Name: "Target", Kind: ir.TypeString},
		{Name: "Up", Kind: ir.TypeBool, Desc: "the probe succeeded"},
		{Name: "DurationS", Kind: ir.TypeReal, Unit: "s"},
		{Name: "HttpStatus", Kind: ir.TypeInt},
		{Name: "Expected", Kind: ir.TypeString},
		{Name: "Fault", Kind: ir.TypeBool, Desc: "not Up"},
	}},
	{Name: "IncusCluster", Desc: "an Incus cluster", Fields: []Field{
		{Name: "Online", Kind: ir.TypeBool},
		{Name: "MembersTotal", Kind: ir.TypeInt},
		{Name: "MembersOnline", Kind: ir.TypeInt},
		{Name: "HealingThreshold", Kind: ir.TypeInt},
		{Name: "InstancesRunning", Kind: ir.TypeInt},
		{Name: "Fault", Kind: ir.TypeBool, Desc: "a member not online"},
	}},
	{Name: "CephCluster", Desc: "a Ceph cluster", Fields: []Field{
		{Name: "Online", Kind: ir.TypeBool},
		{Name: "HealthStatus", Kind: ir.TypeInt, Desc: "0 OK, 1 WARN, 2 ERR"},
		{Name: "HealthOK", Kind: ir.TypeBool},
		{Name: "Fault", Kind: ir.TypeBool, Desc: "HEALTH_ERR"},
		{Name: "Warning", Kind: ir.TypeBool, Desc: "HEALTH_WARN"},
		{Name: "OsdTotal", Kind: ir.TypeInt},
		{Name: "OsdUp", Kind: ir.TypeInt},
		{Name: "OsdIn", Kind: ir.TypeInt},
		{Name: "MonQuorum", Kind: ir.TypeInt, Desc: "mons in quorum"},
		{Name: "MgrActive", Kind: ir.TypeBool},
		{Name: "PgDegraded", Kind: ir.TypeInt},
		{Name: "ObjectsDegraded", Kind: ir.TypeInt},
		{Name: "UsedPct", Kind: ir.TypeReal, Unit: "%"},
	}},
	{Name: "KubeCluster", Desc: "a Kubernetes cluster", Fields: []Field{
		{Name: "Online", Kind: ir.TypeBool},
		{Name: "NodesTotal", Kind: ir.TypeInt},
		{Name: "NodesReady", Kind: ir.TypeInt},
		{Name: "Fault", Kind: ir.TypeBool, Desc: "a node not Ready"},
	}},
	{Name: "CnpgCluster", Desc: "a CloudNativePG cluster", Fields: []Field{
		{Name: "Online", Kind: ir.TypeBool},
		{Name: "Instances", Kind: ir.TypeInt},
		{Name: "Primaries", Kind: ir.TypeInt, Desc: "instances not in recovery: 1 is healthy"},
		{Name: "Primary", Kind: ir.TypeString, Desc: "the pod that is primary"},
		{Name: "StreamingReplicas", Kind: ir.TypeInt},
		{Name: "LagS", Kind: ir.TypeReal, Unit: "s"},
		{Name: "BackupAgeS", Kind: ir.TypeReal, Unit: "s"},
		{Name: "Fault", Kind: ir.TypeBool, Desc: "no primary, or more than one"},
	}},
}

// TypeByName looks a contract type up.
func TypeByName(name string) (Type, bool) {
	for _, t := range Types {
		if t.Name == name {
			return t, true
		}
	}
	return Type{}, false
}

// FieldOf resolves one member of a contract type: its slot index and Field.
func FieldOf(typeName, member string) (int, Field, bool) {
	t, ok := TypeByName(typeName)
	if !ok {
		return 0, Field{}, false
	}
	for i, f := range t.Fields {
		if f.Name == member {
			return i, f, true
		}
	}
	return 0, Field{}, false
}

// TypeNames lists the contract set, sorted, for error messages.
func TypeNames() string {
	names := make([]string, 0, len(Types))
	for _, t := range Types {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

var (
	defsOnce sync.Once
	defs     map[string]*ir.StructDef
)

// StructDef returns THE ir.StructDef for a contract type — one pointer per
// type per process. The tag store detects a struct change by StructDef
// identity before it compares members (runtime/tags.go sameValue), so a
// driver that minted a fresh definition per delivery would bump the store's
// generation on every scan and re-publish every tag on every Sparkplug tick.
func StructDef(name string) *ir.StructDef {
	defsOnce.Do(func() {
		defs = make(map[string]*ir.StructDef, len(Types))
		for _, t := range Types {
			sd := &ir.StructDef{Name: t.Name, FieldIndex: make(map[string]int, len(t.Fields))}
			for i, f := range t.Fields {
				sd.Fields = append(sd.Fields, ir.StructField{Name: f.Name, Type: scalarType(f.Kind)})
				sd.FieldIndex[f.Name] = i
			}
			defs[t.Name] = sd
		}
	})
	return defs[name]
}

// StructDefs returns every contract type's definition, keyed by name — what
// a driver reports so the project can check the program's TYPE declarations
// against what will actually arrive.
func StructDefs() map[string]*ir.StructDef {
	StructDef("")
	out := make(map[string]*ir.StructDef, len(defs))
	for k, v := range defs {
		out[k] = v
	}
	return out
}

// Zero returns the zero value of a contract type, with its shared StructDef
// attached — the value a tag holds for the members no poll has filled yet.
func Zero(name string) (ir.Value, bool) {
	sd := StructDef(name)
	if sd == nil {
		return ir.Value{}, false
	}
	return ir.Zero(&ir.Type{Kind: ir.TypeStruct, Struct: sd}), true
}

func scalarType(k ir.TypeKind) *ir.Type {
	switch k {
	case ir.TypeBool:
		return ir.BoolT
	case ir.TypeInt:
		return ir.IntT
	case ir.TypeReal:
		return ir.RealT
	case ir.TypeString:
		return ir.StringT
	}
	panic(fmt.Sprintf("hw: contract field of kind %s", k))
}

func stgenType(k ir.TypeKind) stgen.Type {
	switch k {
	case ir.TypeBool:
		return stgen.BOOL
	case ir.TypeInt:
		return stgen.DINT
	case ir.TypeReal:
		return stgen.REAL
	default:
		return stgen.STRING
	}
}

// TypesST renders hw_types.st: every contract TYPE, in table order, with a
// header naming which importer wrote it. Any of the three importers writes
// the same bytes apart from that one header line, so a project with a
// switch over SNMP and a host over Prometheus keeps one types file that
// either regeneration reproduces.
func TypesST(importer string) ([]byte, error) {
	var sb strings.Builder
	sb.WriteString("(*\n")
	fmt.Fprintf(&sb, "  Generated by `naut %s import`: the IT-hardware UDT set from\n", importer)
	sb.WriteString("  docs/design/it-drivers.md §2. Do not edit — every importer (snmp, redfish,\n")
	sb.WriteString("  prometheus) writes this same file, so re-running any of them restores it.\n")
	sb.WriteString("  Member order is the wire order the drivers deliver; the compiled TYPE\n")
	sb.WriteString("  must match it slot for slot, which is why it is generated and not typed.\n")
	sb.WriteString("*)\n\n")
	structs := make([]*stgen.StructDef, 0, len(Types))
	for _, t := range Types {
		s := stgen.Struct(t.Name)
		for _, f := range t.Fields {
			s.AddField(stgen.Field(f.Name, stgenType(f.Kind)))
		}
		structs = append(structs, s)
	}
	block, err := stgen.Render(structs...)
	if err != nil {
		return nil, err
	}
	sb.WriteString(block)
	return []byte(sb.String()), nil
}
