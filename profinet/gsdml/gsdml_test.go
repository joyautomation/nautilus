package gsdml

import (
	"bytes"
	"encoding/xml"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/profinet/device"
)

func sample() Device {
	return Device{
		Identity: device.Identity{Station: "naut-1", VendorName: "nautilus", VendorID: 0x1234, DeviceID: 0x0001, OrderNumber: "NAUTILUS-PN"},
		Subslots: []device.Subslot{
			{Addr: device.Addr{Slot: 1, Subslot: 1}, ModuleIdent: 0x101, SubmoduleIdent: 1, InputLen: 8, OutputLen: 4},
			{Addr: device.Addr{Slot: 1, Subslot: 2}, ModuleIdent: 0x101, SubmoduleIdent: 2, InputLen: 2},
			{Addr: device.Addr{Slot: 3, Subslot: 1}, ModuleIdent: 0x103, SubmoduleIdent: 1, OutputLen: 1},
		},
		MinCycle: 8 * time.Millisecond,
	}
}

func TestGenerate(t *testing.T) {
	out, err := Generate(sample())
	if err != nil {
		t.Fatal(err)
	}
	var doc gsdmlISO15745Profile
	if err := xml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("not XML: %v", err)
	}
	id := doc.ProfileBody.DeviceIdentity
	if id.VendorID != "0x1234" || id.DeviceID != "0x0001" || id.VendorName.Value != "nautilus" {
		t.Fatalf("identity %+v", id)
	}
	dap := doc.ProfileBody.ApplicationProcess.DeviceAccessPointList.DeviceAccessPointItem[0]
	// The DAP idents and the order number are what the device's records
	// report; the engineering tool compares the two.
	if dap.ModuleIdentNumber != "0x00010000" || dap.ModuleInfo.OrderNumber.Value != "NAUTILUS-PN" ||
		dap.DNS_CompatibleName != "naut-1" || dap.PhysicalSlots != "0..3" || dap.MinDeviceInterval != "256" {
		t.Fatalf("DAP %+v", dap)
	}
	sys := dap.SystemDefinedSubmoduleList
	if sys.InterfaceSubmoduleItem[0].SubslotNumber != "32768" || sys.PortSubmoduleItem[0].SubslotNumber != "32769" {
		t.Fatalf("system submodules %+v", sys)
	}
	mods := doc.ProfileBody.ApplicationProcess.ModuleList.ModuleItem
	if len(mods) != 2 || mods[0].ModuleIdentNumber != "0x00000101" || mods[1].ModuleIdentNumber != "0x00000103" {
		t.Fatalf("modules %+v", mods)
	}
	subs := mods[0].VirtualSubmoduleList.VirtualSubmoduleItem
	if len(subs) != 2 || subs[0].IOData.Input.DataItem[0].Length != "8" || subs[0].IOData.Output.DataItem[0].Length != "4" ||
		subs[1].IOData.Output != nil || subs[1].FixedInSubslots != "2" {
		t.Fatalf("slot 1 submodules %+v", subs)
	}
	if refs := dap.UseableModules.ModuleItemRef; len(refs) != 2 || refs[1].AllowedInSlots != "3" {
		t.Fatalf("refs %+v", refs)
	}
	// Every TextId used is defined.
	defined := map[string]bool{}
	for _, tx := range doc.ProfileBody.ApplicationProcess.ExternalTextList.PrimaryLanguage.Text {
		defined[tx.TextId] = true
	}
	for _, m := range regexpTextIDs(string(out)) {
		if !defined[m] {
			t.Errorf("TextId %s not in the text list", m)
		}
	}
}

func regexpTextIDs(s string) []string {
	var out []string
	for _, part := range strings.Split(s, `TextId="`)[1:] {
		out = append(out, part[:strings.IndexByte(part, '"')])
	}
	return out
}

func TestGenerateIsDeterministic(t *testing.T) {
	a, _ := Generate(sample())
	b, _ := Generate(sample())
	if !bytes.Equal(a, b) {
		t.Fatal("two runs differ")
	}
}

func TestGenerateRefusesIncomplete(t *testing.T) {
	d := sample()
	d.VendorID = 0
	if _, err := Generate(d); err == nil {
		t.Fatal("no vendor ID accepted")
	}
}

func TestFilename(t *testing.T) {
	got := Filename(sample(), time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC))
	if got != "GSDML-V2.35-nautilus-naut-1-20261117.xml" {
		t.Fatal(got)
	}
}
