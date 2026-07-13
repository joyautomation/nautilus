package cip

// CIP service codes (Vol 1, App. A). Bit 7 set in the reply indicates the
// service was a response (0x80 | request_service).
const (
	ServiceGetAttributesAll   uint8 = 0x01
	ServiceSetAttributesAll   uint8 = 0x02
	ServiceGetAttributeList   uint8 = 0x03
	ServiceSetAttributeList   uint8 = 0x04
	ServiceReset              uint8 = 0x05
	ServiceStart              uint8 = 0x06
	ServiceStop               uint8 = 0x07
	ServiceCreate             uint8 = 0x08
	ServiceDelete             uint8 = 0x09
	ServiceMultipleServicePkt uint8 = 0x0A
	ServiceApplyAttributes    uint8 = 0x0D
	ServiceGetAttributeSingle uint8 = 0x0E
	ServiceSetAttributeSingle uint8 = 0x10
	ServiceFindNextObjectInst uint8 = 0x11
	ServiceRestore            uint8 = 0x15
	ServiceSave               uint8 = 0x16
	ServiceNOP                uint8 = 0x17
	ServiceGetMember          uint8 = 0x18
	ServiceSetMember          uint8 = 0x19
	ServiceInsertMember       uint8 = 0x1A
	ServiceRemoveMember       uint8 = 0x1B
	ServiceGroupSync          uint8 = 0x1C

	// Logix tag-server services (vendor-specific, but widely used).
	ServiceReadTag            uint8 = 0x4C
	ServiceReadTagFragmented  uint8 = 0x52
	ServiceWriteTag           uint8 = 0x4D
	ServiceWriteTagFragmented uint8 = 0x53

	// ServiceGetInstanceAttributeList (0x55) enumerates instances of a class
	// returning a chosen attribute set per instance. pycomm3 walks the Symbol
	// class (0x6B) with it to upload a controller's tag list.
	ServiceGetInstanceAttributeList uint8 = 0x55

	// CIP File Object services (Vol 1 §5A-3). Service codes are class-scoped,
	// so overlap with Logix tag services (0x4C/0x4D) is fine at the wire level
	// — the dispatcher routes by class first.
	ServiceInitiateUpload uint8 = 0x4B
	ServiceUploadTransfer uint8 = 0x4F

	// Connection Manager services.
	ServiceForwardOpen       uint8 = 0x54
	ServiceLargeForwardOpen  uint8 = 0x5B
	ServiceForwardClose      uint8 = 0x4E
	ServiceUnconnectedSend   uint8 = 0x52 // Note: same value as ReadTagFragmented; context disambiguates
	ServiceGetConnectionData uint8 = 0x56

	// Reply bit
	ReplyBit uint8 = 0x80
)

// Standard CIP object class IDs (Vol 1, App. A).
const (
	ClassIdentity           uint16 = 0x0001
	ClassMessageRouter      uint16 = 0x0002
	ClassDeviceNet          uint16 = 0x0003
	ClassAssembly           uint16 = 0x0004
	ClassConnection         uint16 = 0x0005
	ClassConnectionManager  uint16 = 0x0006
	ClassRegister           uint16 = 0x0007
	ClassDiscreteInput      uint16 = 0x0008
	ClassDiscreteOutput     uint16 = 0x0009
	ClassAnalogInput        uint16 = 0x000A
	ClassAnalogOutput       uint16 = 0x000B
	ClassPresenceSensing    uint16 = 0x000E
	ClassParameter          uint16 = 0x000F
	ClassParameterGroup     uint16 = 0x0010
	ClassGroup              uint16 = 0x0012
	ClassDiscreteInputGrp   uint16 = 0x001D
	ClassDiscreteOutputGrp  uint16 = 0x001E
	ClassDiscreteGroup      uint16 = 0x001F
	ClassAnalogInputGroup   uint16 = 0x0020
	ClassAnalogOutputGroup  uint16 = 0x0021
	ClassAnalogGroup        uint16 = 0x0022
	ClassPositionSensor     uint16 = 0x0023
	ClassPositionController uint16 = 0x0024
	ClassPositionControlSup uint16 = 0x0025
	ClassBlockSequencer     uint16 = 0x0026
	ClassCommandBlock       uint16 = 0x0027
	ClassMotorData          uint16 = 0x0028
	ClassControlSupervisor  uint16 = 0x0029
	ClassACDCDrive          uint16 = 0x002A
	ClassAcknowledgeHandler uint16 = 0x002B
	ClassOverloadProtection uint16 = 0x002C
	ClassSoftStart          uint16 = 0x002D
	ClassSelection          uint16 = 0x002E
	ClassProgramName        uint16 = 0x0064 // Logix controller/program name (Rockwell KB 23341)
	ClassSymbol             uint16 = 0x006B // Logix tag database
	ClassTemplate           uint16 = 0x006C // Logix UDT definitions
	ClassFile               uint16 = 0x0037
	ClassTCPIP              uint16 = 0x00F5
	ClassEthernetLink       uint16 = 0x00F6
)

// CIP elementary data-type codes (Vol 1, App. C, Table C-6.1). These are the
// 2-byte type identifiers that lead a Logix Read Tag reply so a client can
// decode the value that follows. PowerFlex's parameter object uses the bare
// 0xCx literals; these named constants cover the Logix tag-server path.
const (
	TypeBOOL  uint16 = 0x00C1
	TypeSINT  uint16 = 0x00C2
	TypeINT   uint16 = 0x00C3
	TypeDINT  uint16 = 0x00C4
	TypeLINT  uint16 = 0x00C5
	TypeUSINT uint16 = 0x00C6
	TypeUINT  uint16 = 0x00C7
	TypeUDINT uint16 = 0x00C8
	TypeULINT uint16 = 0x00C9
	TypeREAL  uint16 = 0x00CA
	TypeLREAL uint16 = 0x00CB
	TypeWORD  uint16 = 0x00D2
	TypeDWORD uint16 = 0x00D3
	TypeLWORD uint16 = 0x00D4
	// TypeStruct leads a structured (UDT) value; the two bytes after it are the
	// template/struct handle. Not used until M1, included for completeness.
	TypeStruct uint16 = 0x02A0
)

// CIP general status codes (Vol 1, App. B).
const (
	StatusSuccess             uint8 = 0x00
	StatusConnectionFailure   uint8 = 0x01
	StatusResourceUnavail     uint8 = 0x02
	StatusInvalidParameter    uint8 = 0x03 // Vol1: "Invalid parameter value"
	StatusPathSegmentError    uint8 = 0x04
	StatusPathDestUnknown     uint8 = 0x05
	StatusPartialTransfer     uint8 = 0x06
	StatusConnLost            uint8 = 0x07
	StatusServiceNotSupported uint8 = 0x08
	StatusInvalidAttrValue    uint8 = 0x09
	StatusAttrListError       uint8 = 0x0A
	StatusAlreadyInState      uint8 = 0x0B
	StatusObjectStateConflict uint8 = 0x0C
	StatusObjectExists        uint8 = 0x0D
	StatusAttrNotSettable     uint8 = 0x0E
	StatusPrivilegeViolation  uint8 = 0x0F
	StatusDeviceStateConflict uint8 = 0x10
	StatusReplyTooLarge       uint8 = 0x11
	StatusFragPrimitive       uint8 = 0x12
	StatusNotEnoughData       uint8 = 0x13
	StatusAttrNotSupported    uint8 = 0x14
	StatusTooMuchData         uint8 = 0x15
	StatusObjDoesNotExist     uint8 = 0x16
	StatusServiceNotImpl      uint8 = 0x17
	StatusNoStoredAttrData    uint8 = 0x18
	StatusStoreFailure        uint8 = 0x19
	StatusRoutingFailure      uint8 = 0x1A
	StatusRoutingFailureLong  uint8 = 0x1B
	StatusMissingListData     uint8 = 0x1C
	StatusInvalidListStatus   uint8 = 0x1D
	StatusServiceError        uint8 = 0x1E
	StatusEmbeddedFailure     uint8 = 0x1F
	StatusVendorSpecific      uint8 = 0x1F
	StatusInvalidParam        uint8 = 0x20
	StatusWriteOnceFailure    uint8 = 0x21
	StatusInvalidReply        uint8 = 0x22
	StatusBufferOverflow      uint8 = 0x23
	StatusInvalidMsgFormat    uint8 = 0x24
	StatusKeyFailure          uint8 = 0x25
	StatusPathSizeInvalid     uint8 = 0x26
	StatusUnexpectedAttr      uint8 = 0x27
	StatusInvalidMember       uint8 = 0x28
	StatusMemberNotSettable   uint8 = 0x29
)
