package codegen

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/redfish/mockup"
)

// partsTree is a server with one of each part, shaped like the X14 1U:
// drives on a backplane and behind a RAID card, an empty DIMM slot, a CPU
// temperature sensor, a NIC on a riser whose label numbers the system slot
// in parentheses, onboard LAN and the BMC's own port.
func partsTree() mockup.Tree {
	j := func(s string) json.RawMessage { return json.RawMessage(s) }
	return mockup.Tree{
		"/redfish/v1":         j(`{"Systems":{"@odata.id":"/redfish/v1/Systems"},"Chassis":{"@odata.id":"/redfish/v1/Chassis"}}`),
		"/redfish/v1/Systems": j(`{"Members":[{"@odata.id":"/redfish/v1/Systems/1"}]}`),
		"/redfish/v1/Systems/1": j(`{"Id":"1","PowerState":"On","Links":{"Chassis":[{"@odata.id":"/redfish/v1/Chassis/1"}],"ManagedBy":[{"@odata.id":"/redfish/v1/Managers/1"}]},
			"Memory":{"@odata.id":"/redfish/v1/Systems/1/Memory"},"Processors":{"@odata.id":"/redfish/v1/Systems/1/Processors"},
			"EthernetInterfaces":{"@odata.id":"/redfish/v1/Systems/1/EthernetInterfaces"}}`),
		"/redfish/v1/Chassis": j(`{"Members":[{"@odata.id":"/redfish/v1/Chassis/1"},{"@odata.id":"/redfish/v1/Chassis/BP"},{"@odata.id":"/redfish/v1/Chassis/RAID"}]}`),
		"/redfish/v1/Chassis/1": j(`{"ThermalSubsystem":{"@odata.id":"/redfish/v1/Chassis/1/ThermalSubsystem"},"Sensors":{"@odata.id":"/redfish/v1/Chassis/1/Sensors"},
			"PCIeDevices":{"@odata.id":"/redfish/v1/Chassis/1/PCIeDevices"},"NetworkAdapters":{"@odata.id":"/redfish/v1/Chassis/1/NetworkAdapters"}}`),
		"/redfish/v1/Chassis/1/ThermalSubsystem": j(`{}`),
		"/redfish/v1/Chassis/1/Sensors":          j(`{"Members":[{"@odata.id":"/redfish/v1/Chassis/1/Sensors/CPUTemp"},{"@odata.id":"/redfish/v1/Chassis/1/Sensors/NIC3"}]}`),
		"/redfish/v1/Chassis/1/Sensors/CPUTemp":  j(`{"Name":"CPU Temp","ReadingType":"Temperature","Reading":50}`),
		"/redfish/v1/Chassis/1/Sensors/NIC3":     j(`{"Name":"AOC_NIC3 Temp","ReadingType":"Temperature","Reading":60}`),
		"/redfish/v1/Chassis/BP":                 j(`{"Drives":{"@odata.id":"/redfish/v1/Chassis/BP/Drives"}}`),
		"/redfish/v1/Chassis/BP/Drives":          j(`{"Members":[{"@odata.id":"/redfish/v1/Chassis/BP/Drives/0"}]}`),
		"/redfish/v1/Chassis/BP/Drives/0": j(`{"Name":"Disk.Bay.0","Model":"M","SerialNumber":"S","Protocol":"NVMe","CapacityBytes":1920000000000,"FailurePredicted":false,
			"PhysicalLocation":{"PartLocation":{"LocationOrdinalValue":0,"LocationType":"Slot"}},"Status":{"Health":"OK","State":"Enabled"}}`),
		"/redfish/v1/Chassis/RAID":        j(`{"Drives":{"@odata.id":"/redfish/v1/Chassis/RAID/Drives"}}`),
		"/redfish/v1/Chassis/RAID/Drives": j(`{"Members":[{"@odata.id":"/redfish/v1/Chassis/RAID/Drives/4"}]}`),
		"/redfish/v1/Chassis/RAID/Drives/4": j(`{"Name":"Disk.Bay.4","Protocol":"SATA","MediaType":"SSD",
			"PhysicalLocation":{"PartLocation":{"LocationOrdinalValue":4,"LocationType":"Slot"}},"Status":{"Health":"OK","State":"Enabled"}}`),
		"/redfish/v1/Systems/1/Memory":   j(`{"Members":[{"@odata.id":"/redfish/v1/Systems/1/Memory/1"},{"@odata.id":"/redfish/v1/Systems/1/Memory/2"}]}`),
		"/redfish/v1/Systems/1/Memory/1": j(`{"DeviceLocator":"DIMMA1","CapacityMiB":65536,"Manufacturer":"Samsung","PartNumber":"P","Status":{"Health":"OK","State":"Enabled"}}`),
		"/redfish/v1/Systems/1/Memory/2": j(`{"DeviceLocator":"DIMMB1","Status":{"State":"Absent"}}`),
		"/redfish/v1/Systems/1/Processors":   j(`{"Members":[{"@odata.id":"/redfish/v1/Systems/1/Processors/1"}]}`),
		"/redfish/v1/Systems/1/Processors/1": j(`{"Model":"Xeon","TotalCores":12,"ProcessorType":"CPU","Status":{"Health":"OK","State":"Enabled"}}`),
		"/redfish/v1/Chassis/1/PCIeDevices":  j(`{"Members":[{"@odata.id":"/redfish/v1/Chassis/1/PCIeDevices/NIC2"},{"@odata.id":"/redfish/v1/Chassis/1/PCIeDevices/SSD"}]}`),
		"/redfish/v1/Chassis/1/PCIeDevices/NIC2": j(`{"Name":"NIC2","Model":"AOC","Manufacturer":"SMC","FirmwareVersion":"1",
			"Slot":{"Location":{"PartLocation":{"LocationOrdinalValue":1,"LocationType":"Slot","ServiceLabel":"SXB2 slot 1(3)"}}},"Status":{"Health":"OK","State":"Enabled"}}`),
		"/redfish/v1/Chassis/1/PCIeDevices/SSD": j(`{"Name":"NVMe","Status":{"Health":"OK","State":"Enabled"}}`),
		"/redfish/v1/Chassis/1/NetworkAdapters": j(`{"Members":[{"@odata.id":"/redfish/v1/Chassis/1/NetworkAdapters/2"}]}`),
		"/redfish/v1/Chassis/1/NetworkAdapters/2": j(`{"Controllers":[{"Location":{"PartLocation":{"LocationOrdinalValue":1,"LocationType":"Slot","ServiceLabel":"SXB2 slot 1 (3)"}}}],
			"Ports":{"@odata.id":"/redfish/v1/Chassis/1/NetworkAdapters/2/Ports"}}`),
		"/redfish/v1/Chassis/1/NetworkAdapters/2/Ports":   j(`{"Members":[{"@odata.id":"/redfish/v1/Chassis/1/NetworkAdapters/2/Ports/1"}]}`),
		"/redfish/v1/Chassis/1/NetworkAdapters/2/Ports/1": j(`{"PortId":"1","LinkStatus":"LinkUp","CurrentSpeedGbps":25,"MaxSpeedGbps":1,"Ethernet":{"AssociatedMACAddresses":["00:00:5E:00:53:01"]}}`),
		"/redfish/v1/Systems/1/EthernetInterfaces":         j(`{"Members":[{"@odata.id":"/redfish/v1/Systems/1/EthernetInterfaces/1"},{"@odata.id":"/redfish/v1/Systems/1/EthernetInterfaces/3"}]}`),
		"/redfish/v1/Systems/1/EthernetInterfaces/1":       j(`{"Description":"OnBoard #1","SpeedMbps":1000,"MACAddress":"00:00:5E:00:53:02"}`),
		"/redfish/v1/Systems/1/EthernetInterfaces/3":       j(`{"Description":"AOC-S25G6-M2S #1","LinkStatus":"LinkUp"}`),
		"/redfish/v1/Managers/1":                           j(`{"EthernetInterfaces":{"@odata.id":"/redfish/v1/Managers/1/EthernetInterfaces"}}`),
		"/redfish/v1/Managers/1/EthernetInterfaces":        j(`{"Members":[{"@odata.id":"/redfish/v1/Managers/1/EthernetInterfaces/ToHost"},{"@odata.id":"/redfish/v1/Managers/1/EthernetInterfaces/1"}]}`),
		"/redfish/v1/Managers/1/EthernetInterfaces/ToHost": j(`{"Id":"ToHost"}`),
		"/redfish/v1/Managers/1/EthernetInterfaces/1":      j(`{"Id":"1","LinkStatus":"LinkUp","SpeedMbps":1000,"MACAddress":"00:00:5E:00:53:03"}`),
	}
}

func TestImportParts(t *testing.T) {
	out, err := Import(context.Background(), TreeGetter(partsTree()), opts())
	if err != nil {
		t.Fatal(err)
	}
	want := "NODE1,NODE1_Temp_CPU,NODE1_Temp_AOC_NIC3,NODE1_Drive_NVMe0,NODE1_Drive_SATA4,NODE1_Dimm_A1,NODE1_Cpu1,NODE1_Pcie_Slot3,NODE1_Nic_Slot3_P1,NODE1_Nic_LAN1,NODE1_Nic_BMC"
	if got := strings.Join(tagNames(out.Manifest), ","); got != want {
		t.Fatalf("tags:\n got %s\nwant %s", got, want)
	}
	tags := map[string]map[string]string{}
	for _, tg := range out.Manifest.Tags {
		tags[tg.Name] = map[string]string{}
		for m, b := range tg.Members {
			res := b.Resource
			if res == "" {
				res = tg.Resource
			}
			tags[tg.Name][m] = res + "#" + b.Path
			if b.Derived != "" {
				tags[tg.Name][m] = "#"
			}
			if b.Const != nil {
				tags[tg.Name][m] = "const"
			}
		}
	}
	for _, c := range []struct{ tag, member, want string }{
		{"NODE1_Drive_NVMe0", "Present", "/redfish/v1/Chassis/BP/Drives/0#Status.State"},
		{"NODE1_Drive_NVMe0", "CapacityGB", "/redfish/v1/Chassis/BP/Drives/0#CapacityBytes"},
		{"NODE1_Drive_NVMe0", "Bay", "const"},
		{"NODE1_Drive_NVMe0", "Fault", "#"}, // derived
		{"NODE1_Cpu1", "TempC", "/redfish/v1/Chassis/1/Sensors/CPUTemp#Reading"},
		{"NODE1_Pcie_Slot3", "TempC", "/redfish/v1/Chassis/1/Sensors/NIC3#Reading"},
		{"NODE1_Nic_Slot3_P1", "SpeedGbps", "/redfish/v1/Chassis/1/NetworkAdapters/2/Ports/1#CurrentSpeedGbps"},
		{"NODE1_Nic_Slot3_P1", "LinkUp", "/redfish/v1/Chassis/1/NetworkAdapters/2/Ports/1#LinkStatus"},
		{"NODE1_Nic_BMC", "LinkUp", "/redfish/v1/Managers/1/EthernetInterfaces/1#LinkStatus"},
	} {
		if got := tags[c.tag][c.member]; got != c.want {
			t.Errorf("%s.%s = %q, want %q", c.tag, c.member, got, c.want)
		}
	}
	for _, tg := range out.Manifest.Tags {
		if tg.Name == "NODE1_Drive_NVMe0" && tg.Members["Fault"].Derived != "Health > 0 || !Present" {
			t.Errorf("a pulled drive is a fault: %q", tg.Members["Fault"].Derived)
		}
		if tg.Name == "NODE1_Nic_LAN1" {
			if _, ok := tg.Members["LinkUp"]; ok {
				t.Error("an interface with no LinkStatus has no LinkUp binding")
			}
		}
	}
}

func TestSlotNumber(t *testing.T) {
	loc := func(label string, ord int) map[string]any {
		return map[string]any{"PartLocation": map[string]any{"LocationType": "Slot", "ServiceLabel": label, "LocationOrdinalValue": float64(ord)}}
	}
	for _, c := range []struct {
		label string
		ord   int
		want  int
	}{{"SXB2 slot 1(3)", 1, 3}, {"SXB1 slot 2 (2)", 2, 2}, {"PCIe 4", 4, 4}} {
		if got, ok := slotNumber(loc(c.label, c.ord)); !ok || got != c.want {
			t.Errorf("%q: %d %v, want %d", c.label, got, ok, c.want)
		}
	}
	if _, ok := slotNumber(map[string]any{"PartLocation": map[string]any{"LocationType": "Bay"}}); ok {
		t.Error("a bay is not a slot")
	}
}

// A fan whose BMC reports no Health (the X14) is at fault when it is there
// and stopped: the contract's own definition, so a stopped fan can alarm.
func TestFanWithoutHealth(t *testing.T) {
	tr := tree(nil)
	tr["/redfish/v1/Chassis/1/ThermalSubsystem"] = json.RawMessage(`{"Fans":{"@odata.id":"/redfish/v1/Chassis/1/ThermalSubsystem/Fans"}}`)
	tr["/redfish/v1/Chassis/1/ThermalSubsystem/Fans"] = json.RawMessage(`{"Members":[{"@odata.id":"/redfish/v1/Chassis/1/ThermalSubsystem/Fans/FAN1"}]}`)
	tr["/redfish/v1/Chassis/1/ThermalSubsystem/Fans/FAN1"] = json.RawMessage(`{"Name":"FAN1","SpeedPercent":{"SpeedRPM":9800},"Status":{"State":"Enabled"}}`)
	out, err := Import(context.Background(), TreeGetter(tr), opts())
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range out.Manifest.Tags {
		if tg.Name == "NODE1_Fan1" {
			if got := tg.Members["Fault"].Derived; got != "Present && RPM < 1" {
				t.Fatalf("Fault = %+v", tg.Members["Fault"])
			}
			return
		}
	}
	t.Fatal("no NODE1_Fan1")
}
