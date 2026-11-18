package iodd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Each case breaks the level IODD in one place and expects Parse (or
// Check) to say what is wrong, rather than decode something else.
func TestParseRefuses(t *testing.T) {
	base, err := os.ReadFile(filepath.Join("testdata", level))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, old, new, want string
	}{
		{"not IODD namespace", `xmlns="http://www.io-link.com/IODD/2010/10"`, `xmlns="urn:something-else"`, "namespace"},
		{"doctype", `<IODevice `, "<!DOCTYPE IODevice [<!ENTITY x \"y\">]>\n<IODevice ", "DOCTYPE"},
		{"vendor id 0", `vendorId="65261"`, `vendorId="0"`, "vendorId"},
		{"vendor id 17 bits", `vendorId="65261"`, `vendorId="65536"`, "vendorId"},
		{"device id 25 bits", `deviceId="1048577"`, `deviceId="16777216"`, "deviceId"},
		{"image too long", `id="PDI_Level" bitLength="32"`, `id="PDI_Level" bitLength="264"`, "bitLength"},
		{"item outside image", `<RecordItem subindex="1" bitOffset="18">`, `<RecordItem subindex="1" bitOffset="19">`, "outside the 32-bit image"},
		{"items overlap", `<RecordItem subindex="3" bitOffset="16">`, `<RecordItem subindex="3" bitOffset="15">`, "overlap"},
		{"subindex twice", `<RecordItem subindex="5" bitOffset="1">`, `<RecordItem subindex="4" bitOffset="1">`, "declared twice"},
		{"dangling DatatypeRef", `<DatatypeRef datatypeId="DT_Window"/>`, `<DatatypeRef datatypeId="DT_Nope"/>`, "DT_Nope"},
		{"integer too wide", `<SimpleDatatype xsi:type="IntegerT" bitLength="14"/>`, `<SimpleDatatype xsi:type="IntegerT" bitLength="65"/>`, "bitLength"},
		{"no xsi:type", `<SimpleDatatype xsi:type="IntegerT" bitLength="12"/>`, `<SimpleDatatype bitLength="12"/>`, "no xsi:type"},
		{"bad access", `accessRights="rw" defaultValue="100"`, `accessRights="rx" defaultValue="100"`, "accessRights"},
		{"gradient zero", `gradient="0.1"`, `gradient="0"`, "gradient"},
		{"scaling an unknown subindex", `<ProcessDataRecordItemInfo subindex="2" unitCode="1001"/>`, `<ProcessDataRecordItemInfo subindex="9" unitCode="1001"/>`, "subindex 9"},
		{"scaling a boolean", `<ProcessDataRecordItemInfo subindex="2" unitCode="1001"/>`, `<ProcessDataRecordItemInfo subindex="3" gradient="2"/>`, "only integers are scaled"},
		{"string encoding", `<SimpleDatatype xsi:type="BooleanT"/>
                <Name textId="TN_PDI_Ready"/>`, `<SimpleDatatype xsi:type="StringT" fixedLength="1" encoding="latin1"/>
                <Name textId="TN_PDI_Ready"/>`, "encoding"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(string(base), c.old) {
				t.Fatalf("the base IODD has no %q", c.old)
			}
			_, err := Parse([]byte(strings.Replace(string(base), c.old, c.new, 1)))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("Parse error %v, want one containing %q", err, c.want)
			}
		})
	}
	if _, err := Parse(nil); err == nil {
		t.Error("Parse(nil) accepted")
	}
	if _, err := Parse(make([]byte, MaxSize+1)); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Errorf("an oversize document: %v", err)
	}
}

// Check refuses what the codec cannot decode; Parse keeps it, since the
// IODD is still worth reading for its parameters.
func TestCheckRefusesWhatTheCodecCannotDecode(t *testing.T) {
	base, err := os.ReadFile(filepath.Join("testdata", level))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, old, new, want string }{
		{"TimeT", `<SimpleDatatype xsi:type="IntegerT" bitLength="12"/>`, `<SimpleDatatype xsi:type="TimeSpanT" bitLength="12"/>`, "TimeSpanT"},
		{"unaligned string", `<RecordItem subindex="2" bitOffset="4">
                <SimpleDatatype xsi:type="IntegerT" bitLength="12"/>`, `<RecordItem subindex="2" bitOffset="4">
                <SimpleDatatype xsi:type="OctetStringT" fixedLength="1"/>`, "not on an octet boundary"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(string(base), c.old) {
				t.Fatalf("the base IODD has no %q", c.old)
			}
			d, err := Parse([]byte(strings.Replace(string(base), c.old, c.new, 1)))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			in := d.ProcessData[0].In
			if err := in.Check(); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("Check error %v, want one containing %q", err, c.want)
			}
			if _, err := in.Decode(make([]byte, in.Octets())); err == nil {
				t.Error("Decode decoded what Check refuses")
			}
		})
	}
}
