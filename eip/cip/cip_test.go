package cip

import (
	"bytes"
	"testing"
)

func TestEncapRoundTrip(t *testing.T) {
	h := EncapHeader{
		Command:       CmdRegisterSession,
		SessionHandle: 0xDEADBEEF,
		Status:        0,
		Options:       0,
	}
	copy(h.SenderContext[:], []byte("ABCDEFGH"))
	data := []byte{0x01, 0x00, 0x00, 0x00} // protocol v1, options
	frame := EncodeFrame(h, data)

	gotHdr, gotData, err := DecodeFrame(frame)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if gotHdr.Command != h.Command || gotHdr.SessionHandle != h.SessionHandle {
		t.Fatalf("header mismatch: %+v vs %+v", gotHdr, h)
	}
	if !bytes.Equal(gotData, data) {
		t.Fatalf("data mismatch: %v vs %v", gotData, data)
	}
}

func TestAssemblyEndpoints(t *testing.T) {
	// PowerFlex-style Forward_Open path: Class 4, config inst 1, ConnPoint
	// 20 (O→T), ConnPoint 70 (T→O). Each segment is 2 bytes (8-bit form).
	path := []byte{
		0x20, 0x04, // Class 4 (Assembly)
		0x24, 0x01, // Instance 1 (config)
		0x2C, 0x14, // ConnPoint 20 (O→T)
		0x2C, 0x46, // ConnPoint 70 (T→O)
	}
	oToT, tToO, ok := AssemblyEndpoints(path)
	if !ok {
		t.Fatal("AssemblyEndpoints returned !ok")
	}
	if oToT != 20 || tToO != 70 {
		t.Fatalf("got oToT=%d tToO=%d want 20/70", oToT, tToO)
	}

	// libplctag-style: Instance segments instead of ConnPoint.
	path2 := []byte{
		0x20, 0x04,
		0x24, 0x01,
		0x24, 0x15, // Instance 21
		0x24, 0x47, // Instance 71
	}
	oToT, tToO, ok = AssemblyEndpoints(path2)
	if !ok || oToT != 21 || tToO != 71 {
		t.Fatalf("instance form: got oToT=%d tToO=%d ok=%v want 21/71/true", oToT, tToO, ok)
	}
}

func TestPathBuildParse(t *testing.T) {
	tests := []struct {
		class, instance uint32
		attr            uint16
	}{
		{uint32(ClassIdentity), 1, 7},  // Identity.Inst1.Attr7 (product name)
		{uint32(ClassAssembly), 71, 3}, // Assembly.Inst71.Attr3 (data)
		{uint32(ClassParameter), 1, 1}, // Parameter.Inst1.Attr1 (value)
		{0x012F, 5, 0},                 // 16-bit class, no attribute
	}
	for _, tt := range tests {
		p := BuildPath(tt.class, tt.instance, tt.attr)
		parsed, n, err := ParsePath(p)
		if err != nil {
			t.Errorf("class=0x%x inst=%d: parse failed: %v", tt.class, tt.instance, err)
			continue
		}
		if n != len(p) {
			t.Errorf("class=0x%x inst=%d: parsed %d of %d bytes", tt.class, tt.instance, n, len(p))
		}
		if parsed.Class != tt.class || parsed.Instance != tt.instance {
			t.Errorf("class=0x%x inst=%d: got class=0x%x inst=%d", tt.class, tt.instance, parsed.Class, parsed.Instance)
		}
		if tt.attr != 0 && parsed.Attribute != uint32(tt.attr) {
			t.Errorf("attr %d: got %d", tt.attr, parsed.Attribute)
		}
	}
}

func TestCPFRoundTrip(t *testing.T) {
	items := []CPFItem{
		{TypeID: ItemNullAddress, Data: nil},
		{TypeID: ItemUnconnectedData, Data: []byte{0x01, 0x02, 0x03}},
	}
	enc := EncodeCPF(items)
	got, err := DecodeCPF(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 || got[0].TypeID != ItemNullAddress || got[1].TypeID != ItemUnconnectedData {
		t.Fatalf("unexpected items: %+v", got)
	}
	if !bytes.Equal(got[1].Data, []byte{0x01, 0x02, 0x03}) {
		t.Fatalf("data mismatch: %v", got[1].Data)
	}
}

func TestMRRequestDecode(t *testing.T) {
	// Service: Get_Attribute_Single (0x0E)
	// Path: Class=1, Instance=1, Attribute=7 (Identity product name)
	path := BuildPath(uint32(ClassIdentity), 1, 7)
	body := []byte{ServiceGetAttributeSingle, byte(len(path) / 2)}
	body = append(body, path...)

	req, err := DecodeMRRequest(body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if req.Service != ServiceGetAttributeSingle {
		t.Fatalf("service: %x", req.Service)
	}
	parsed, _, _ := ParsePath(req.Path)
	if parsed.Class != uint32(ClassIdentity) || parsed.Instance != 1 || parsed.Attribute != 7 {
		t.Fatalf("path: %+v", parsed)
	}
}
