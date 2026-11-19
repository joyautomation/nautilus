package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/iolink/iodd"
)

const ioddTestdata = "../../iolink/iodd/testdata"

func ioddProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte("name: iol\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestIOLinkImportVendors(t *testing.T) {
	dir := ioddProject(t)
	src := filepath.Join(ioddTestdata, "acme-level100-20261118-IODD1.1.xml")
	var out bytes.Buffer
	if err := importIODD(dir, src, &out); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "iodd", "acme-level100-20261118-IODD1.1.xml")
	if !bytes.Equal(mustRead(t, dst), mustRead(t, src)) {
		t.Error("the copy is not the IODD byte for byte")
	}
	for _, want := range []string{"added iodd/acme-level100-20261118-IODD1.1.xml", "vendor id 65261, device id 1048577", "iodd: acme-level100-20261118-IODD1.1.xml"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	// The same IODD again: nothing to do.
	out.Reset()
	if err := importIODD(dir, src, &out); err != nil || !strings.Contains(out.String(), "unchanged") {
		t.Errorf("re-import: %v\n%s", err, out.String())
	}
	// A different IODD under the same name never replaces the pin.
	other := filepath.Join(t.TempDir(), "acme-level100-20261118-IODD1.1.xml")
	press := mustRead(t, filepath.Join(ioddTestdata, "acme-press200-20261118-IODD1.0.1.xml"))
	if err := os.WriteFile(other, press, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := importIODD(dir, other, io.Discard); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Errorf("a different IODD of the same name: %v", err)
	}
	if !bytes.Equal(mustRead(t, dst), mustRead(t, src)) {
		t.Error("the pinned copy was replaced")
	}
}

// import checks before it copies: nothing lands in iodd/ that the project
// could not load.
func TestIOLinkImportRefuses(t *testing.T) {
	level := mustRead(t, filepath.Join(ioddTestdata, "acme-level100-20261118-IODD1.1.xml"))
	timeSpan := strings.ReplaceAll(string(level), `xsi:type="IntegerT" bitLength="12"`, `xsi:type="TimeSpanT" bitLength="12"`)
	for _, c := range []struct {
		name, file, body, want string
		noProject              bool
	}{
		{"not an IODD", "x-IODD1.1.xml", "<html/>", "not an IODD", false},
		{"not xml", "acme.iodd", string(level), "is a .xml file", false},
		{"no project", "acme-IODD1.1.xml", string(level), "not a project", true},
		{"undecodable process data", "acme-IODD1.1.xml", timeSpan, "TimeSpanT", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := ioddProject(t)
			if c.noProject {
				dir = t.TempDir()
			}
			src := filepath.Join(t.TempDir(), c.file)
			if err := os.WriteFile(src, []byte(c.body), 0o644); err != nil {
				t.Fatal(err)
			}
			err := importIODD(dir, src, io.Discard)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("import: %v, want an error saying %q", err, c.want)
			}
			if _, err := os.Lstat(filepath.Join(dir, "iodd")); err == nil {
				t.Error("iodd/ was created for a refused IODD")
			}
		})
	}
}

// A variant nautilus cannot decode doesn't stop an IODD whose other
// variant it can: the layout is chosen per port at load.
func TestIOLinkImportKeepsADecodableVariant(t *testing.T) {
	flow := mustRead(t, filepath.Join(ioddTestdata, "acme-flow300-20261118-IODD1.1.xml"))
	body := strings.Replace(string(flow), `<SimpleDatatype xsi:type="OctetStringT" fixedLength="2"/>`, `<SimpleDatatype xsi:type="TimeT" bitLength="16"/>`, 1)
	src := filepath.Join(t.TempDir(), "acme-flow300-IODD1.1.xml")
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := importIODD(ioddProject(t), src, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "note: process data PD_Tagged: ") || !strings.Contains(out.String(), "cannot be used") {
		t.Errorf("no note on the undecodable layout:\n%s", out.String())
	}
}

// import writes only into the project's own iodd/: never through a
// symlink there, nor into an iodd/ that is itself a link.
func TestIOLinkImportRefusesSymlinks(t *testing.T) {
	const name = "acme-level100-20261118-IODD1.1.xml"
	src := filepath.Join(ioddTestdata, name)
	for label, tc := range map[string]struct {
		plant func(t *testing.T, dir, outside string)
		want  string
	}{
		"iodd is a link": {func(t *testing.T, dir, outside string) {
			symlink(t, outside, filepath.Join(dir, "iodd"))
		}, "not a folder"},
		"dangling file link": {func(t *testing.T, dir, outside string) {
			mkdir(t, filepath.Join(dir, "iodd"))
			symlink(t, filepath.Join(outside, "planted.xml"), filepath.Join(dir, "iodd", name))
		}, "is a symlink"},
		"link to an identical file": {func(t *testing.T, dir, outside string) {
			if err := os.WriteFile(filepath.Join(outside, "planted.xml"), mustRead(t, src), 0o644); err != nil {
				t.Fatal(err)
			}
			mkdir(t, filepath.Join(dir, "iodd"))
			symlink(t, filepath.Join(outside, "planted.xml"), filepath.Join(dir, "iodd", name))
		}, "is a symlink"},
	} {
		t.Run(label, func(t *testing.T) {
			dir := ioddProject(t)
			outside := t.TempDir()
			tc.plant(t, dir, outside)
			before, _ := os.ReadDir(outside)
			err := importIODD(dir, src, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("import: %v, want an error saying %q", err, tc.want)
			}
			after, _ := os.ReadDir(outside)
			if len(after) != len(before) {
				t.Errorf("import wrote outside the project: %d entries before, %d after", len(before), len(after))
			}
		})
	}
}

func TestIOLinkShow(t *testing.T) {
	var out bytes.Buffer
	if code := runIOLinkShow([]string{filepath.Join(ioddTestdata, "acme-level100-20261118-IODD1.1.xml")}, &out); code != 0 {
		t.Fatalf("exit %d", code)
	}
	s := strings.Join(strings.Fields(out.String()), " ")
	for _, want := range []string{
		"Acme LV-100 level sensor (IODD 1.1)",
		"vendor id 65261 (0xFEED), device id 1048577 (0x100001), also 1048578",
		"in, 32 bits (4 octets)",
		// Level: bits 31..18, an IntegerT scaled to a REAL.
		"1 31..18 Level IntegerT REAL × 0.1 + 0 unit 1013",
		"out, 8 bits (1 octet)",
		"64 rw V_Setpoint Setpoint UIntegerT 100",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("show lacks %q:\n%s", want, out.String())
		}
	}
}

// A file larger than an IODD may be is read only as far as Parse needs to
// refuse it, never whole.
func TestIOLinkReadsAtMostMaxSize(t *testing.T) {
	big := filepath.Join(t.TempDir(), "huge-IODD1.1.xml")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(iodd.MaxSize + 4096); err != nil { // sparse
		t.Fatal(err)
	}
	f.Close()
	data, err := readIODD(big)
	if err != nil || len(data) != iodd.MaxSize+1 {
		t.Fatalf("readIODD read %d bytes, %v; want %d", len(data), err, iodd.MaxSize+1)
	}
	if err := importIODD(ioddProject(t), big, io.Discard); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Errorf("import of an oversize file: %v", err)
	}
}

// naut devices show prints a master's iolink: block with each carrier's
// provenance: a form no document supports shows as UNVERIFIED, and a point
// carried as octets shows its wire type, so a reader sees both.
func TestDevicesShowIOLinkProvenance(t *testing.T) {
	for id, want := range map[string][]string{
		"pepperl-fuchs-ice3": {"isdu: none.", "iolink: 8 ports", "pd-in Port{n}_PdiBytes as bytes (form: pf §12.2 p. 171)", "pd-out Port{n}_PdoBytes as bytes (form: pf §12.2 p. 172)", "pd-valid Port{n}_PdiValid", "device-id Port{n}_DeviceID", "wire ByteString", "pd-out-valid Port{n}_PdoValid"},
		"ifm-al1590":         {"iolink: 8 ports", "pd-in Port{n}_Pdin as hex (form: measured pdin hex text, '0286' is 0x0286)", "pd-out Port{n}_Pdout as hex (form: UNVERIFIED (al1427 p. 168", "vendor-id Port{n}_VendorID", "isdu: methods, read ns=5;s={location}/iolinkmaster/port[{n}]/iolinkdevice/MethodSet/ReadISDU", "data as hex, at most 32 octets", "pd-out-valid: none. With the wrong device on a port this master keeps sending its last process data out to it"},
		"sick-sig300":        {"iolink: 8 ports", "isdu: opc-30120, read ns=4;s=SIG300/Port {n}/Device/ReadISDU", "pd-in Port{n}_PdiBytes as bytes (form: opc30120 ProcessDataInput", "wire ByteArray", "UNVERIFIED"},
	} {
		var out bytes.Buffer
		if code := runDevicesShow([]string{id}, &out); code != 0 {
			t.Fatalf("%s: exit %d", id, code)
		}
		got := strings.Join(strings.Fields(out.String()), " ")
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: show lacks %q:\n%s", id, w, out.String())
			}
		}
	}
}
