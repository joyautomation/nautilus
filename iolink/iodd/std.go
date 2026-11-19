package iodd

// standardVariables are the variables an IODD names by StdVariableRef,
// with their ISDU index, access and type, from the IO-Link Community's
// IODD-StandardDefinitions1.1.xml (v1.1.3). The file itself is not
// vendored (its terms are the Community's); these are the facts a parser
// needs. IODD 1.0.1's older ids are listed beside 1.1's.
//
// String lengths are the identification strings' maximum, so a type's
// FixedLength here bounds what ISDU may return rather than fixing it.
var standardVariables = map[string]struct {
	index  int
	access string
	name   string
	typ    Type
}{
	"V_DirectParameters_1":      {0, "ro", "Direct Parameters 1", Type{Kind: Unsupported, Name: "RecordT"}},
	"V_DirectParameters_2":      {1, "rw", "Direct Parameters 2", Type{Kind: Unsupported, Name: "RecordT"}},
	"V_SystemCommand":           {2, "wo", "System Command", uint8T},
	"V_DeviceAccessLocks":       {12, "rw", "Device Access Locks", Type{Kind: Unsupported, Name: "RecordT"}},
	"V_VendorName":              {16, "ro", "Vendor Name", stringT(64)},
	"V_VendorText":              {17, "ro", "Vendor Text", stringT(64)},
	"V_ProductName":             {18, "ro", "Product Name", stringT(64)},
	"V_ProductID":               {19, "ro", "Product ID", stringT(64)},
	"V_ProductText":             {20, "ro", "Product Text", stringT(64)},
	"V_SerialNumber":            {21, "ro", "Serial Number", stringT(16)},
	"V_HardwareRevision":        {22, "ro", "Hardware Revision", stringT(64)},
	"V_FirmwareRevision":        {23, "ro", "Firmware Revision", stringT(64)},
	"V_ApplicationSpecificTag":  {24, "rw", "Application Specific Tag", stringT(32)},
	"V_ApplicationSpecificName": {24, "rw", "Application Specific Tag", stringT(32)}, // 1.0.1
	"V_ErrorCount":              {32, "ro", "Error Count", Type{Kind: UInteger, Name: "UIntegerT", BitLength: 16}},
	"V_DeviceStatus":            {36, "ro", "Device Status", uint8T},
	"V_DetailedDeviceStatus":    {37, "ro", "Detailed Device Status", Type{Kind: Unsupported, Name: "ArrayT"}},
	"V_ProcessDataInput":        {40, "ro", "Process Data Input", Type{Kind: Unsupported, Name: "ProcessData"}},
	"V_ProcessDataIn":           {40, "ro", "Process Data Input", Type{Kind: Unsupported, Name: "ProcessData"}}, // 1.0.1
	"V_ProcessDataOutput":       {41, "ro", "Process Data Output", Type{Kind: Unsupported, Name: "ProcessData"}},
	"V_ProcessDataOut":          {41, "ro", "Process Data Output", Type{Kind: Unsupported, Name: "ProcessData"}}, // 1.0.1
	"V_OffsetTime":              {48, "rw", "Offset Time", Type{Kind: Unsupported, Name: "RecordT"}},
}

var uint8T = Type{Kind: UInteger, Name: "UIntegerT", BitLength: 8}

func stringT(n int) Type {
	return Type{Kind: String, Name: "StringT", FixedLength: n, BitLength: 8 * n, Encoding: "UTF-8"} // UTF-8 reads ASCII too
}
