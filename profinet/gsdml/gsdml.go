// Package gsdml writes the GSDML file (GSD Markup Language, the XML an
// engineering tool such as TIA Portal imports to know a PROFINET device)
// for a nautilus PROFINET device, from the same layout the device serves.
// The structure is the one tentacle-next's generator settled on against
// TIA Portal and PRONETA.
package gsdml

import (
	"encoding/xml"
	"fmt"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/profinet/device"
)

// Device is what the GSDML describes.
type Device struct {
	device.Identity
	Subslots []device.Subslot
	MinCycle time.Duration // MinDeviceInterval
}

// Generate returns the GSDML document.
func Generate(d Device) ([]byte, error) {
	if d.VendorID == 0 || d.DeviceID == 0 || d.Station == "" || len(d.Subslots) == 0 {
		return nil, fmt.Errorf("gsdml: vendor ID, device ID, station and at least one subslot are required")
	}
	out, err := xml.MarshalIndent(build(d), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("gsdml: %w", err)
	}
	return append([]byte(xml.Header), append(out, '\n')...), nil
}

// Filename is the conventional name, GSDML-V2.35-<vendor>-<device>-<date>.xml.
func Filename(d Device, date time.Time) string {
	return fmt.Sprintf("GSDML-V2.35-%s-%s-%s.xml", sanitize(d.VendorName), sanitize(d.Station), date.Format("20060102"))
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// GSDML XML types — structured to produce valid GSDML V2.35 output.
// Modeled after known-good p-net GSDML files that PRONETA accepts.

type gsdmlISO15745Profile struct {
	XMLName        xml.Name           `xml:"ISO15745Profile"`
	Xmlns          string             `xml:"xmlns,attr"`
	XSI            string             `xml:"xmlns:xsi,attr"`
	SchemaLocation string             `xml:"xsi:schemaLocation,attr"`
	ProfileHeader  gsdmlProfileHeader `xml:"ProfileHeader"`
	ProfileBody    gsdmlProfileBody   `xml:"ProfileBody"`
}

type gsdmlProfileHeader struct {
	ProfileIdentification string `xml:"ProfileIdentification"`
	ProfileRevision       string `xml:"ProfileRevision"`
	ProfileName           string `xml:"ProfileName"`
	ProfileSource         string `xml:"ProfileSource"`
	ProfileClassID        string `xml:"ProfileClassID"`
	ISO15745Reference     struct {
		ISO15745Part      int    `xml:"ISO15745Part"`
		ISO15745Edition   int    `xml:"ISO15745Edition"`
		ProfileTechnology string `xml:"ProfileTechnology"`
	} `xml:"ISO15745Reference"`
}

type gsdmlProfileBody struct {
	DeviceIdentity     gsdmlDeviceIdentity     `xml:"DeviceIdentity"`
	DeviceFunction     gsdmlDeviceFunction     `xml:"DeviceFunction"`
	ApplicationProcess gsdmlApplicationProcess `xml:"ApplicationProcess"`
}

type gsdmlDeviceIdentity struct {
	VendorID   string        `xml:"VendorID,attr"`
	DeviceID   string        `xml:"DeviceID,attr"`
	InfoText   *gsdmlTextRef `xml:"InfoText,omitempty"`
	VendorName *gsdmlValue   `xml:"VendorName,omitempty"`
}

type gsdmlTextRef struct {
	TextId string `xml:"TextId,attr"`
}

type gsdmlValue struct {
	Value string `xml:"Value,attr"`
}

type gsdmlDeviceFunction struct {
	Family gsdmlFamily `xml:"Family"`
}

type gsdmlFamily struct {
	MainFamily    string `xml:"MainFamily,attr"`
	ProductFamily string `xml:"ProductFamily,attr,omitempty"`
}

type gsdmlApplicationProcess struct {
	DeviceAccessPointList gsdmlDAPList          `xml:"DeviceAccessPointList"`
	ModuleList            *gsdmlModuleList      `xml:"ModuleList,omitempty"`
	ExternalTextList      gsdmlExternalTextList `xml:"ExternalTextList"`
}

type gsdmlDAPList struct {
	DeviceAccessPointItem []gsdmlDAPItem `xml:"DeviceAccessPointItem"`
}

type gsdmlDAPItem struct {
	ID                           string `xml:"ID,attr"`
	PNIO_Version                 string `xml:"PNIO_Version,attr"`
	PhysicalSlots                string `xml:"PhysicalSlots,attr"`
	ModuleIdentNumber            string `xml:"ModuleIdentNumber,attr"`
	MinDeviceInterval            string `xml:"MinDeviceInterval,attr"`
	DNS_CompatibleName           string `xml:"DNS_CompatibleName,attr"`
	FixedInSlots                 string `xml:"FixedInSlots,attr"`
	ObjectUUID_LocalIndex        string `xml:"ObjectUUID_LocalIndex,attr"`
	DeviceAccessSupported        string `xml:"DeviceAccessSupported,attr"`
	NumberOfDeviceAccessAR       string `xml:"NumberOfDeviceAccessAR,attr"`
	MultipleWriteSupported       string `xml:"MultipleWriteSupported,attr"`
	CheckDeviceID_Allowed        string `xml:"CheckDeviceID_Allowed,attr"`
	NameOfStationNotTransferable string `xml:"NameOfStationNotTransferable,attr"`
	LLDP_NoD_Supported           string `xml:"LLDP_NoD_Supported,attr"`
	ResetToFactoryModes          string `xml:"ResetToFactoryModes,attr"`

	ModuleInfo                 *gsdmlModuleInfo           `xml:"ModuleInfo,omitempty"`
	CertificationInfo          *gsdmlCertificationInfo    `xml:"CertificationInfo,omitempty"`
	IOConfigData               *gsdmlIOConfigData         `xml:"IOConfigData,omitempty"`
	UseableModules             *gsdmlUseableModules       `xml:"UseableModules,omitempty"`
	VirtualSubmoduleList       *gsdmlVirtualSubmoduleList `xml:"VirtualSubmoduleList,omitempty"`
	SystemDefinedSubmoduleList *gsdmlSystemSubmoduleList  `xml:"SystemDefinedSubmoduleList,omitempty"`
}

type gsdmlCertificationInfo struct {
	ConformanceClass string `xml:"ConformanceClass,attr"`
	ApplicationClass string `xml:"ApplicationClass,attr"`
	NetloadClass     string `xml:"NetloadClass,attr"`
}

type gsdmlIOConfigData struct {
	MaxInputLength  string `xml:"MaxInputLength,attr"`
	MaxOutputLength string `xml:"MaxOutputLength,attr"`
}

type gsdmlModuleInfo struct {
	Name        *gsdmlTextRef `xml:"Name,omitempty"`
	InfoText    *gsdmlTextRef `xml:"InfoText,omitempty"`
	VendorName  *gsdmlValue   `xml:"VendorName,omitempty"`
	OrderNumber *gsdmlValue   `xml:"OrderNumber,omitempty"`
}

type gsdmlUseableModules struct {
	ModuleItemRef []gsdmlModuleItemRef `xml:"ModuleItemRef"`
}

type gsdmlModuleItemRef struct {
	ModuleItemTarget string `xml:"ModuleItemTarget,attr"`
	AllowedInSlots   string `xml:"AllowedInSlots,attr,omitempty"`
	FixedInSlots     string `xml:"FixedInSlots,attr,omitempty"`
}

type gsdmlSystemSubmoduleList struct {
	InterfaceSubmoduleItem []gsdmlInterfaceSubmodule `xml:"InterfaceSubmoduleItem"`
	PortSubmoduleItem      []gsdmlPortSubmodule      `xml:"PortSubmoduleItem"`
}

type gsdmlInterfaceSubmodule struct {
	ID                    string                     `xml:"ID,attr"`
	SubmoduleIdentNumber  string                     `xml:"SubmoduleIdentNumber,attr"`
	SubslotNumber         string                     `xml:"SubslotNumber,attr"`
	TextId                string                     `xml:"TextId,attr"`
	SupportedRT_Classes   string                     `xml:"SupportedRT_Classes,attr"`
	SupportedProtocols    string                     `xml:"SupportedProtocols,attr,omitempty"`
	PTP_BoundarySupported string                     `xml:"PTP_BoundarySupported,attr"`
	DCP_BoundarySupported string                     `xml:"DCP_BoundarySupported,attr"`
	NetworkInterface      string                     `xml:"NetworkInterface,attr,omitempty"`
	ApplicationRelations  *gsdmlApplicationRelations `xml:"ApplicationRelations,omitempty"`
}

type gsdmlApplicationRelations struct {
	StartupMode      string                `xml:"StartupMode,attr"`
	TimingProperties gsdmlTimingProperties `xml:"TimingProperties"`
}

type gsdmlTimingProperties struct {
	SendClock      string `xml:"SendClock,attr"`
	ReductionRatio string `xml:"ReductionRatio,attr"`
}

type gsdmlPortSubmodule struct {
	ID                   string            `xml:"ID,attr"`
	SubmoduleIdentNumber string            `xml:"SubmoduleIdentNumber,attr"`
	SubslotNumber        string            `xml:"SubslotNumber,attr"`
	TextId               string            `xml:"TextId,attr"`
	MaxPortRxDelay       string            `xml:"MaxPortRxDelay,attr"`
	MaxPortTxDelay       string            `xml:"MaxPortTxDelay,attr"`
	MAUTypeList          *gsdmlMAUTypeList `xml:"MAUTypeList,omitempty"`
}

type gsdmlMAUTypeList struct {
	MAUTypeItem []gsdmlMAUTypeItem `xml:"MAUTypeItem"`
}

type gsdmlMAUTypeItem struct {
	Value string `xml:"Value,attr"`
}

type gsdmlModuleList struct {
	ModuleItem []gsdmlModuleItem `xml:"ModuleItem"`
}

type gsdmlModuleItem struct {
	ID                   string                     `xml:"ID,attr"`
	ModuleIdentNumber    string                     `xml:"ModuleIdentNumber,attr"`
	ModuleInfo           *gsdmlModuleInfo           `xml:"ModuleInfo,omitempty"`
	VirtualSubmoduleList *gsdmlVirtualSubmoduleList `xml:"VirtualSubmoduleList,omitempty"`
}

type gsdmlVirtualSubmoduleList struct {
	VirtualSubmoduleItem []gsdmlVirtualSubmoduleItem `xml:"VirtualSubmoduleItem"`
}

type gsdmlVirtualSubmoduleItem struct {
	ID                   string           `xml:"ID,attr"`
	SubmoduleIdentNumber string           `xml:"SubmoduleIdentNumber,attr"`
	FixedInSubslots      string           `xml:"FixedInSubslots,attr,omitempty"`
	MayIssueProcessAlarm string           `xml:"MayIssueProcessAlarm,attr,omitempty"`
	IOData               *gsdmlIOData     `xml:"IOData,omitempty"`
	ModuleInfo           *gsdmlModuleInfo `xml:"ModuleInfo,omitempty"`
}

type gsdmlIOData struct {
	Input  *gsdmlIODataDir `xml:"Input,omitempty"`
	Output *gsdmlIODataDir `xml:"Output,omitempty"`
}

type gsdmlIODataDir struct {
	DataItem []gsdmlDataItem `xml:"DataItem"`
}

type gsdmlDataItem struct {
	DataType string `xml:"DataType,attr"`
	Length   string `xml:"Length,attr,omitempty"`
	TextId   string `xml:"TextId,attr"`
}

// ExternalTextList (GSDML standard text format)
type gsdmlExternalTextList struct {
	PrimaryLanguage gsdmlPrimaryLanguage `xml:"PrimaryLanguage"`
}

type gsdmlPrimaryLanguage struct {
	Text []gsdmlTextEntry `xml:"Text"`
}

type gsdmlTextEntry struct {
	TextId string `xml:"TextId,attr"`
	Value  string `xml:"Value,attr"`
}

func build(d Device) gsdmlISO15745Profile {
	var texts []gsdmlTextEntry
	addText := func(id, value string) { texts = append(texts, gsdmlTextEntry{TextId: id, Value: value}) }
	addText("IDT_INFO_Device", d.VendorName+" PROFINET IO device "+d.Station)
	addText("IDT_NAME_DAP", "DAP")
	addText("IDT_INFO_DAP", "Device Access Point")
	addText("IDT_NAME_Interface", "Interface")
	addText("IDT_NAME_Port1", "Port 1")

	// Group the subslots by slot (the layout is sorted).
	type slot struct {
		num   uint16
		ident uint32
		subs  []device.Subslot
	}
	var slots []*slot
	for _, s := range d.Subslots {
		if len(slots) == 0 || slots[len(slots)-1].num != s.Slot {
			slots = append(slots, &slot{num: s.Slot, ident: s.ModuleIdent})
		}
		slots[len(slots)-1].subs = append(slots[len(slots)-1].subs, s)
	}
	maxSlot := 1
	var items []gsdmlModuleItem
	var refs []gsdmlModuleItemRef
	for _, sl := range slots {
		maxSlot = max(maxSlot, int(sl.num))
		modID := fmt.Sprintf("IDM_Mod%d", sl.num)
		nameID, infoID := fmt.Sprintf("IDT_NAME_Mod%d", sl.num), fmt.Sprintf("IDT_INFO_Mod%d", sl.num)
		addText(nameID, fmt.Sprintf("Slot %d", sl.num))
		addText(infoID, fmt.Sprintf("Module in slot %d", sl.num))
		var subs []gsdmlVirtualSubmoduleItem
		for _, s := range sl.subs {
			sn, si := fmt.Sprintf("IDT_NAME_Mod%d_Sub%d", s.Slot, s.Subslot), fmt.Sprintf("IDT_INFO_Mod%d_Sub%d", s.Slot, s.Subslot)
			addText(sn, fmt.Sprintf("Slot %d subslot %d", s.Slot, s.Subslot))
			addText(si, fmt.Sprintf("%d bytes in, %d bytes out", s.InputLen, s.OutputLen))
			subs = append(subs, gsdmlVirtualSubmoduleItem{
				ID:                   fmt.Sprintf("IDS_Mod%d_Sub%d", s.Slot, s.Subslot),
				SubmoduleIdentNumber: fmt.Sprintf("0x%08X", s.SubmoduleIdent),
				FixedInSubslots:      fmt.Sprintf("%d", s.Subslot),
				MayIssueProcessAlarm: "false",
				IOData:               ioData(s, addText),
				ModuleInfo:           &gsdmlModuleInfo{Name: &gsdmlTextRef{TextId: sn}, InfoText: &gsdmlTextRef{TextId: si}},
			})
		}
		items = append(items, gsdmlModuleItem{
			ID:                   modID,
			ModuleIdentNumber:    fmt.Sprintf("0x%08X", sl.ident),
			ModuleInfo:           &gsdmlModuleInfo{Name: &gsdmlTextRef{TextId: nameID}, InfoText: &gsdmlTextRef{TextId: infoID}},
			VirtualSubmoduleList: &gsdmlVirtualSubmoduleList{VirtualSubmoduleItem: subs},
		})
		refs = append(refs, gsdmlModuleItemRef{ModuleItemTarget: modID, AllowedInSlots: fmt.Sprintf("%d", sl.num)})
	}

	// MinDeviceInterval is in units of 31.25 µs; 32 (1 ms) at least.
	minInterval := max(int(d.MinCycle/(31250*time.Nanosecond)), 32)

	p := gsdmlISO15745Profile{
		Xmlns:          "http://www.profibus.com/GSDML/2003/11/DeviceProfile",
		XSI:            "http://www.w3.org/2001/XMLSchema-instance",
		SchemaLocation: "http://www.profibus.com/GSDML/2003/11/DeviceProfile ..\\xsd\\GSDML-DeviceProfile-V2.35.xsd",
	}
	p.ProfileHeader.ProfileIdentification = "PROFINET Device Profile"
	p.ProfileHeader.ProfileRevision = "1.00"
	p.ProfileHeader.ProfileName = "Device Profile for PROFINET Devices"
	p.ProfileHeader.ProfileSource = "PROFIBUS Nutzerorganisation e. V. (PNO)"
	p.ProfileHeader.ProfileClassID = "Device"
	p.ProfileHeader.ISO15745Reference.ISO15745Part = 4
	p.ProfileHeader.ISO15745Reference.ISO15745Edition = 1
	p.ProfileHeader.ISO15745Reference.ProfileTechnology = "GSDML"
	p.ProfileBody.DeviceIdentity = gsdmlDeviceIdentity{
		VendorID: fmt.Sprintf("0x%04X", d.VendorID), DeviceID: fmt.Sprintf("0x%04X", d.DeviceID),
		InfoText: &gsdmlTextRef{TextId: "IDT_INFO_Device"}, VendorName: &gsdmlValue{Value: d.VendorName},
	}
	p.ProfileBody.DeviceFunction.Family = gsdmlFamily{MainFamily: "I/O", ProductFamily: "Gateway"}
	dap := gsdmlDAPItem{
		ID: "IDD_1", PNIO_Version: "V2.4", PhysicalSlots: fmt.Sprintf("0..%d", maxSlot),
		ModuleIdentNumber: fmt.Sprintf("0x%08X", device.DAPModuleIdent), MinDeviceInterval: fmt.Sprintf("%d", minInterval),
		DNS_CompatibleName: d.Station, FixedInSlots: "0", ObjectUUID_LocalIndex: "1",
		DeviceAccessSupported: "true", NumberOfDeviceAccessAR: "1", MultipleWriteSupported: "true",
		CheckDeviceID_Allowed: "false", NameOfStationNotTransferable: "false", LLDP_NoD_Supported: "true",
		ResetToFactoryModes: "2",
		ModuleInfo: &gsdmlModuleInfo{
			Name: &gsdmlTextRef{TextId: "IDT_NAME_DAP"}, InfoText: &gsdmlTextRef{TextId: "IDT_INFO_DAP"},
			VendorName: &gsdmlValue{Value: d.VendorName}, OrderNumber: &gsdmlValue{Value: d.OrderNumber},
		},
		CertificationInfo: &gsdmlCertificationInfo{ConformanceClass: "B", NetloadClass: "III"},
		IOConfigData:      &gsdmlIOConfigData{MaxInputLength: "244", MaxOutputLength: "244"},
		UseableModules:    &gsdmlUseableModules{ModuleItemRef: refs},
		VirtualSubmoduleList: &gsdmlVirtualSubmoduleList{VirtualSubmoduleItem: []gsdmlVirtualSubmoduleItem{{
			ID: "IDS_DAP", SubmoduleIdentNumber: fmt.Sprintf("0x%08X", device.DAPSubmoduleIdent), MayIssueProcessAlarm: "false",
			IOData:     &gsdmlIOData{},
			ModuleInfo: &gsdmlModuleInfo{Name: &gsdmlTextRef{TextId: "IDT_NAME_DAP"}, InfoText: &gsdmlTextRef{TextId: "IDT_INFO_DAP"}},
		}}},
		SystemDefinedSubmoduleList: &gsdmlSystemSubmoduleList{
			InterfaceSubmoduleItem: []gsdmlInterfaceSubmodule{{
				ID: "IDS_I", SubmoduleIdentNumber: fmt.Sprintf("0x%08X", device.InterfaceSubmodIdent),
				SubslotNumber: fmt.Sprintf("%d", device.InterfaceSubslot), TextId: "IDT_NAME_Interface",
				SupportedRT_Classes: "RT_CLASS_1", SupportedProtocols: "LLDP;SNMP",
				PTP_BoundarySupported: "true", DCP_BoundarySupported: "true",
				ApplicationRelations: &gsdmlApplicationRelations{
					StartupMode:      "Legacy;Advanced",
					TimingProperties: gsdmlTimingProperties{SendClock: "32", ReductionRatio: "1 2 4 8 16 32 64 128 256 512"},
				},
			}},
			PortSubmoduleItem: []gsdmlPortSubmodule{{
				ID: "IDS_P1", SubmoduleIdentNumber: fmt.Sprintf("0x%08X", device.PortSubmodIdent),
				SubslotNumber: fmt.Sprintf("%d", device.PortSubslot), TextId: "IDT_NAME_Port1",
				MaxPortRxDelay: "350", MaxPortTxDelay: "160",
				MAUTypeList: &gsdmlMAUTypeList{MAUTypeItem: []gsdmlMAUTypeItem{{Value: "16"}}},
			}},
		},
	}
	p.ProfileBody.ApplicationProcess.DeviceAccessPointList.DeviceAccessPointItem = []gsdmlDAPItem{dap}
	p.ProfileBody.ApplicationProcess.ModuleList = &gsdmlModuleList{ModuleItem: items}
	p.ProfileBody.ApplicationProcess.ExternalTextList.PrimaryLanguage.Text = texts
	return p
}

// ioData declares a subslot's data as one octet string each way; the tag
// layout inside it is the manifest's, not the GSDML's.
func ioData(s device.Subslot, addText func(string, string)) *gsdmlIOData {
	io := &gsdmlIOData{}
	if s.InputLen > 0 {
		id := fmt.Sprintf("IDT_IO_Slot%d_Sub%d_In", s.Slot, s.Subslot)
		addText(id, fmt.Sprintf("Input data slot %d subslot %d", s.Slot, s.Subslot))
		io.Input = &gsdmlIODataDir{DataItem: []gsdmlDataItem{{DataType: "OctetString", Length: fmt.Sprintf("%d", s.InputLen), TextId: id}}}
	}
	if s.OutputLen > 0 {
		id := fmt.Sprintf("IDT_IO_Slot%d_Sub%d_Out", s.Slot, s.Subslot)
		addText(id, fmt.Sprintf("Output data slot %d subslot %d", s.Slot, s.Subslot))
		io.Output = &gsdmlIODataDir{DataItem: []gsdmlDataItem{{DataType: "OctetString", Length: fmt.Sprintf("%d", s.OutputLen), TextId: id}}}
	}
	return io
}
