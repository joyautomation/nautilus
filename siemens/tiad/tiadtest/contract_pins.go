package tiadtest

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// The pins half of the contract (siemens-ide.md §3.8): trust on first
// use, then pin. The first upload from a CPU pins its identity; every
// later online call to that address (upload, compare, download, a dry run
// too) refuses a different identity, and one it cannot read. A pin
// changes only through an announced, audited POST /v1/pins/{address}.

func contractPins(t *testing.T, start Starter) {
	t.Run("FirstTrust", func(t *testing.T) { contractPinFirstTrust(t, start) })
	t.Run("DeviceChanged", func(t *testing.T) { contractPinDeviceChanged(t, start) })
	t.Run("Unverified", func(t *testing.T) { contractPinUnverified(t, start) })
	t.Run("SetPin", func(t *testing.T) { contractSetPin(t, start) })
	t.Run("NotReadFailsClosed", func(t *testing.T) { contractPinNotRead(t, start) })
	t.Run("UnreadableUpload", func(t *testing.T) { contractPinUnreadableUpload(t, start) })
}

// contractPinUnreadableUpload: an upload that reads no usable identity
// pins nothing, and says so.
func contractPinUnreadableUpload(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	c := a.Client()
	a.SetDevice(t, tiad.DeviceIdentity{Firmware: "V3.1"}, false)
	if _, err := s.UploadWith(ctx, tiad.UploadRequest{}); tiad.Kind(err) != tiad.KindDeviceUnverified ||
		!strings.Contains(err.Error(), "device.order is empty") || !strings.Contains(err.Error(), "nothing was pinned") {
		t.Fatalf("upload reading no order number: %v", err)
	}
	if ps := pinsOf(t, c); len(ps) != 0 {
		t.Fatalf("an unreadable identity was pinned: %+v", ps)
	}
}

func str(s string) *string { return &s }

func pinsOf(t *testing.T, c *tiad.Client) []tiad.Pin {
	t.Helper()
	ps, err := c.Pins(ctx)
	if err != nil {
		t.Fatalf("pins: %v", err)
	}
	return ps
}

var benchPinRequest = tiad.PinRequest{Device: &tiad.DeviceIdentity{Order: "6ES7 510-1DK03-0AB0", Firmware: "V3.1"}, Announced: true}

// contractPinFirstTrust: no pin until the first upload; it pins order and
// firmware (serial and mac not read, as null), and a second upload of the
// same CPU matches.
func contractPinFirstTrust(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	c := a.Client()
	if ps := pinsOf(t, c); len(ps) != 0 {
		t.Fatalf("pins before any upload: %+v", ps)
	}
	// Serial and mac are null in the listing, never left out or "".
	status, b := rawJSON(t, c, "GET", "/v1/pins", nil)
	if status != http.StatusOK || !strings.Contains(string(b), `"pins":[]`) {
		t.Fatalf("empty listing: HTTP %d %s", status, b)
	}
	r, err := s.UploadWith(ctx, tiad.UploadRequest{})
	if err != nil {
		t.Fatalf("first upload: %v", err)
	}
	if r.Pin != tiad.PinFirstTrust || !reflect.DeepEqual(r.Device, BenchDevice()) {
		t.Fatalf("first upload: pin %q device %+v", r.Pin, r.Device)
	}
	ps := pinsOf(t, c)
	want := []tiad.Pin{{Address: benchCPU, Device: BenchDevice(), Holds: []string{"order", "firmware"}}}
	if !reflect.DeepEqual(ps, want) {
		t.Fatalf("pins after the first upload = %+v, want %+v", ps, want)
	}
	status, b = rawJSON(t, c, "GET", "/v1/pins", nil)
	if status != http.StatusOK || !strings.Contains(string(b), `"serial":null`) || !strings.Contains(string(b), `"mac":null`) {
		t.Fatalf("a pin's unread fields must be null: HTTP %d %s", status, b)
	}
	if r, err := s.UploadWith(ctx, tiad.UploadRequest{}); err != nil || r.Pin != tiad.PinMatched {
		t.Fatalf("second upload of the same CPU: %+v %v", r, err)
	}
	if !reflect.DeepEqual(pinsOf(t, c), want) {
		t.Fatal("a matching upload changed the pin")
	}
}

// contractPinDeviceChanged: once pinned, a CPU of another model or
// firmware at the address is refused by every online call, nothing is
// loaded and the pin stands; a re-pin lets it through.
func contractPinDeviceChanged(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	c := a.Client()
	if _, err := c.SetPin(ctx, benchCPU, benchPinRequest); err != nil {
		t.Fatalf("pin: %v", err)
	}
	updated := tiad.DeviceIdentity{Order: "6ES7 510-1DK03-0AB0", Firmware: "V3.2"}
	a.SetDevice(t, updated, false)
	dl := tiad.DownloadRequest{Address: benchCPU, Software: true, Announced: true, Decisions: tiad.Decisions{StartAfter: tiad.No()}}
	dry := dl
	dry.DryRun = true
	for _, call := range []struct {
		name string
		do   func() error
	}{
		{"dry run", func() error { _, err := s.Download(ctx, dry); return err }},
		{"download", func() error { _, err := s.Download(ctx, dl); return err }},
		{"compare", func() error { _, err := s.Compare(ctx); return err }},
	} {
		err := call.do()
		if tiad.Kind(err) != tiad.KindDeviceChanged || !strings.Contains(err.Error(), `firmware "V3.1" is now "V3.2"`) ||
			!strings.Contains(err.Error(), "POST /v1/pins/"+benchCPU) {
			t.Errorf("%s to a changed CPU: %v, want device_changed naming the change and the re-pin", call.name, err)
		}
	}
	if l := a.Loaded(t); len(l) != 0 {
		t.Fatalf("a changed CPU was loaded: %+v", l)
	}
	if _, err := s.UploadWith(ctx, tiad.UploadRequest{}); tiad.Kind(err) != tiad.KindDeviceChanged || !strings.Contains(err.Error(), "The pin stands") {
		t.Fatalf("upload from a changed CPU: %v", err)
	}
	if ps := pinsOf(t, c); len(ps) != 1 || ps[0].Device.Firmware != "V3.1" {
		t.Fatalf("a refused upload changed the pin: %+v", ps)
	}
	// A deliberate firmware update: re-pin, then the CPU is trusted again.
	p, err := c.SetPin(ctx, benchCPU, tiad.PinRequest{Device: &updated, Replace: true, Announced: true})
	if err != nil || p.Previous == nil || p.Previous.Firmware != "V3.1" || p.Device.Firmware != "V3.2" {
		t.Fatalf("re-pin: %+v %v", p, err)
	}
	if r, err := s.UploadWith(ctx, tiad.UploadRequest{}); err != nil || r.Pin != tiad.PinMatched {
		t.Fatalf("upload after the re-pin: %+v %v", r, err)
	}
}

// contractPinUnverified: a TIA side that cannot read a CPU's identity
// online (tiad.Openness today) cannot check a pin, so a pinned CPU is
// refused; an unpinned one has nothing to check.
func contractPinUnverified(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	c := a.Client()
	a.SetDevice(t, BenchDevice(), true)
	dl := tiad.DownloadRequest{Address: benchCPU, Software: true, Announced: true, Decisions: tiad.Decisions{StartAfter: tiad.No()}}
	if _, err := s.Compare(ctx); err != nil {
		t.Fatalf("compare to an unpinned CPU: %v", err)
	}
	if _, err := c.SetPin(ctx, benchCPU, benchPinRequest); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Compare(ctx); tiad.Kind(err) != tiad.KindDeviceUnverified {
		t.Fatalf("compare to a pinned CPU it cannot identify: %v", err)
	}
	if _, err := s.Download(ctx, dl); tiad.Kind(err) != tiad.KindDeviceUnverified || !strings.Contains(err.Error(), "cannot be checked") {
		t.Fatalf("download to a pinned CPU it cannot identify: %v", err)
	}
	if l := a.Loaded(t); len(l) != 0 {
		t.Fatalf("an unverified CPU was loaded: %+v", l)
	}
	// The upload reads the identity from the project it brought in, so
	// it still checks the pin.
	if r, err := s.UploadWith(ctx, tiad.UploadRequest{}); err != nil || r.Pin != tiad.PinMatched {
		t.Fatalf("upload: %+v %v", r, err)
	}
}

// contractPinNotRead: a field the pin holds must be read again. The
// upload reads no serial, so a pin holding one fails closed, never
// matching on what was not read.
func contractPinNotRead(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	c := a.Client()
	withSerial := tiad.DeviceIdentity{Order: "6ES7 510-1DK03-0AB0", Firmware: "V3.1", Serial: str("S-TEST-0001")}
	p, err := c.SetPin(ctx, benchCPU, tiad.PinRequest{Device: &withSerial, Announced: true})
	if err != nil || !reflect.DeepEqual(p.Holds, []string{"order", "firmware", "serial"}) {
		t.Fatalf("pin with a serial: %+v %v", p, err)
	}
	if _, err := s.UploadWith(ctx, tiad.UploadRequest{}); tiad.Kind(err) != tiad.KindDeviceChanged || !strings.Contains(err.Error(), "serial is pinned but was not read now") {
		t.Fatalf("upload that reads no serial, against a pin holding one: %v", err)
	}
	// A read serial that matches is a match; a pin without one ignores it.
	a.SetDevice(t, withSerial, false)
	if r, err := s.UploadWith(ctx, tiad.UploadRequest{}); err != nil || r.Pin != tiad.PinMatched {
		t.Fatalf("upload reading the pinned serial: %+v %v", r, err)
	}
	a.SetDevice(t, tiad.DeviceIdentity{Order: withSerial.Order, Firmware: withSerial.Firmware, Serial: str("S-TEST-0002")}, false)
	if _, err := s.UploadWith(ctx, tiad.UploadRequest{}); tiad.Kind(err) != tiad.KindDeviceChanged || !strings.Contains(err.Error(), `serial "S-TEST-0001" is now "S-TEST-0002"`) {
		t.Fatalf("upload reading another serial: %v", err)
	}
}

// contractSetPin: the pin call's own rules, over the wire.
func contractSetPin(t *testing.T, start Starter) {
	a := start(t, Config{Targets: []string{benchCPU}})
	c := a.Client()
	dev := map[string]any{"order": "6ES7 510-1DK03-0AB0", "firmware": "V3.1"}
	with := func(k string, v any) map[string]any {
		d := map[string]any{}
		for k0, v0 := range dev {
			d[k0] = v0
		}
		d[k] = v
		return d
	}
	for _, x := range []struct {
		name, address string
		body          map[string]any
		status        int
		kind, want    string
	}{
		{"not an address", "plc.local", map[string]any{"device": dev, "announced": true}, 400, tiad.KindBadRequest, "not an IPv4 address"},
		{"leading zero", "192.0.2.010", map[string]any{"device": dev, "announced": true}, 400, tiad.KindBadRequest, "not an IPv4 address"},
		{"not a target", otherCPU, map[string]any{"device": dev, "announced": true}, 403, tiad.KindTargetRefused, "TIAD_TARGETS"},
		{"no device", benchCPU, map[string]any{"announced": true}, 400, tiad.KindBadRequest, "device is required"},
		{"empty order", benchCPU, map[string]any{"device": with("order", ""), "announced": true}, 400, tiad.KindBadRequest, "device.order is empty"},
		{"no firmware", benchCPU, map[string]any{"device": map[string]any{"order": "x"}, "announced": true}, 400, tiad.KindBadRequest, "device.firmware is empty"},
		{"empty serial", benchCPU, map[string]any{"device": with("serial", ""), "announced": true}, 400, tiad.KindBadRequest, "leave it out when it was not read"},
		{"control character", benchCPU, map[string]any{"device": with("mac", "00\n11"), "announced": true}, 400, tiad.KindBadRequest, "device.mac has a control character"},
		{"too long", benchCPU, map[string]any{"device": with("firmware", strings.Repeat("V", 129)), "announced": true}, 400, tiad.KindBadRequest, "longer than 128"},
		{"not announced", benchCPU, map[string]any{"device": dev}, 400, tiad.KindBadRequest, "announce it first"},
		{"unknown field", benchCPU, map[string]any{"device": dev, "announced": true, "force": true}, 400, tiad.KindBadRequest, "force"},
		{"unknown device field", benchCPU, map[string]any{"device": with("model", "x"), "announced": true}, 400, tiad.KindBadRequest, "model"},
		{"replace with no pin", benchCPU, map[string]any{"device": dev, "announced": true, "replace": true}, 404, tiad.KindNotFound, "no device pin to replace"},
	} {
		status, b := rawJSON(t, c, "POST", "/v1/pins/"+x.address, x.body)
		if status != x.status || envelopeKind(b) != x.kind || !strings.Contains(errMessage(b), x.want) {
			t.Errorf("%s: HTTP %d %s, want %d %s with %q", x.name, status, b, x.status, x.kind, x.want)
		}
	}
	if ps := pinsOf(t, c); len(ps) != 0 {
		t.Fatalf("a refused pin call pinned: %+v", ps)
	}
	if p, err := c.SetPin(ctx, benchCPU, benchPinRequest); err != nil || p.Previous != nil || p.Address != benchCPU {
		t.Fatalf("first pin: %+v %v", p, err)
	}
	status, b := rawJSON(t, c, "POST", "/v1/pins/"+benchCPU, map[string]any{"device": dev, "announced": true})
	if status != http.StatusBadRequest || !strings.Contains(errMessage(b), "pinned already") || !strings.Contains(errMessage(b), "replace: true") {
		t.Fatalf("a second pin without replace: HTTP %d %s", status, b)
	}
}
