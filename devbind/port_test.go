package devbind

import (
	"os"
	"strings"
	"testing"
	"testing/fstest"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/iolink"
	"github.com/joyautomation/nautilus/lang/ir"
)

const levelFile = "acme-level100-20261118-IODD1.1.xml"

// Level100's identity (vendor 65261, device 1048577), its process data in
// (FE 0D FF D1: Level -12.5, Temperature -3, Ready, SP1 set) and its
// process data out (Teach bit 0, Mode bits 3..1, Lock bit 7).
const (
	levelVendor, levelDevice = 65261, 1048577
	levelImage               = "fe0dffd1"
)

func portFS(t *testing.T, profile string) fstest.MapFS {
	t.Helper()
	fsys := vendored(t, profile)
	raw, err := os.ReadFile("../iolink/iodd/testdata/" + levelFile)
	if err != nil {
		t.Fatal(err)
	}
	fsys["iodd/"+levelFile] = &fstest.MapFile{Data: raw}
	return fsys
}

func pfMaster(t *testing.T, mod func(*Device)) *Set {
	t.Helper()
	d := Device{Name: "M", Profile: "pepperl-fuchs-ice3", Endpoint: "h",
		Ports: map[int]Port{1: {Name: "LEVEL1", IODD: levelFile}}}
	if mod != nil {
		mod(&d)
	}
	s, err := Resolve(portFS(t, d.Profile), []Device{d})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A port device is a struct tag of its own, derived from its IODD, with
// the two companion tags and a command tag per process data out item; the
// master has no command tag of its own for the process data out or its
// valid flag.
func TestPortDeviceTagsAndTypes(t *testing.T) {
	s := pfMaster(t, nil)
	got := map[string]string{}
	for _, tg := range s.Tags() {
		got[tg.Name] = tg.Role + ":" + tg.Type
	}
	for name, want := range map[string]string{
		"M": "input:PEPPERL_FUCHS_ICE3", "LEVEL1": "input:ACME_LEVEL100", "LEVEL1__Identity": "input:INT",
		"LEVEL1__IdentityFault": "input:BOOL", "LEVEL1_Teach": "output:BOOL", "LEVEL1_Mode": "output:USINT", "LEVEL1_Lock": "output:BOOL",
	} {
		if got[name] != want && !strings.HasSuffix(got[name], strings.TrimPrefix(want, "input:")) {
			t.Errorf("tag %s = %q, want %q", name, got[name], want)
		}
	}
	for _, owned := range []string{"M_Port1_PdoBytes", "M_Port1_PdoValid", "M_Port1_PdoU32"} {
		_, has := got[owned]
		if owned == "M_Port1_PdoU32" != has { // the U32 twin is no port's carrier: still the master's
			t.Errorf("master command tag %s present = %v", owned, has)
		}
	}
	// Port 2 is no listed port: its pdo command tags are the master's.
	if _, ok := got["M_Port2_PdoBytes"]; !ok {
		t.Error("an unlisted port's pdo command tag is gone")
	}
	st, err := s.TypesST()
	if err != nil || !strings.Contains(st, "ACME_LEVEL100 : STRUCT") || !strings.Contains(st, "Level : REAL;") || !strings.Contains(st, "PdValid : BOOL;") {
		t.Errorf("types: %v\n%s", err, st)
	}
}

func TestPortDeviceResolveErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		mod  func(*Device)
		want string
	}{
		"a carrier point left out": {func(d *Device) {
			d.Points = []string{"Model", "Port1_VendorID", "Port1_DeviceID", "Port1_PdiValid", "Port1_PdoBytes", "Port1_PdoValid"}
		},
			"leave out Port1_PdiBytes"},
		"the pd-out point left out": {func(d *Device) {
			d.Points = []string{"Port1_PdiBytes", "Port1_VendorID", "Port1_DeviceID", "Port1_PdiValid", "Port1_PdoValid"}
		}, "leave out Port1_PdoBytes"},
		"the id left out": {func(d *Device) {
			d.Points = []string{"Port1_PdiBytes", "Port1_DeviceID", "Port1_PdiValid", "Port1_PdoBytes", "Port1_PdoValid"}
		}, "leave out Port1_VendorID"},
	} {
		d := Device{Name: "M", Profile: "pepperl-fuchs-ice3", Endpoint: "h", Ports: map[int]Port{1: {Name: "LEVEL1", IODD: levelFile}}}
		tc.mod(&d)
		if _, err := Resolve(portFS(t, d.Profile), []Device{d}); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
	// Two ports on one IODD share its type; a second device of the type
	// with other members is the check that names both.
	two := Device{Name: "M", Profile: "pepperl-fuchs-ice3", Endpoint: "h",
		Ports: map[int]Port{1: {Name: "LEVEL1", IODD: levelFile}, 2: {Name: "LEVEL2", IODD: levelFile}}}
	s, err := Resolve(portFS(t, two.Profile), []Device{two})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, st := range s.types {
		if st.Name == "ACME_LEVEL100" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("two ports on one IODD declare the type %d times", n)
	}
}

// fakeDriver captures what the master's driver is handed.
type fakeDriver struct{ got []nio.Values }

func (f *fakeDriver) WriteOutputs(v nio.Values) error {
	cp := nio.Values{}
	for k, x := range v {
		cp[k] = x
	}
	f.got = append(f.got, cp)
	return nil
}
func (f *fakeDriver) ReadInputs() (nio.Values, error) { return nil, nil }
func (f *fakeDriver) Close() error                    { return nil }

func masterVals(vendor, device int64, image string, valid bool) nio.Values {
	return nio.Values{
		"M.Port1_PdiBytes": image, "M.Port1_VendorID": vendor, "M.Port1_DeviceID": device, "M.Port1_PdiValid": valid,
	}
}

func portsOf(t *testing.T, s *Set) *ports {
	t.Helper()
	return newPorts(s.devices[0].ports)
}

func TestPortDeviceReadsAndChecksIdentity(t *testing.T) {
	ps := portsOf(t, pfMaster(t, nil))

	// Nothing from the master yet: nothing delivered.
	v := nio.Values{}
	ps.take(v)
	if _, ok := v["LEVEL1"]; ok || len(v) != 0 {
		t.Errorf("a port with nothing from the master delivered %v", v)
	}

	// The right device: the struct, identity Ok.
	v = masterVals(levelVendor, levelDevice, levelImage, true)
	ps.take(v)
	st, ok := v["LEVEL1"].(ir.Value)
	if !ok || st.Struct.Name != "ACME_LEVEL100" {
		t.Fatalf("LEVEL1 = %#v", v["LEVEL1"])
	}
	if st.Fld[0].F != -12.5 || st.Fld[1].I != -3 || !st.Fld[2].B || !st.Fld[3].B || st.Fld[4].B || !st.Fld[5].B {
		t.Errorf("members %+v, want Level -12.5, Temperature -3, Ready, SP1, not SP2, PdValid", st.Fld)
	}
	if v["LEVEL1__Identity"] != int64(0) || v["LEVEL1__IdentityFault"] != false {
		t.Errorf("identity %v fault %v, want Ok", v["LEVEL1__Identity"], v["LEVEL1__IdentityFault"])
	}
	if q := ps.quality(map[string]nio.Quality{}, nil); len(q) != 0 {
		t.Errorf("a good port has quality %v, want no entry", q)
	}

	// A wrong device: the fault, the old struct kept, the new bytes NOT
	// decoded, quality Bad.
	v = masterVals(levelVendor, levelDevice+100, "00000000", true)
	ps.take(v)
	if v["LEVEL1__Identity"] != int64(iolink.WrongDevice) || v["LEVEL1__IdentityFault"] != true {
		t.Errorf("wrong device: identity %v fault %v", v["LEVEL1__Identity"], v["LEVEL1__IdentityFault"])
	}
	if kept := v["LEVEL1"].(ir.Value); kept.Fld[0].F != -12.5 {
		t.Errorf("the bytes of a wrong device were decoded: Level %v", kept.Fld[0].F)
	}
	if q := ps.quality(map[string]nio.Quality{}, nil)["LEVEL1"]; q != nio.Bad {
		t.Errorf("wrong device quality %v, want Bad", q)
	}

	// No device (ids 0): the other fault.
	v = masterVals(0, 0, levelImage, true)
	ps.take(v)
	if v["LEVEL1__Identity"] != int64(iolink.NoDevice) || v["LEVEL1__IdentityFault"] != true {
		t.Errorf("no device: identity %v", v["LEVEL1__Identity"])
	}

	// The right one back, but the master says its data is invalid: Bad.
	v = masterVals(levelVendor, levelDevice, levelImage, false)
	ps.take(v)
	if v["LEVEL1__Identity"] != int64(iolink.Ok) {
		t.Errorf("identity %v", v["LEVEL1__Identity"])
	}
	if q := ps.quality(map[string]nio.Quality{}, nil)["LEVEL1"]; q != nio.Bad {
		t.Errorf("invalid process data quality %v, want Bad", q)
	}

	// A short image is a decode fault: Bad, never a partial value.
	v = masterVals(levelVendor, levelDevice, "fe0d", true)
	ps.take(v)
	if q := ps.quality(map[string]nio.Quality{}, nil)["LEVEL1"]; q != nio.Bad {
		t.Errorf("a short image quality %v, want Bad", q)
	}
}

// Outputs: nothing until a program changes every item; a wrong device
// gets nothing and the master's valid flag goes false; the right one back
// is sent nothing until the program writes again; the image goes out
// before the flag.
func TestPortDeviceOutputsFollowTheGate(t *testing.T) {
	ps := portsOf(t, pfMaster(t, nil))
	drv := &fakeDriver{}
	push := func(teach bool, mode int64, lock bool) nio.Values {
		t.Helper()
		if err := ps.write(drv, nio.Values{"LEVEL1_Teach": teach, "LEVEL1_Mode": mode, "LEVEL1_Lock": lock}); err != nil {
			t.Fatal(err)
		}
		return drv.got[len(drv.got)-1]
	}
	ok := func() { v := masterVals(levelVendor, levelDevice, levelImage, true); ps.take(v) }
	check := func(step string, got nio.Values, image any, valid bool) {
		t.Helper()
		if got["M_Port1_PdoBytes"] != image || got["M_Port1_PdoValid"] != valid {
			t.Errorf("%s: pdo %v valid %v, want %v / %v", step, got["M_Port1_PdoBytes"], got["M_Port1_PdoValid"], image, valid)
		}
		for _, tag := range []string{"LEVEL1_Teach", "LEVEL1_Mode", "LEVEL1_Lock"} {
			if _, leaked := got[tag]; leaked {
				t.Errorf("%s: the port's command tag %s reached the master's driver", step, tag)
			}
		}
	}
	ok()
	check("first push (the baseline)", push(false, 0, false), nil, false)
	check("only Mode changed", push(false, 5, false), nil, false)
	check("every item changed: image first, flag not yet", push(true, 5, true), "8b", false)
	check("next push: the flag", push(true, 5, true), "8b", true)

	// A wrong device: no image, the flag false.
	v := masterVals(levelVendor, levelDevice+100, levelImage, true)
	ps.take(v)
	check("wrong device", push(true, 5, true), nil, false)
	check("wrong device, still", push(true, 5, true), nil, false)

	// The right one back: the program has not written since, so nothing.
	ok()
	check("right device back, no new write", push(true, 5, true), nil, false)
	check("right device back, one item changed", push(false, 5, true), nil, false)
	check("right device back, every item written since the break (Teach, Mode, Lock)", push(true, 2, false), "05", false)
	check("and then the flag", push(true, 2, false), "05", true)
}

// Without a pd-out-valid point there is no flag to drop: a wrong device on
// the port is sent nothing new, but the master keeps its last image (what
// naut devices show says), and the identity fault is all that tells.
func TestPortDeviceOutputsWithoutAValidFlag(t *testing.T) {
	d := Device{Name: "M", Profile: "ifm-al1590", Endpoint: "h", Params: map[string]string{"location": "X"},
		Ports: map[int]Port{1: {Name: "LEVEL1", IODD: levelFile}}}
	s, err := Resolve(portFS(t, d.Profile), []Device{d})
	if err != nil {
		t.Fatal(err)
	}
	ps := portsOf(t, s)
	drv := &fakeDriver{}
	push := func(teach bool, mode int64, lock bool) nio.Values {
		if err := ps.write(drv, nio.Values{"LEVEL1_Teach": teach, "LEVEL1_Mode": mode, "LEVEL1_Lock": lock}); err != nil {
			t.Fatal(err)
		}
		return drv.got[len(drv.got)-1]
	}
	take := func(vendor, device int64) {
		ps.take(nio.Values{"M.Port1_Pdin": "FE0DFFD1", "M.Port1_VendorID": vendor, "M.Port1_DeviceID": device})
	}
	take(levelVendor, levelDevice)
	push(false, 0, false)
	got := push(true, 5, true)
	if got["M_Port1_Pdout"] != "8B" {
		t.Fatalf("pdout %v, want 8B (this profile's form is hex)", got["M_Port1_Pdout"])
	}
	for k := range got {
		if strings.Contains(k, "Valid") {
			t.Errorf("%s was written: this profile has no pd-out-valid", k)
		}
	}
	take(levelVendor, levelDevice+100)
	if got := push(false, 1, false); got["M_Port1_Pdout"] != nil {
		t.Errorf("a wrong device was sent %v", got["M_Port1_Pdout"])
	}
}

// A value that does not fit its item is an error naming the command tag,
// and the push still reaches the master.
func TestPortDeviceRefusesAValueThatDoesNotFit(t *testing.T) {
	ps := portsOf(t, pfMaster(t, nil))
	drv := &fakeDriver{}
	v := masterVals(levelVendor, levelDevice, levelImage, true)
	ps.take(v)
	_ = ps.write(drv, nio.Values{"LEVEL1_Teach": false, "LEVEL1_Mode": int64(0), "LEVEL1_Lock": false})
	err := ps.write(drv, nio.Values{"LEVEL1_Teach": true, "LEVEL1_Mode": int64(9), "LEVEL1_Lock": true})
	if err == nil || !strings.Contains(err.Error(), "LEVEL1_Mode") {
		t.Errorf("Mode 9 (3 bits): %v, want an error naming LEVEL1_Mode", err)
	}
	if len(drv.got) != 2 {
		t.Errorf("the master's driver was handed %d pushes, want 2", len(drv.got))
	}
}
