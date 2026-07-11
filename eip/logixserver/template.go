package logixserver

import (
	"encoding/binary"

	"github.com/joyautomation/nautilus/eip/cip"
)

// TemplateObject implements CIP class 0x6C (Template), the Logix UDT-definition
// surface clients read during tag-list upload to learn struct members. The
// instance id selects which UDT; it is the template id encoded in a struct
// symbol's type (Symbol attribute 2, low 12 bits).
type TemplateObject struct {
	schema *Schema
}

// NewTemplateObject returns a Template handler backed by the schema.
func NewTemplateObject(schema *Schema) *TemplateObject { return &TemplateObject{schema: schema} }

// HandleService answers the two services clients use on the Template class:
// GetAttributeList (0x03) for the structure makeup, and ReadTag (0x4C) for the
// template body. The Read Tag interception in the server forwards class-0x6C
// reads here.
func (o *TemplateObject) HandleService(service uint8, instance uint32, _ uint32, data []byte) cip.MessageRouterResponse {
	tmpl, ok := o.schema.templates[instance]
	if !ok {
		return cip.MRError(service, cip.StatusObjDoesNotExist)
	}
	switch service {
	case cip.ServiceGetAttributeList:
		return cip.MROK(service, tmpl.attributesReply())
	case cip.ServiceReadTag:
		return o.readTemplate(service, tmpl, data)
	default:
		return cip.MRError(service, cip.StatusServiceNotSupported)
	}
}

// readTemplate serves the Read Template request: request data is [DINT offset]
// [UINT size]. We return the slice from offset to the end (our templates fit a
// single packet) with success status so the client stops after one read.
func (o *TemplateObject) readTemplate(service uint8, tmpl *templateDef, data []byte) cip.MessageRouterResponse {
	raw := tmpl.rawData()
	offset := 0
	if len(data) >= 4 {
		offset = int(int32(binary.LittleEndian.Uint32(data[:4])))
	}
	if offset < 0 || offset > len(raw) {
		return cip.MRError(service, cip.StatusInvalidParameter)
	}
	return cip.MROK(service, raw[offset:])
}
