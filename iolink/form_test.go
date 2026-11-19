package iolink

import (
	"bytes"
	"math/rand"
	"strings"
	"testing"
)

// The u32 form's short image is the U32's LAST big-endian octets
// (docs/design/iolink.md §3.1): 0x00000286 carries the 2-octet image
// 02 86. The other alignment, 0x02860000, is refused rather than read as
// 00 00, so a wrong guess about the P+F reads Bad, not plausibly wrong.
func TestImageU32(t *testing.T) {
	for _, c := range []struct {
		v      any
		octets int
		want   []byte
		err    string
	}{
		{uint32(0x00000286), 2, []byte{0x02, 0x86}, ""},
		{uint32(0x02860000), 2, nil, "leading octets are not zero"},
		{uint32(0x12345678), 4, []byte{0x12, 0x34, 0x56, 0x78}, ""},
		{uint32(0x000000FF), 1, []byte{0xFF}, ""},
		{uint32(0x00000100), 1, nil, "leading octets are not zero"},
		{uint64(0x00ABCDEF), 3, []byte{0xAB, 0xCD, 0xEF}, ""},
		{int64(0x286), 2, []byte{0x02, 0x86}, ""},
		{uint32(0), 5, nil, "does not fit the four octets"},
		{int64(-1), 4, nil, "not a 32-bit unsigned value"},
		{uint64(1 << 32), 4, nil, "not a 32-bit unsigned value"},
		{"0286", 2, nil, "is not an integer"},
	} {
		got, err := Image("u32", c.v, c.octets)
		switch {
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("Image(u32, %#x, %d) = % X, %v; want an error saying %q", c.v, c.octets, got, err, c.err)
		case c.err == "" && (err != nil || !bytes.Equal(got, c.want)):
			t.Errorf("Image(u32, %#x, %d) = % X, %v; want % X", c.v, c.octets, got, err, c.want)
		}
	}
}

func TestImageHex(t *testing.T) {
	for _, c := range []struct {
		v      any
		octets int
		want   []byte
		err    string
	}{
		{"0286", 2, []byte{0x02, 0x86}, ""}, // measured on an AL1590: '0286' is 0x0286
		{"abCD", 2, []byte{0xAB, 0xCD}, ""},
		{"0286", 3, nil, "4 digits; a 3-octet image is 6"},
		{"028", 2, nil, "3 digits"},
		{"0x86", 2, nil, "not hex digits"},
		{"02 8", 2, nil, "not hex digits"},
		{uint32(0x286), 2, nil, "not text"},
	} {
		got, err := Image("hex", c.v, c.octets)
		switch {
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("Image(hex, %v, %d) = % X, %v; want an error saying %q", c.v, c.octets, got, err, c.err)
		case c.err == "" && (err != nil || !bytes.Equal(got, c.want)):
			t.Errorf("Image(hex, %v, %d) = % X, %v; want % X", c.v, c.octets, got, err, c.want)
		}
	}
	if _, err := Image("bytes", []byte{1}, 1); err == nil {
		t.Error("an unknown form was accepted")
	}
}

func TestCarrier(t *testing.T) {
	if v, err := Carrier("u32", []byte{0x02, 0x86}); err != nil || v != uint32(0x286) {
		t.Errorf("Carrier(u32, 02 86) = %v, %v; want 0x286", v, err)
	}
	if v, err := Carrier("u32", []byte{0x12, 0x34, 0x56, 0x78}); err != nil || v != uint32(0x12345678) {
		t.Errorf("Carrier(u32, 12 34 56 78) = %v, %v", v, err)
	}
	if _, err := Carrier("u32", make([]byte, 5)); err == nil {
		t.Error("a 5-octet image went into a U32")
	}
	if v, err := Carrier("hex", []byte{0xAB, 0x0D}); err != nil || v != "AB0D" {
		t.Errorf("Carrier(hex, AB 0D) = %v, %v", v, err)
	}
	// Image undoes Carrier for every length a form carries.
	r := rand.New(rand.NewSource(705))
	for range 1000 {
		n := 1 + r.Intn(4)
		img := make([]byte, n)
		r.Read(img)
		for _, form := range []string{"u32", "hex"} {
			v, err := Carrier(form, img)
			if err != nil {
				t.Fatal(err)
			}
			back, err := Image(form, v, n)
			if err != nil || !bytes.Equal(back, img) {
				t.Fatalf("%s: % X → %v → % X, %v", form, img, v, back, err)
			}
		}
	}
}
