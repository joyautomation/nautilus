package codegen

// parts.go: the server's inside — drives, DIMMs, CPUs, PCIe cards and
// network ports — one tag per part, named the way a 3D chassis profile
// binds its bays, sockets and slots:
//
//	NODE1_Drive_NVMe0      a drive: <Protocol><Bay>, from its enclosure
//	NODE1_Dimm_A1          a DIMM: its DeviceLocator without "DIMM"
//	NODE1_Cpu1             a processor socket
//	NODE1_Pcie_Slot2       a card: the system's slot number
//	NODE1_Nic_Slot2_P1     a NIC port: slot and PortId
//	NODE1_Nic_LAN1         an onboard port ("OnBoard #1")
//	NODE1_Nic_BMC          the BMC's own port
//
// The system slot number is the one in a ServiceLabel's parentheses when
// it has one ("SXB2 slot 1(3)" is slot 3: the riser numbers its own slots,
// the chassis numbers the system's), else LocationOrdinalValue.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/redfish"
)

// parts builds every part tag the service describes.
func (im *importer) parts(root, sys map[string]any, temps []*tagB) ([]*tagB, error) {
	var out []*tagB
	for _, step := range []func() ([]*tagB, error){
		func() ([]*tagB, error) { return im.drives(root, temps) },
		func() ([]*tagB, error) { return im.dimms(sys, temps) },
		func() ([]*tagB, error) { return im.cpus(sys, temps) },
		func() ([]*tagB, error) { return im.pcie(root, temps) },
		func() ([]*tagB, error) { return im.netPorts(root, sys) },
	} {
		ts, err := step()
		if err != nil {
			return nil, err
		}
		out = append(out, ts...)
	}
	return out, nil
}

// each fetches a collection's members, noting the ones listed but not served.
func (im *importer) each(collURI, what string, fn func(uri string, doc map[string]any) error) error {
	coll, ok, err := im.tryGet(collURI)
	if err != nil || !ok {
		return err
	}
	for _, u := range members(coll) {
		doc, ok, err := im.tryGet(u)
		if err != nil {
			return err
		}
		if !ok {
			im.note("%s %s is listed but not served: skipped", what, u)
			continue
		}
		if err := fn(u, doc); err != nil {
			return err
		}
	}
	return nil
}

// partStatus binds Health, Fault and (where the part can be pulled)
// Present from Status. A part that can be pulled is at fault when it is:
// an alarm fires on a member going true, and "the drive is gone" is the
// fault an operator most needs to see.
func partStatus(t *tagB, uri string, doc map[string]any, present bool) {
	health := has(doc, "Status", "Health")
	pull := present && has(doc, "Status", "State")
	if health {
		t.bind("Health", uri, "Status.Health", func(b *redfish.MemberBinding) { b.Map = healthMap() })
	}
	if pull {
		t.bind("Present", uri, "Status.State", func(b *redfish.MemberBinding) { b.Map = presentMap() })
	}
	switch {
	case health && pull:
		t.derived("Fault", "Health > 0 || !Present")
	case health:
		t.derived("Fault", "Health > 0")
	case pull:
		t.derived("Fault", "!Present")
	}
}

func (im *importer) drives(root map[string]any, temps []*tagB) ([]*tagB, error) {
	var out []*tagB
	// One NVMe temperature sensor on the chassis ("NVMe_SSDA Temp"): the
	// NVMe drives' temperature. Several, or none, and it stays unbound.
	var nvme *tagB
	if ss := sensorsLike(temps, `^NVMe_SSD`); len(ss) == 1 {
		nvme = ss[0]
	}
	err := im.each(link(root, "Chassis"), "chassis", func(chURI string, ch map[string]any) error {
		return im.each(link(ch, "Drives"), "drive", func(u string, d map[string]any) error {
			bay, ok := ordinal(d)
			if !ok {
				im.note("drive %s has no bay number (PhysicalLocation): skipped", u)
				return nil
			}
			proto := Sanitise(firstStr(d, "Protocol", "MediaType"))
			if proto == "Sensor" {
				proto = "Drive"
			}
			name := fmt.Sprintf("%s_Drive_%s%d", im.o.Tag, proto, bay)
			if err := im.claim(name, "drive "+u); err != nil {
				return err
			}
			t := newTag(name, "Drive", im.o.Tag)
			t.Desc = strings.TrimSpace(str(d, "Model"))
			t.constant("Bay", bay)
			for member, path := range map[string]string{"Name": "Name", "Model": "Model", "Serial": "SerialNumber", "Protocol": "Protocol", "MediaType": "MediaType"} {
				if has(d, path) {
					t.bind(member, u, path, nil)
				}
			}
			if has(d, "CapacityBytes") {
				t.bind("CapacityGB", u, "CapacityBytes", func(b *redfish.MemberBinding) { b.Scale = 1e-9 })
			}
			if has(d, "FailurePredicted") {
				t.bind("PredictedFailure", u, "FailurePredicted", nil)
			}
			if nvme != nil && str(d, "Protocol") == "NVMe" {
				t.bind("TempC", nvme.valueRes, nvme.valuePath, nil)
			}
			partStatus(t, u, d, true)
			out = append(out, t)
			return nil
		})
	})
	return out, err
}

func (im *importer) dimms(sys map[string]any, temps []*tagB) ([]*tagB, error) {
	var out []*tagB
	err := im.each(link(sys, "Memory"), "DIMM", func(u string, d map[string]any) error {
		loc := firstStr(d, "DeviceLocator", "Id")
		if st := str(obj(d, "Status"), "State"); st == "Absent" {
			return nil // an empty slot is not a part
		}
		name := im.o.Tag + "_Dimm_" + Sanitise(strings.TrimPrefix(loc, "DIMM"))
		if err := im.claim(name, "DIMM "+u); err != nil {
			return err
		}
		t := newTag(name, "DIMM", im.o.Tag)
		t.Desc = loc
		for member, path := range map[string]string{"Locator": "DeviceLocator", "Manufacturer": "Manufacturer", "PartNumber": "PartNumber"} {
			if has(d, path) {
				t.bind(member, u, path, nil)
			}
		}
		if has(d, "CapacityMiB") {
			t.bind("CapacityGB", u, "CapacityMiB", func(b *redfish.MemberBinding) { b.Scale = 1.0 / 1024 })
		}
		if s := dimmSensor(temps, loc); s != nil {
			t.bind("TempC", s.valueRes, s.valuePath, nil)
		}
		partStatus(t, u, d, false)
		out = append(out, t)
		return nil
	})
	return out, err
}

func (im *importer) cpus(sys map[string]any, temps []*tagB) ([]*tagB, error) {
	var out []*tagB
	err := im.each(link(sys, "Processors"), "processor", func(u string, d map[string]any) error {
		if pt := str(d, "ProcessorType"); pt != "" && pt != "CPU" {
			return nil // a GPU or accelerator is not a socket
		}
		if st := str(obj(d, "Status"), "State"); st == "Absent" {
			return nil
		}
		name := fmt.Sprintf("%s_Cpu%d", im.o.Tag, len(out)+1)
		if err := im.claim(name, "processor "+u); err != nil {
			return err
		}
		t := newTag(name, "CPU", im.o.Tag)
		t.Desc = str(d, "Model")
		if has(d, "Model") {
			t.bind("Model", u, "Model", nil)
		}
		if has(d, "TotalCores") {
			t.bind("Cores", u, "TotalCores", nil)
		}
		partStatus(t, u, d, false)
		out = append(out, t)
		return nil
	})
	// One socket: the chassis' "CPU" temperature sensor is its temperature.
	if len(out) == 1 {
		if s := sensor(temps, "CPU"); s != nil {
			out[0].bind("TempC", s.valueRes, s.valuePath, nil)
		}
	}
	return out, err
}

func (im *importer) pcie(root map[string]any, temps []*tagB) ([]*tagB, error) {
	var out []*tagB
	err := im.each(link(root, "Chassis"), "chassis", func(_ string, ch map[string]any) error {
		return im.each(link(ch, "PCIeDevices"), "PCIe device", func(u string, d map[string]any) error {
			slot, ok := slotNumber(obj(obj(d, "Slot"), "Location"))
			if !ok {
				return nil // on-board or behind a backplane (an NVMe drive): not a card in a slot
			}
			name := fmt.Sprintf("%s_Pcie_Slot%d", im.o.Tag, slot)
			if err := im.claim(name, "PCIe device "+u); err != nil {
				return err
			}
			t := newTag(name, "PCIeDevice", im.o.Tag)
			t.Desc = str(d, "Name")
			t.constant("Slot", slot)
			for member, path := range map[string]string{"Name": "Name", "Model": "Model", "Manufacturer": "Manufacturer", "Firmware": "FirmwareVersion"} {
				if has(d, path) {
					t.bind(member, u, path, nil)
				}
			}
			partStatus(t, u, d, false)
			if s := sensor(temps, fmt.Sprintf("AOC_NIC%d", slot)); s != nil {
				t.bind("TempC", s.valueRes, s.valuePath, nil)
			}
			out = append(out, t)
			return nil
		})
	})
	return out, err
}

func (im *importer) netPorts(root, sys map[string]any) ([]*tagB, error) {
	var out []*tagB
	// Add-in NICs: NetworkAdapters/*/Ports/*, by the adapter's slot.
	err := im.each(link(root, "Chassis"), "chassis", func(_ string, ch map[string]any) error {
		return im.each(link(ch, "NetworkAdapters"), "network adapter", func(au string, a map[string]any) error {
			var slot int
			var ok bool
			for _, c := range arr(a, "Controllers") {
				if cm, _ := c.(map[string]any); cm != nil {
					if slot, ok = slotNumber(obj(cm, "Location")); ok {
						break
					}
				}
			}
			if !ok {
				im.note("network adapter %s has no slot: its ports are skipped", au)
				return nil
			}
			n := 0
			return im.each(link(a, "Ports"), "network port", func(u string, p map[string]any) error {
				n++
				id := firstStr(p, "PortId", "Id")
				if id == "" {
					id = strconv.Itoa(n)
				}
				name := fmt.Sprintf("%s_Nic_Slot%d_P%s", im.o.Tag, slot, Sanitise(id))
				if err := im.claim(name, "network port "+u); err != nil {
					return err
				}
				t := newTag(name, "NetPort", im.o.Tag)
				t.Desc = fmt.Sprintf("slot %d port %s", slot, id)
				t.constant("Name", t.Desc)
				if has(p, "LinkStatus") {
					t.bind("LinkUp", u, "LinkStatus", func(b *redfish.MemberBinding) { b.Eq = "LinkUp" })
				}
				// CurrentSpeedGbps, never MaxSpeedGbps: BMCs misreport the
				// maximum (a 25G port reading 1.0).
				if has(p, "CurrentSpeedGbps") {
					t.bind("SpeedGbps", u, "CurrentSpeedGbps", nil)
				}
				if macs := arr(obj(p, "Ethernet"), "AssociatedMACAddresses"); len(macs) > 0 {
					if m, ok := macs[0].(string); ok {
						t.constant("MAC", m)
					}
				}
				out = append(out, t)
				return nil
			})
		})
	})
	if err != nil {
		return nil, err
	}
	// Onboard LAN: the system's EthernetInterfaces that say "OnBoard #n".
	err = im.each(link(sys, "EthernetInterfaces"), "Ethernet interface", func(u string, e map[string]any) error {
		m := onboardRE.FindStringSubmatch(str(e, "Description"))
		if m == nil {
			return nil // an add-in NIC's function (bound above) or the host interface
		}
		t, err := im.ethPort(fmt.Sprintf("%s_Nic_LAN%s", im.o.Tag, m[1]), "LAN"+m[1], u, e)
		if err == nil {
			out = append(out, t)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	// The BMC's own port: its manager's first EthernetInterface.
	for _, mu := range linkList(sys, "Links", "ManagedBy") {
		mgr, ok, err := im.tryGet(mu)
		if err != nil || !ok {
			return out, err
		}
		coll, ok, err := im.tryGet(link(mgr, "EthernetInterfaces"))
		if err != nil || !ok {
			return out, err
		}
		for _, u := range members(coll) {
			e, ok, err := im.tryGet(u)
			if err != nil {
				return out, err
			}
			if !ok || str(e, "Id") == "ToHost" {
				continue
			}
			t, err := im.ethPort(im.o.Tag+"_Nic_BMC", "BMC", u, e)
			if err != nil {
				return out, err
			}
			return append(out, t), nil
		}
	}
	return out, nil
}

var onboardRE = regexp.MustCompile(`(?i)^on-?board\s*#?\s*(\d+)$`)

// ethPort is a NetPort over an EthernetInterface resource.
func (im *importer) ethPort(name, label, u string, e map[string]any) (*tagB, error) {
	if err := im.claim(name, "Ethernet interface "+u); err != nil {
		return nil, err
	}
	t := newTag(name, "NetPort", im.o.Tag)
	t.Desc = label
	t.constant("Name", label)
	if has(e, "LinkStatus") {
		t.bind("LinkUp", u, "LinkStatus", func(b *redfish.MemberBinding) { b.Eq = "LinkUp" })
	}
	if has(e, "SpeedMbps") {
		t.bind("SpeedGbps", u, "SpeedMbps", func(b *redfish.MemberBinding) { b.Scale = 0.001 })
	}
	if m := str(e, "MACAddress"); m != "" {
		t.constant("MAC", m)
	}
	return t, nil
}

// dimmGroup reads a DIMM temperature sensor's name: "DIMMA~D Temp" covers
// channels A to D, "DIMM Temp" every DIMM.
var dimmGroup = regexp.MustCompile(`^DIMM(?:([A-Z])(?:~([A-Z]))?)?\b`)

// dimmSensor is the sensor that covers the DIMM at locator (DIMMA1).
func dimmSensor(temps []*tagB, locator string) *tagB {
	ch := strings.TrimPrefix(strings.ToUpper(locator), "DIMM")
	if ch == "" {
		return nil
	}
	var hit *tagB
	for _, t := range temps {
		m := dimmGroup.FindStringSubmatch(t.Desc)
		if m == nil {
			continue
		}
		lo, hi := m[1], m[2]
		if hi == "" {
			hi = lo
		}
		if lo == "" || (ch[:1] >= lo && ch[:1] <= hi) {
			if hit != nil {
				return nil // two sensors claim it: bind neither
			}
			hit = t
		}
	}
	return hit
}

// sensorsLike are the TempSensor tags whose sanitised name matches pattern.
func sensorsLike(temps []*tagB, pattern string) []*tagB {
	re := regexp.MustCompile(pattern)
	var out []*tagB
	for _, t := range temps {
		if re.MatchString(Sanitise(t.Desc)) {
			out = append(out, t)
		}
	}
	return out
}

// sensor finds the TempSensor tag whose own name sanitises to label.
func sensor(temps []*tagB, label string) *tagB {
	for _, t := range temps {
		if Sanitise(t.Desc) == label {
			return t
		}
	}
	return nil
}

// ordinal is a part's PhysicalLocation.PartLocation.LocationOrdinalValue.
func ordinal(doc map[string]any) (int, bool) {
	pl := obj(obj(doc, "PhysicalLocation"), "PartLocation")
	return intOf(pl["LocationOrdinalValue"])
}

var parenSlot = regexp.MustCompile(`\((\d+)\)\s*$`)

// slotNumber is the system's slot number from a Location: the ServiceLabel's
// parenthesised number, else LocationOrdinalValue, for LocationType Slot.
func slotNumber(loc map[string]any) (int, bool) {
	pl := obj(loc, "PartLocation")
	if pl == nil || str(pl, "LocationType") != "Slot" {
		return 0, false
	}
	if m := parenSlot.FindStringSubmatch(str(pl, "ServiceLabel")); m != nil {
		n, err := strconv.Atoi(m[1])
		return n, err == nil
	}
	return intOf(pl["LocationOrdinalValue"])
}

func intOf(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		return int(x), true
	case interface{ Int64() (int64, error) }:
		n, err := x.Int64()
		return int(n), err == nil
	}
	return 0, false
}
