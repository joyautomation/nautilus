package logixserver

import (
	"context"
	"encoding/binary"
	"log/slog"
	"net"
	"strings"
	"sync"
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
	srvOnce  sync.Once
	nextConn uint32

	// DenyStructRoots makes whole-struct reads fail with privilege violation
	// (0x0F), the way real controllers refuse AOI backing tags — members must
	// then be read individually. For driver leaf-mode tests.
	DenyStructRoots bool

	// failMu guards failing: canonical paths whose reads answer with a
	// transient error (FailReads), for driver read-quality tests.
	failMu  sync.Mutex
	failing map[string]bool

	// writeLog records the canonical path of every Write Tag request the
	// server received, for tests that count what reached the controller.
	writeMu  sync.Mutex
	writeLog []string

	// frags holds a fragmented write's bytes until the last chunk lands.
	fragMu sync.Mutex
	frags  map[string][]byte
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

// build wires the CIP server once; Listen and Run share it.
func (s *Server) build() *cip.Server {
	s.srvOnce.Do(func() {
		srv := cip.NewServer(s.addr, s.log)
		srv.Dispatcher.Register(cip.ClassIdentity, NewIdentityObject())
		srv.Dispatcher.Register(cip.ClassProgramName, NewProgramNameObject(s.controllerName))
		srv.Dispatcher.Register(cip.ClassTemplate, NewTemplateObject(s.schema))
		srv.ListIdentityPayload = func(local net.IP, port uint16) []byte {
			return ListIdentityPayload(local, port)
		}
		srv.HandleForwardOpen = s.handleForwardOpen
		srv.HandleConnectedMessage = s.handleConnected
		s.srv = srv
	})
	return s.srv
}

// Listen binds the TCP and UDP sockets now, so the caller knows the address
// (and that nothing else took it) before Run serves. An addr of host:0 gets
// a real port, reported by Addr. Run calls Listen itself if you did not.
func (s *Server) Listen() error { return s.build().Listen() }

// Addr is the bound listen address after Listen, or "" before.
func (s *Server) Addr() string { return s.build().BoundAddr() }

// Run starts the CIP server and blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error { return s.build().Run(ctx) }

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

// Writes returns the canonical path of every Write Tag request received so
// far, in arrival order (a struct written member by member is one per member).
func (s *Server) Writes() []string {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return append([]string(nil), s.writeLog...)
}

// FailReads makes reads of path (canonical, e.g. "Speed") answer with a
// transient error (object state conflict, 0x0C) while on is true; the
// session stays up. For driver read-quality tests.
func (s *Server) FailReads(path string, on bool) {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	if s.failing == nil {
		s.failing = map[string]bool{}
	}
	s.failing[strings.ToUpper(path)] = on
}

func (s *Server) readFails(path string) bool {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	return s.failing[strings.ToUpper(path)]
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
	if s.readFails(path) {
		return cip.MRError(mr.Service, cip.StatusObjectStateConflict)
	}
	// Atomic leaf.
	if leafType, value, ok := s.store.Resolve(path); ok {
		out := make([]byte, 0, 12)
		out = append(out, u16(leafType)...)
		out = append(out, EncodeLeaf(leafType, value)...)
		return cip.MROK(mr.Service, out)
	}

	// Struct root (or array of structs / single indexed element), else a
	// whole array of an elementary type.
	header, raw, ok := s.assembleStruct(path)
	if ok {
		if s.DenyStructRoots {
			return cip.MRError(mr.Service, cip.StatusPrivilegeViolation)
		}
	} else if header, raw, ok = s.assembleElementaryArray(path); !ok {
		return cip.MRError(mr.Service, cip.StatusPathDestUnknown)
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
// leaf store at dotted paths beneath prefix. Missing leaves stay zero. Array
// members read their elements at "<member>[i]"; a BOOL array is bit-packed.
func (s *Server) encodeStructInstance(prefix string, tmpl *templateDef) []byte {
	buf := make([]byte, tmpl.structSize)
	for _, m := range tmpl.members {
		path := prefix + "." + m.name
		isArray := m.typeCode&memberArrayBit != 0
		count := 1
		if isArray {
			count = int(m.typeInfo)
		}
		if m.typeCode&symbolTypeStructBit != 0 {
			child, ok := s.schema.templates[uint32(m.typeCode&symbolTypeTemplateMask)]
			if !ok {
				continue
			}
			if !isArray {
				copy(buf[m.offset:], s.encodeStructInstance(path, child))
				continue
			}
			for i := 0; i < count; i++ {
				off := int(m.offset) + i*int(child.structSize)
				if off < len(buf) {
					copy(buf[off:], s.encodeStructInstance(path+"["+itoa(i)+"]", child))
				}
			}
			continue
		}
		code := m.typeCode &^ memberArrayBit
		if !isArray {
			if _, v, ok := s.store.Resolve(path); ok {
				copy(buf[m.offset:], EncodeLeaf(code, v))
			}
			continue
		}
		for i := 0; i < count; i++ {
			_, v, ok := s.store.Resolve(path + "[" + itoa(i) + "]")
			if !ok {
				continue
			}
			if code == cip.TypeBOOL {
				off := int(m.offset) + i/8
				if off < len(buf) && truthy(v) {
					buf[off] |= 1 << (i % 8)
				}
				continue
			}
			off := int(m.offset) + i*int(sizeForCIPType(code))
			if off < len(buf) {
				copy(buf[off:], EncodeLeaf(code, v))
			}
		}
	}
	return buf
}

// assembleElementaryArray serves a whole-array read of an elementary array
// symbol ("Trend" of REAL[4]): the type code, then every element from its
// "<base>[i]" leaf, back to back.
func (s *Server) assembleElementaryArray(path string) (header []byte, raw []byte, ok bool) {
	sym, found := s.schema.lookupSymbol(path)
	if !found || sym.symbolType&symbolTypeStructBit != 0 || sym.dims[0] == 0 {
		return nil, nil, false
	}
	code := sym.symbolType &^ (3 << symbolTypeDimShift)
	for i := 0; i < int(sym.dims[0]); i++ {
		_, v, _ := s.store.Resolve(path + "[" + itoa(i) + "]")
		raw = append(raw, EncodeLeaf(code, v)...)
	}
	return u16(code), raw, true
}

// writeTag serves Write Tag (0x4D) and Write Tag Fragmented (0x53): data
// is [u16 type][(struct) u16 handle][u16 count][(fragmented) u32 offset]
// [value bytes]. An elementary write lands count leaves from the one named;
// a structure (0x02A0) lands whole (writeStruct). Fragments are put back
// together by offset until the value is complete.
func (s *Server) writeTag(mr cip.MessageRouterRequest) cip.MessageRouterResponse {
	if len(mr.Path) == 0 || mr.Path[0] != cip.AnsiExtendedSymbol {
		return cip.MRError(mr.Service, cip.StatusPathSegmentError)
	}
	segs, err := cip.ParseSymbolicTag(mr.Path)
	if err != nil {
		return cip.MRError(mr.Service, cip.StatusPathSegmentError)
	}
	path := cip.CanonicalTagPath(segs)
	s.writeMu.Lock()
	s.writeLog = append(s.writeLog, path)
	s.writeMu.Unlock()
	data := mr.Data
	if len(data) < 4 {
		return cip.MRError(mr.Service, cip.StatusNotEnoughData)
	}
	typeCode := binary.LittleEndian.Uint16(data[0:2])
	hdr := 2
	var tmpl *templateDef
	if typeCode == 0x02A0 {
		hdr = 4
		if len(data) < 6 {
			return cip.MRError(mr.Service, cip.StatusNotEnoughData)
		}
		base, _, _ := splitTrailingIndex(path)
		sym, found := s.schema.lookupSymbol(base)
		if !found {
			return cip.MRError(mr.Service, cip.StatusPathDestUnknown)
		}
		t, isStruct := s.schema.templateFor(sym)
		if !isStruct || binary.LittleEndian.Uint16(data[2:4]) != t.handle {
			return cip.MRError(mr.Service, cip.StatusInvalidParameter)
		}
		tmpl = t
	}
	count := int(binary.LittleEndian.Uint16(data[hdr : hdr+2]))
	payload := data[hdr+2:]
	size := int(sizeForCIPType(typeCode))
	if tmpl != nil {
		size = int(tmpl.structSize)
	}
	total := size * count
	if mr.Service == cip.ServiceWriteTagFragmented {
		if len(payload) < 4 {
			return cip.MRError(mr.Service, cip.StatusNotEnoughData)
		}
		off := int(binary.LittleEndian.Uint32(payload[0:4]))
		whole, done := s.reassemble(path, off, payload[4:], total)
		if !done {
			return cip.MROK(mr.Service, nil)
		}
		payload = whole
	}
	if count < 1 || len(payload) < total {
		return cip.MRError(mr.Service, cip.StatusNotEnoughData)
	}
	if tmpl != nil {
		return s.writeStruct(mr, path, tmpl, count, payload)
	}
	base, index, hasIndex := splitTrailingIndex(path)
	start := 0
	if hasIndex {
		start = atoiOr(index, -1)
	}
	for i := 0; i < count; i++ {
		leaf := path
		if count > 1 || hasIndex {
			if start < 0 {
				return cip.MRError(mr.Service, cip.StatusPathSegmentError)
			}
			leaf = base + "[" + itoa(start+i) + "]"
		}
		leafType, _, ok := s.store.Resolve(leaf)
		if !ok {
			return cip.MRError(mr.Service, cip.StatusPathDestUnknown)
		}
		if leafType != typeCode {
			return cip.MRError(mr.Service, cip.StatusInvalidParameter)
		}
		v, ok := DecodeLeaf(typeCode, payload[i*size:])
		if !ok {
			return cip.MRError(mr.Service, cip.StatusNotEnoughData)
		}
		s.store.UpdateValue(leaf, v)
	}
	return cip.MROK(mr.Service, nil)
}

// reassemble collects one fragmented write's chunks by offset; done when
// all total bytes are in.
func (s *Server) reassemble(path string, off int, chunk []byte, total int) ([]byte, bool) {
	s.fragMu.Lock()
	defer s.fragMu.Unlock()
	if s.frags == nil {
		s.frags = map[string][]byte{}
	}
	buf := s.frags[path]
	if off == 0 || len(buf) != total {
		buf = make([]byte, total)
	}
	if off < 0 || off+len(chunk) > total {
		delete(s.frags, path)
		return nil, false
	}
	copy(buf[off:], chunk)
	if off+len(chunk) == total {
		delete(s.frags, path)
		return buf, true
	}
	s.frags[path] = buf
	return nil, false
}

func atoiOr(s string, def int) int {
	n := 0
	if s == "" {
		return def
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return def
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// writeStruct lands count whole structure instances, the way a Logix MSG
// writes a UDT: the root of a structure tag, an element of an array of
// them, or the whole array from its first element. Each member lands in
// its leaf from its template offset, the mirror of encodeStructInstance.
func (s *Server) writeStruct(mr cip.MessageRouterRequest, path string, tmpl *templateDef, count int, raw []byte) cip.MessageRouterResponse {
	base, index, hasIndex := splitTrailingIndex(path)
	sym, _ := s.schema.lookupSymbol(base)
	size := int(tmpl.structSize)
	switch {
	case sym.dims[0] == 0 && !hasIndex:
		if count != 1 {
			return cip.MRError(mr.Service, cip.StatusInvalidParameter)
		}
		s.decodeStructInstance(base, tmpl, raw[:size])
	default:
		start := 0
		if hasIndex {
			if start = atoiOr(index, -1); start < 0 {
				return cip.MRError(mr.Service, cip.StatusPathSegmentError)
			}
		}
		if start+count > int(sym.dims[0]) {
			return cip.MRError(mr.Service, cip.StatusInvalidParameter)
		}
		for i := 0; i < count; i++ {
			s.decodeStructInstance(base+"["+itoa(start+i)+"]", tmpl, raw[i*size:(i+1)*size])
		}
	}
	return cip.MROK(mr.Service, nil)
}

// decodeStructInstance writes one instance's bytes into the leaf store at
// dotted paths beneath prefix, the inverse of encodeStructInstance.
func (s *Server) decodeStructInstance(prefix string, tmpl *templateDef, buf []byte) {
	for _, m := range tmpl.members {
		path := prefix + "." + m.name
		isArray := m.typeCode&memberArrayBit != 0
		count := 1
		if isArray {
			count = int(m.typeInfo)
		}
		if m.typeCode&symbolTypeStructBit != 0 {
			child, ok := s.schema.templates[uint32(m.typeCode&symbolTypeTemplateMask)]
			if !ok {
				continue
			}
			for i := 0; i < count; i++ {
				off := int(m.offset) + i*int(child.structSize)
				if off+int(child.structSize) > len(buf) {
					break
				}
				p := path
				if isArray {
					p += "[" + itoa(i) + "]"
				}
				s.decodeStructInstance(p, child, buf[off:off+int(child.structSize)])
			}
			continue
		}
		code := m.typeCode &^ memberArrayBit
		if !isArray {
			if int(m.offset) < len(buf) {
				if v, ok := DecodeLeaf(code, buf[m.offset:]); ok {
					s.store.UpdateValue(path, v)
				}
			}
			continue
		}
		for i := 0; i < count; i++ {
			p := path + "[" + itoa(i) + "]"
			if code == cip.TypeBOOL {
				off := int(m.offset) + i/8
				if off < len(buf) {
					s.store.UpdateValue(p, buf[off]&(1<<(i%8)) != 0)
				}
				continue
			}
			off := int(m.offset) + i*int(sizeForCIPType(code))
			if off >= len(buf) {
				break
			}
			if v, ok := DecodeLeaf(code, buf[off:]); ok {
				s.store.UpdateValue(p, v)
			}
		}
	}
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
