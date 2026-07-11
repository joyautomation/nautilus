package logix

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/joyautomation/nautilus/eip/cip"
)

// instanceAttrs is the Symbol-class attribute set requested per instance
// during tag-list upload — the same set pycomm3 uses, so any controller (or
// emulator) exercised by pycomm3 answers it: 1 name, 2 symbol type, 3 symbol
// address, 5 symbol object address, 6 software control, 8 dimensions,
// 10 external access.
var instanceAttrs = []uint16{1, 2, 3, 5, 6, 8, 10}

// listScope walks the Symbol class in one scope ("" = controller, or
// "Program:Name") with Get_Instance_Attribute_List, continuing from the last
// instance while the controller reports partial transfer (0x06).
func (c *Controller) listScope(ctx context.Context, scope string) ([]Symbol, error) {
	var out []Symbol
	start := uint32(0)
	for {
		var path []byte
		if scope != "" {
			path = scopePrefixPath(scope)
		}
		path = append(path, cip.BuildPath(uint32(cip.ClassSymbol), start, 0)...)

		data := make([]byte, 2+2*len(instanceAttrs))
		binary.LittleEndian.PutUint16(data[0:2], uint16(len(instanceAttrs)))
		for i, a := range instanceAttrs {
			binary.LittleEndian.PutUint16(data[2+i*2:], a)
		}
		resp, err := c.sendConnected(ctx, cip.ServiceGetInstanceAttributeList, path, data)
		if err != nil {
			return nil, err
		}
		if resp.GeneralStatus != cip.StatusSuccess && resp.GeneralStatus != cip.StatusPartialTransfer {
			return nil, statusError("tag list", scope, resp)
		}
		syms, last, err := parseInstanceList(resp.Data)
		if err != nil {
			return nil, fmt.Errorf("logix: tag list scope %q: %w", scope, err)
		}
		out = append(out, syms...)
		if resp.GeneralStatus == cip.StatusSuccess {
			return out, nil
		}
		if len(syms) == 0 {
			return nil, fmt.Errorf("logix: tag list scope %q: partial reply with no instances", scope)
		}
		start = last + 1
	}
}

// parseInstanceList decodes the packed per-instance records of a
// Get_Instance_Attribute_List reply for instanceAttrs. Returns the symbols
// and the highest instance ID seen (the continuation cursor).
func parseInstanceList(b []byte) ([]Symbol, uint32, error) {
	var out []Symbol
	var last uint32
	off := 0
	for off < len(b) {
		if off+6 > len(b) {
			return nil, 0, fmt.Errorf("truncated instance record at %d", off)
		}
		var s Symbol
		s.InstanceID = binary.LittleEndian.Uint32(b[off : off+4])
		off += 4
		nameLen := int(binary.LittleEndian.Uint16(b[off : off+2]))
		off += 2
		if off+nameLen > len(b) {
			return nil, 0, fmt.Errorf("truncated symbol name at %d", off)
		}
		s.Name = string(b[off : off+nameLen])
		off += nameLen
		// symbol_type u16, symbol_address u32, symbol_object_address u32,
		// software_control u32, dims 3×u32, external_access u8.
		need := 2 + 4 + 4 + 4 + 12 + 1
		if off+need > len(b) {
			return nil, 0, fmt.Errorf("truncated attributes for %q", s.Name)
		}
		s.Type = binary.LittleEndian.Uint16(b[off : off+2])
		off += 2 + 4 + 4 + 4
		for d := 0; d < 3; d++ {
			s.Dims[d] = binary.LittleEndian.Uint32(b[off : off+4])
			off += 4
		}
		off++ // external access
		last = s.InstanceID
		out = append(out, s)
	}
	return out, last, nil
}

// hiddenSymbol filters controller-internal tags out of the upload: the
// system bit, Routine markers, and compiler-generated names ("__DEFVAL_*"
// default images, "__l*" FBD wire temporaries — anything double-underscored).
func hiddenSymbol(s Symbol) bool {
	return s.IsSystem() || strings.HasPrefix(s.Name, "__") || strings.HasPrefix(s.Name, "Routine:")
}

// ListSymbols uploads the full tag list: controller scope plus every program
// scope. Program-scope symbol names are canonicalized to
// "Program:<prog>.<name>". System tags, module container entries, and
// __DEFVAL_ compiler artifacts are filtered out; program markers become the
// Programs list.
func (c *Controller) ListSymbols(ctx context.Context) ([]Symbol, []string, error) {
	ctrl, err := c.listScope(ctx, "")
	if err != nil {
		return nil, nil, err
	}
	var out []Symbol
	var programs []string
	for _, s := range ctrl {
		if strings.HasPrefix(s.Name, "Program:") {
			programs = append(programs, strings.TrimPrefix(s.Name, "Program:"))
			continue
		}
		if hiddenSymbol(s) {
			continue
		}
		out = append(out, s)
	}
	for _, prog := range programs {
		scope := "Program:" + prog
		syms, err := c.listScope(ctx, scope)
		if err != nil {
			// A program that can't be walked shouldn't kill the whole browse.
			c.log.Warn("logix: skipping program scope", "program", prog, "error", err)
			continue
		}
		for _, s := range syms {
			if hiddenSymbol(s) {
				continue
			}
			s.Name = scope + "." + s.Name
			out = append(out, s)
		}
	}
	return out, programs, nil
}

// ReadTemplate uploads one UDT definition: GetAttributeList for the structure
// makeup (object definition size, structure size, member count, handle), then
// Read Tag on the Template instance for the member descriptors and names.
func (c *Controller) ReadTemplate(ctx context.Context, id uint16) (*Template, error) {
	t := &Template{ID: id}

	// Structure makeup — attributes in pycomm3's order: 4, 5, 2, 1.
	path := cip.BuildPath(uint32(cip.ClassTemplate), uint32(id), 0)
	data := []byte{4, 0, 4, 0, 5, 0, 2, 0, 1, 0}
	resp, err := c.sendConnected(ctx, cip.ServiceGetAttributeList, path, data)
	if err != nil {
		return nil, err
	}
	if resp.GeneralStatus != cip.StatusSuccess {
		return nil, statusError("template makeup", fmt.Sprintf("0x%x", id), resp)
	}
	memberCount, err := t.parseMakeup(resp.Data)
	if err != nil {
		return nil, fmt.Errorf("logix: template 0x%x makeup: %w", id, err)
	}

	// Body: pycomm3's sizing — (object definition size × 4) − 23 bytes of
	// payload (21 header bytes + 2 rounding), read in connection-size chunks.
	want := int(t.ObjDefSize)*4 - 21
	if want <= 0 {
		return nil, fmt.Errorf("logix: template 0x%x: bad object definition size %d", id, t.ObjDefSize)
	}
	// The status per chunk is not a reliable completion signal: real
	// controllers may answer Success for every chunk of a long template
	// (offset bookkeeping is the client's job), emulators may return the
	// whole remainder at once. Only the byte count terminates the loop.
	raw := make([]byte, 0, want)
	for len(raw) < want {
		chunk := want - len(raw)
		if chunk > maxPayload-8 {
			chunk = maxPayload - 8
		}
		body := make([]byte, 6)
		binary.LittleEndian.PutUint32(body[0:4], uint32(len(raw)))
		binary.LittleEndian.PutUint16(body[4:6], uint16(chunk))
		resp, err := c.sendConnected(ctx, cip.ServiceReadTag, path, body)
		if err != nil {
			return nil, err
		}
		if resp.GeneralStatus != cip.StatusSuccess && resp.GeneralStatus != cip.StatusPartialTransfer {
			return nil, statusError("template body", fmt.Sprintf("0x%x", id), resp)
		}
		if len(resp.Data) == 0 {
			return nil, fmt.Errorf("logix: template 0x%x body: no progress at offset %d (want %d)", id, len(raw), want)
		}
		raw = append(raw, resp.Data...)
	}
	if err := t.parseBody(raw, memberCount); err != nil {
		return nil, fmt.Errorf("logix: template 0x%x body: %w", id, err)
	}
	return t, nil
}

// parseMakeup decodes a GetAttributeList reply: [u16 count] then per entry
// [u16 attr][u16 status][value] with attribute-dependent value width.
func (t *Template) parseMakeup(b []byte) (int, error) {
	if len(b) < 2 {
		return 0, fmt.Errorf("short makeup reply")
	}
	count := int(binary.LittleEndian.Uint16(b[:2]))
	off := 2
	memberCount := 0
	for i := 0; i < count; i++ {
		if off+4 > len(b) {
			return 0, fmt.Errorf("truncated attribute entry %d", i)
		}
		attr := binary.LittleEndian.Uint16(b[off : off+2])
		status := binary.LittleEndian.Uint16(b[off+2 : off+4])
		off += 4
		width := 4
		if attr == 1 || attr == 2 {
			width = 2
		}
		if off+width > len(b) {
			return 0, fmt.Errorf("truncated attribute %d value", attr)
		}
		if status != 0 {
			off += width
			continue
		}
		switch attr {
		case 1:
			t.Handle = binary.LittleEndian.Uint16(b[off:])
		case 2:
			memberCount = int(binary.LittleEndian.Uint16(b[off:]))
		case 4:
			t.ObjDefSize = binary.LittleEndian.Uint32(b[off:])
		case 5:
			t.StructSize = binary.LittleEndian.Uint32(b[off:])
		}
		off += width
	}
	if memberCount == 0 {
		return 0, fmt.Errorf("member count missing")
	}
	return memberCount, nil
}

// parseBody decodes the template body: memberCount descriptors of
// [u16 info][u16 type][u32 offset], then a null-delimited name blob whose
// first string is "<TemplateName>;<n>" followed by member names in order.
func (t *Template) parseBody(raw []byte, memberCount int) error {
	descLen := memberCount * 8
	if len(raw) < descLen {
		return fmt.Errorf("body shorter than %d member descriptors", memberCount)
	}
	members := make([]Member, memberCount)
	for i := 0; i < memberCount; i++ {
		o := i * 8
		members[i] = Member{
			Info:   binary.LittleEndian.Uint16(raw[o : o+2]),
			Type:   binary.LittleEndian.Uint16(raw[o+2 : o+4]),
			Offset: binary.LittleEndian.Uint32(raw[o+4 : o+8]),
		}
	}
	names := splitNames(raw[descLen:])
	if len(names) == 0 {
		return fmt.Errorf("no name strings in body")
	}
	t.Name = names[0]
	if i := strings.IndexAny(t.Name, ";:"); i >= 0 {
		t.Name = t.Name[:i]
	}
	for i := range members {
		if i+1 < len(names) {
			members[i].Name = names[i+1]
		} else {
			members[i].Name = fmt.Sprintf("__member%d", i)
		}
	}
	t.Members = members
	return nil
}

// splitNames splits the null-delimited name blob, dropping empty strings and
// trailing padding garbage.
func splitNames(b []byte) []string {
	var out []string
	start := 0
	for i := 0; i <= len(b); i++ {
		if i == len(b) || b[i] == 0 {
			if i > start {
				s := string(b[start:i])
				if printable(s) {
					out = append(out, s)
				}
			}
			start = i + 1
		}
	}
	return out
}

func printable(s string) bool {
	for _, r := range s {
		if r < 0x20 || r > 0x7E {
			return false
		}
	}
	return true
}

// Browse uploads the controller's complete user tag database: all symbols
// plus every template reachable from struct symbols, following nested
// template references to any depth (Logix forbids cycles; the work queue
// naturally terminates).
func (c *Controller) Browse(ctx context.Context) (*BrowseResult, error) {
	symbols, programs, err := c.ListSymbols(ctx)
	if err != nil {
		return nil, err
	}
	templates := map[uint16]*Template{}
	var queue []uint16
	seen := map[uint16]bool{}
	for _, s := range symbols {
		if s.IsStruct() && !seen[s.TemplateID()] {
			seen[s.TemplateID()] = true
			queue = append(queue, s.TemplateID())
		}
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		t, err := c.ReadTemplate(ctx, id)
		if err != nil {
			c.log.Warn("logix: skipping unreadable template", "id", fmt.Sprintf("0x%x", id), "error", err)
			continue
		}
		templates[id] = t
		for _, m := range t.Members {
			if m.IsStruct() && !seen[m.NestedID()] {
				seen[m.NestedID()] = true
				queue = append(queue, m.NestedID())
			}
		}
	}
	return &BrowseResult{Symbols: symbols, Templates: templates, Programs: programs}, nil
}
