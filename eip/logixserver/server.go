package logixserver

import (
	"context"
	"encoding/binary"
	"log/slog"
	"net"
	"strings"
	"sync/atomic"

	"github.com/joyautomation/nautilus/eip/cip"
)

// fragLimit is the largest value payload returned in a single Read Tag reply.
// Larger values get CIP status 0x06 (partial transfer), steering clients to
// Read Tag Fragmented — the same behavior a real controller shows on a
// 500-byte Class 3 connection.
const fragLimit = 480

// Server presents a TagStore + Schema as an Allen-Bradley ControlLogix
// target. It accepts the connected (Class 3) Message Router connection Logix
// clients open via Forward_Open and answers symbolic Read/Write Tag, batched
// Multiple Service Packet, tag-list upload, and Template requests.
type Server struct {
	store          *TagStore
	schema         *Schema
	controllerName string
	log            *slog.Logger
	addr           string

	srv      *cip.Server
	nextConn uint32

	// DenyStructRoots makes whole-struct reads fail with privilege violation
	// (0x0F), the way real controllers refuse AOI backing tags — members must
	// then be read individually. For driver leaf-mode tests.
	DenyStructRoots bool
}

// NewServer wires a tag store and tag/UDT schema into a fresh CIP server
// bound to addr. controllerName is reported by the Program Name object.
func NewServer(store *TagStore, schema *Schema, controllerName, addr string, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	if addr == "" {
		addr = ":44818"
	}
	return &Server{
		store:          store,
		schema:         schema,
		controllerName: controllerName,
		log:            log,
		addr:           addr,
		nextConn:       0xC1000001,
	}
}

// Run starts the CIP server and blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	srv := cip.NewServer(s.addr, s.log)
	s.srv = srv

	srv.Dispatcher.Register(cip.ClassIdentity, NewIdentityObject())
	srv.Dispatcher.Register(cip.ClassProgramName, NewProgramNameObject(s.controllerName))
	srv.Dispatcher.Register(cip.ClassTemplate, NewTemplateObject(s.schema))
	srv.ListIdentityPayload = func(local net.IP, port uint16) []byte {
		return ListIdentityPayload(local, port)
	}
	srv.HandleForwardOpen = s.handleForwardOpen
	srv.HandleConnectedMessage = s.handleConnected

	return srv.Run(ctx)
}

// Stop tears the server down.
func (s *Server) Stop() {
	if s.srv != nil {
		s.srv.Stop()
	}
}

// handleForwardOpen accepts the Message Router (class 0x02) connection a
// client opens for connected explicit messaging. We don't validate the
// connection path — the server is the endpoint — and simply allocate a
// connection-ID pair echoing the originator's requested RPI.
func (s *Server) handleForwardOpen(req cip.ForwardOpenRequest) (cip.ForwardOpenResponse, bool) {
	tToO := atomic.AddUint32(&s.nextConn, 1)
	oToT := req.OToTNetworkConnID
	if oToT == 0 {
		oToT = atomic.AddUint32(&s.nextConn, 1)
	}
	return cip.ForwardOpenResponse{
		OToTNetworkConnID:   oToT,
		TToONetworkConnID:   tToO,
		ConnectionSerial:    req.ConnectionSerial,
		OriginatorVendorID:  req.OriginatorVendorID,
		OriginatorSerialNum: req.OriginatorSerialNum,
		OToTAPIMicros:       req.OToTRPIMicros,
		TToOAPIMicros:       req.TToORPIMicros,
	}, true
}

// handleConnected routes a connected explicit message. A Multiple Service
// Packet (0x0A) is unwrapped and each embedded sub-request dispatched
// individually — the client's batched-read hot path.
func (s *Server) handleConnected(_ uint32, _ uint16, mr cip.MessageRouterRequest) cip.MessageRouterResponse {
	if mr.Service == cip.ServiceMultipleServicePkt {
		return s.multipleService(mr)
	}
	return s.dispatchService(mr)
}

// dispatchService handles a single Message Router request. Symbolic Read/
// Write Tag has no class segment so it cannot go through the class
// dispatcher; the tag-list upload (Get_Instance_Attribute_List) carries an
// optional Program data segment the dispatcher's logical-only parser rejects;
// everything else (Identity, Program Name, Template GetAttributeList, etc.)
// falls back to the dispatcher.
func (s *Server) dispatchService(mr cip.MessageRouterRequest) cip.MessageRouterResponse {
	switch mr.Service {
	case cip.ServiceReadTag, cip.ServiceReadTagFragmented:
		return s.readTag(mr)
	case cip.ServiceWriteTag, cip.ServiceWriteTagFragmented:
		return s.writeTag(mr)
	case cip.ServiceGetInstanceAttributeList:
		return s.getInstanceAttributeList(mr)
	default:
		return s.srv.Dispatcher.Dispatch(mr)
	}
}

// multipleService unwraps a Multiple Service Packet (0x0A) and dispatches each
// embedded sub-request through dispatchService, then re-wraps the responses.
// The outer status is success unless a sub-request failed, in which case 0x1E
// (embedded service error) flags it — each sub-response still carries its own
// status.
func (s *Server) multipleService(mr cip.MessageRouterRequest) cip.MessageRouterResponse {
	reqs, err := cip.DecodeMultipleServiceRequest(mr.Data)
	if err != nil {
		return cip.MRError(mr.Service, cip.StatusPathSegmentError)
	}
	resps := make([]cip.MessageRouterResponse, len(reqs))
	anyFailed := false
	for i, sub := range reqs {
		resps[i] = s.dispatchService(sub)
		if resps[i].GeneralStatus != cip.StatusSuccess {
			anyFailed = true
		}
	}
	status := cip.StatusSuccess
	if anyFailed {
		status = cip.StatusServiceError
	}
	return cip.MessageRouterResponse{
		Service:       mr.Service,
		GeneralStatus: status,
		Data:          cip.EncodeMultipleServiceResponse(resps),
	}
}

// readTag serves Read Tag (0x4C) / Read Tag Fragmented (0x52). The path
// determines the form: leading ANSI symbol (0x91) is a symbolic read; leading
// Template class (0x6C) is a Read Template; leading Symbol class (0x6B) is
// Symbol-Instance-Addressing.
func (s *Server) readTag(mr cip.MessageRouterRequest) cip.MessageRouterResponse {
	if len(mr.Path) == 0 {
		return cip.MRError(mr.Service, cip.StatusPathSegmentError)
	}
	if mr.Path[0] == cip.AnsiExtendedSymbol {
		segs, err := cip.ParseSymbolicTag(mr.Path)
		if err != nil {
			return cip.MRError(mr.Service, cip.StatusPathSegmentError)
		}
		return s.resolveAndReply(mr, cip.CanonicalTagPath(segs))
	}

	prefix, consumed := cip.ParseLogicalPrefix(mr.Path)
	switch {
	case prefix.HasClass && uint16(prefix.Class) == cip.ClassTemplate:
		return s.srv.Dispatcher.Dispatch(mr)
	case prefix.HasClass && uint16(prefix.Class) == cip.ClassSymbol && prefix.HasInst:
		return s.readByInstance(mr, prefix.Instance, mr.Path[consumed:])
	default:
		return cip.MRError(mr.Service, cip.StatusPathSegmentError)
	}
}

// readByInstance resolves a Symbol-Instance-Addressing read: the base tag is
// found by its controller-scope Symbol instance id, then any trailing symbolic
// member/index segments are appended to form the canonical store path.
func (s *Server) readByInstance(mr cip.MessageRouterRequest, instance uint32, rest []byte) cip.MessageRouterResponse {
	base, ok := s.schema.instanceBase[instance]
	if !ok {
		return cip.MRError(mr.Service, cip.StatusPathDestUnknown)
	}
	path := base
	if len(rest) > 0 {
		prefix := []byte{cip.AnsiExtendedSymbol, byte(len(base))}
		prefix = append(prefix, []byte(base)...)
		if len(base)%2 == 1 {
			prefix = append(prefix, 0) // word-alignment pad
		}
		segs, err := cip.ParseSymbolicTag(append(prefix, rest...))
		if err != nil {
			return cip.MRError(mr.Service, cip.StatusPathSegmentError)
		}
		path = cip.CanonicalTagPath(segs)
	}
	return s.resolveAndReply(mr, path)
}

// resolveAndReply serves a canonical path: an atomic leaf straight from the
// store, or a struct root/element assembled from its template plus the leaf
// values beneath it. Values larger than fragLimit follow the partial-transfer
// protocol.
func (s *Server) resolveAndReply(mr cip.MessageRouterRequest, path string) cip.MessageRouterResponse {
	// Atomic leaf.
	if leafType, value, ok := s.store.Resolve(path); ok {
		out := make([]byte, 0, 12)
		out = append(out, u16(leafType)...)
		out = append(out, EncodeLeaf(leafType, value)...)
		return cip.MROK(mr.Service, out)
	}

	// Struct root (or array of structs / single indexed element).
	header, raw, ok := s.assembleStruct(path)
	if !ok {
		return cip.MRError(mr.Service, cip.StatusPathDestUnknown)
	}
	if s.DenyStructRoots {
		return cip.MRError(mr.Service, cip.StatusPrivilegeViolation)
	}

	if mr.Service == cip.ServiceReadTagFragmented {
		offset := 0
		if len(mr.Data) >= 6 {
			offset = int(binary.LittleEndian.Uint32(mr.Data[2:6]))
		}
		if offset < 0 || offset > len(raw) {
			return cip.MRError(mr.Service, cip.StatusInvalidParameter)
		}
		chunk := raw[offset:]
		status := cip.StatusSuccess
		if len(chunk) > fragLimit {
			chunk = chunk[:fragLimit]
			status = cip.StatusPartialTransfer
		}
		return cip.MessageRouterResponse{
			Service:       mr.Service,
			GeneralStatus: status,
			Data:          append(append([]byte{}, header...), chunk...),
		}
	}

	if len(raw) > fragLimit {
		// Plain read of a large value: partial transfer with the first chunk,
		// steering the client to Read Tag Fragmented.
		return cip.MessageRouterResponse{
			Service:       mr.Service,
			GeneralStatus: cip.StatusPartialTransfer,
			Data:          append(append([]byte{}, header...), raw[:fragLimit]...),
		}
	}
	return cip.MROK(mr.Service, append(append([]byte{}, header...), raw...))
}

// assembleStruct builds the wire image for a struct-valued path from the
// schema and leaf store. Returns the reply header (0x02A0 + structure handle)
// and the raw instance bytes. Supports "Root", "Root[i]", and whole arrays of
// structs (concatenated instances).
func (s *Server) assembleStruct(path string) (header []byte, raw []byte, ok bool) {
	base, index, hasIndex := splitTrailingIndex(path)
	sym, found := s.schema.lookupSymbol(base)
	if !found {
		return nil, nil, false
	}
	tmpl, isStruct := s.schema.templateFor(sym)
	if !isStruct {
		return nil, nil, false
	}
	header = append(u16(0x02A0), u16(tmpl.handle)...)

	dims := int(sym.dims[0])
	switch {
	case hasIndex:
		raw = s.encodeStructInstance(base+"["+index+"]", tmpl)
	case dims > 0:
		for i := 0; i < dims; i++ {
			raw = append(raw, s.encodeStructInstance(base+"["+itoa(i)+"]", tmpl)...)
		}
	default:
		raw = s.encodeStructInstance(base, tmpl)
	}
	return header, raw, true
}

// encodeStructInstance walks the template, filling member offsets from the
// leaf store at dotted paths beneath prefix. Missing leaves stay zero.
func (s *Server) encodeStructInstance(prefix string, tmpl *templateDef) []byte {
	buf := make([]byte, tmpl.structSize)
	for _, m := range tmpl.members {
		if m.typeCode&symbolTypeStructBit != 0 {
			child, ok := s.schema.templates[uint32(m.typeCode&symbolTypeTemplateMask)]
			if !ok {
				continue
			}
			copy(buf[m.offset:], s.encodeStructInstance(prefix+"."+m.name, child))
			continue
		}
		if _, v, ok := s.store.Resolve(prefix + "." + m.name); ok {
			copy(buf[m.offset:], EncodeLeaf(m.typeCode, v))
		}
	}
	return buf
}

// writeTag serves Write Tag (0x4D): data is [u16 type][u16 count][value
// bytes]. Only elementary leaf writes are supported — matching what real
// controllers accept without a structure handle.
func (s *Server) writeTag(mr cip.MessageRouterRequest) cip.MessageRouterResponse {
	if len(mr.Path) == 0 || mr.Path[0] != cip.AnsiExtendedSymbol {
		return cip.MRError(mr.Service, cip.StatusPathSegmentError)
	}
	segs, err := cip.ParseSymbolicTag(mr.Path)
	if err != nil {
		return cip.MRError(mr.Service, cip.StatusPathSegmentError)
	}
	path := cip.CanonicalTagPath(segs)
	if len(mr.Data) < 4 {
		return cip.MRError(mr.Service, cip.StatusNotEnoughData)
	}
	typeCode := binary.LittleEndian.Uint16(mr.Data[0:2])
	leafType, _, ok := s.store.Resolve(path)
	if !ok {
		return cip.MRError(mr.Service, cip.StatusPathDestUnknown)
	}
	if leafType != typeCode {
		return cip.MRError(mr.Service, cip.StatusInvalidParameter)
	}
	v, ok := DecodeLeaf(typeCode, mr.Data[4:])
	if !ok {
		return cip.MRError(mr.Service, cip.StatusNotEnoughData)
	}
	s.store.UpdateValue(path, v)
	return cip.MROK(mr.Service, nil)
}

// getInstanceAttributeList answers the tag-list-upload walk over the Symbol
// class (0x6B). The path is an optional ANSI Program data segment (selecting
// program scope) followed by a logical class + start-instance. We reply with
// every symbol in that scope whose instance id is >= the start instance, with
// success status so the client's walk terminates after one round.
func (s *Server) getInstanceAttributeList(mr cip.MessageRouterRequest) cip.MessageRouterResponse {
	scope := ""
	rest := mr.Path
	if len(rest) > 0 && rest[0] == cip.AnsiExtendedSymbol {
		if len(rest) < 2 {
			return cip.MRError(mr.Service, cip.StatusPathSegmentError)
		}
		n := int(rest[1])
		end := 2 + n
		if end > len(rest) {
			return cip.MRError(mr.Service, cip.StatusPathSegmentError)
		}
		scope = string(rest[2:end])
		if n%2 == 1 {
			end++ // word-alignment pad byte
		}
		rest = rest[end:]
	}

	prefix, _ := cip.ParseLogicalPrefix(rest)
	if !prefix.HasClass || uint16(prefix.Class) != cip.ClassSymbol {
		return cip.MRError(mr.Service, cip.StatusPathDestUnknown)
	}
	body := s.schema.encodeInstanceList(scope, prefix.Instance)
	return cip.MROK(mr.Service, body)
}

// splitTrailingIndex splits "Name[4]" into ("Name", "4", true). Paths without
// a trailing index return unchanged with hasIndex=false.
func splitTrailingIndex(path string) (base, index string, hasIndex bool) {
	if !strings.HasSuffix(path, "]") {
		return path, "", false
	}
	i := strings.LastIndexByte(path, '[')
	if i < 0 {
		return path, "", false
	}
	return path[:i], path[i+1 : len(path)-1], true
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
