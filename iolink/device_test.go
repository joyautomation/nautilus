package iolink

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/iolink/iodd"
)

func deriveFile(t *testing.T, file string, pdValid bool) (*Device, error) {
	t.Helper()
	b, err := os.ReadFile("iodd/testdata/" + file)
	if err != nil {
		t.Fatal(err)
	}
	d, err := iodd.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return Derive(file, d, pdValid)
}

func TestTypeNameOf(t *testing.T) {
	for in, want := range map[string]string{
		"acme-level100-20261118-IODD1.1.xml":   "ACME_LEVEL100",
		"Acme-Press200-20261118-IODD1.0.1.xml": "ACME_PRESS200",
		"acme-odd.name-IODD1.1.xml":            "ACME_ODD_NAME",
		"sensor.xml":                           "SENSOR",
	} {
		if got, err := TypeNameOf(in); err != nil || got != want {
			t.Errorf("TypeNameOf(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if got, err := TypeNameOf("-IODD1.1.xml"); err == nil {
		t.Errorf("an empty name was accepted: %q", got)
	}
}

func TestDeriveLevel100(t *testing.T) {
	dev, err := deriveFile(t, "acme-level100-20261118-IODD1.1.xml", true)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range dev.Members {
		got = append(got, m.Name+":"+m.Type)
	}
	want := []string{"Level:REAL", "Temperature:INT", "Ready:BOOL", "Switch_point_1:BOOL", "Switch_point_2:BOOL", "PdValid:BOOL"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("members %v, want %v (a scaled integer is a REAL, a 12-bit integer an INT)", got, want)
	}
	got = nil
	for _, c := range dev.Commands {
		got = append(got, c.Name+":"+c.Type)
	}
	if want := []string{"Teach:BOOL", "Mode:USINT", "Lock:BOOL"}; !reflect.DeepEqual(got, want) {
		t.Errorf("commands %v, want %v", got, want)
	}
	if without, _ := deriveFile(t, "acme-level100-20261118-IODD1.1.xml", false); without.Members[len(without.Members)-1].Name == "PdValid" {
		t.Error("PdValid is a member of a device whose master has no pd-valid")
	}
}

// A layout that depends on a parameter needs the project to say which; the
// first pass has no way to, so it is refused naming the variable.
func TestDeriveRefusesAConditionalLayout(t *testing.T) {
	if _, err := deriveFile(t, "acme-flow300-20261118-IODD1.1.xml", false); err == nil || !strings.Contains(err.Error(), "V_PdSelect") {
		t.Errorf("flow300: %v", err)
	}
}

// Decode takes the carrier in each form; the same image gives the same
// members, and a wrong length is an error, not a partial value.
func TestDecodeThroughEachForm(t *testing.T) {
	dev, err := deriveFile(t, "acme-level100-20261118-IODD1.1.xml", true)
	if err != nil {
		t.Fatal(err)
	}
	want := []any{-12.5, int64(-3), true, true, false, true}
	// FE 0D FF D1: the hand vector of iodd's TestDecodeHandVectors.
	for form, carrier := range map[string]any{"u32": uint32(0xFE0DFFD1), "hex": "FE0DFFD1", "bytes": "fe0dffd1"} {
		got, err := dev.Decode(form, carrier, true)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %#v, %v; want %#v", form, got, err, want)
		}
	}
	if got, _ := dev.Decode("bytes", "fe0dffd1", false); got[len(got)-1] != false {
		t.Errorf("PdValid %v while the master says invalid", got[len(got)-1])
	}
	for name, c := range map[string]struct {
		form    string
		carrier any
	}{
		"short image":  {"bytes", "fe0dff"},
		"long image":   {"bytes", "fe0dffd100"},
		"not hex":      {"bytes", "fe0dffzz"},
		"not text":     {"bytes", uint32(1)},
		"u32 too wide": {"u32", "x"},
	} {
		if got, err := dev.Decode(c.form, c.carrier, true); err == nil {
			t.Errorf("%s: decoded to %v", name, got)
		}
	}
	// press200's image is six octets: a U32 cannot carry it.
	press, err := deriveFile(t, "acme-press200-20261118-IODD1.0.1.xml", false)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := press.Decode("u32", uint32(1), false); err == nil {
		t.Errorf("a 6-octet image through a u32: %v", got)
	}
}

// Two items whose texts make the same identifier, or none, get Item<subindex>.
func TestItemNames(t *testing.T) {
	taken := map[string]bool{}
	for _, c := range []struct {
		it   iodd.Item
		want string
	}{
		{iodd.Item{Subindex: 1, Name: "Level (mm)"}, "Level_mm"},
		{iodd.Item{Subindex: 2, Name: "level mm"}, "Item2"}, // the same identifier, any case
		{iodd.Item{Subindex: 3, Name: ""}, "Item3"},
		{iodd.Item{Subindex: 4, Name: "1st"}, "Item4"}, // starts with a digit: not an identifier
	} {
		if got := itemName(c.it, taken); got != c.want {
			t.Errorf("item %d %q is %q, want %q", c.it.Subindex, c.it.Name, got, c.want)
		}
	}
}

// The bytes carrier is written back lowercase, as the opcua driver reads
// and writes octets, and read in either case.
func TestBytesCarrier(t *testing.T) {
	if got, err := Carrier("bytes", []byte{0x02, 0xAB}); err != nil || got != "02ab" {
		t.Errorf("Carrier(bytes) = %v, %v; want 02ab", got, err)
	}
	if got, err := Image("bytes", "02AB", 2); err != nil || !reflect.DeepEqual(got, []byte{0x02, 0xAB}) {
		t.Errorf("Image(bytes, 02AB) = %v, %v", got, err)
	}
	if _, err := Carrier("bytes", nil); err == nil {
		t.Error("an empty image was carried")
	}
}
