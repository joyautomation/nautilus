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
	t.Run("Attested", func(t *testing.T) { contractPinAttested(t, start) })
	t.Run("AttestedRefusals", func(t *testing.T) { contractPinAttestedRefusals(t, start) })
	t.Run("AttestationNeverPins", func(t *testing.T) { contractPinAttestationNeverPins(t, start) })
	t.Run("AttestedVerify", func(t *testing.T) { contractPinAttestedVerify(t, start) })
	t.Run("AttestedOnline", func(t *testing.T) { contractPinAttestedOnline(t, start) })
	t.Run("AttestationBadRequest", func(t *testing.T) { contractPinAttestationBadRequest(t, start) })
}

// contractPinUnreadableUpload: an upload that reads no usable identity
// pins nothing, and says so.
func contractPinUnreadableUpload(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	c := a.Client()
	a.SetDevice(t, tiad.DeviceIdentity{Firmware: "V3.1"}, false)
	if _, err := s.UploadWith(ctx, tiad.UploadRequest{Secrets: BenchLogin}); tiad.Kind(err) != tiad.KindDeviceUnverified ||
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
	r, err := s.UploadWith(ctx, tiad.UploadRequest{Secrets: BenchLogin})
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
	if r, err := s.UploadWith(ctx, tiad.UploadRequest{Secrets: BenchLogin}); err != nil || r.Pin != tiad.PinMatched {
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
	dl := tiad.DownloadRequest{Secrets: BenchDownloadSecrets(), Address: benchCPU, Software: true, Announced: true, Decisions: tiad.Decisions{StartAfter: tiad.No()}}
	dry := dl
	dry.DryRun = true
	for _, call := range []struct {
		name string
		do   func() error
	}{
		{"dry run", func() error { _, err := s.Download(ctx, dry); return err }},
		{"download", func() error { _, err := s.Download(ctx, dl); return err }},
		{"compare", func() error { _, err := s.CompareWith(ctx, BenchCompare); return err }},
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
	if _, err := s.UploadWith(ctx, tiad.UploadRequest{Secrets: BenchLogin}); tiad.Kind(err) != tiad.KindDeviceChanged || !strings.Contains(err.Error(), "The pin stands") {
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
	if r, err := s.UploadWith(ctx, tiad.UploadRequest{Secrets: BenchLogin}); err != nil || r.Pin != tiad.PinMatched {
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
	dl := tiad.DownloadRequest{Secrets: BenchDownloadSecrets(), Address: benchCPU, Software: true, Announced: true, Decisions: tiad.Decisions{StartAfter: tiad.No()}}
	if _, err := s.CompareWith(ctx, BenchCompare); err != nil {
		t.Fatalf("compare to an unpinned CPU: %v", err)
	}
	if _, err := c.SetPin(ctx, benchCPU, benchPinRequest); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompareWith(ctx, BenchCompare); tiad.Kind(err) != tiad.KindDeviceUnverified {
		t.Fatalf("compare to a pinned CPU it cannot identify: %v", err)
	}
	if _, err := s.Download(ctx, dl); tiad.Kind(err) != tiad.KindDeviceUnverified || !strings.Contains(err.Error(), "cannot be checked") ||
		!strings.Contains(err.Error(), "lifts for good once tiad can read a CPU's identity online") || !strings.Contains(err.Error(), "cannot lift this") {
		t.Fatalf("download to a pinned CPU it cannot identify: %v", err)
	}
	if l := a.Loaded(t); len(l) != 0 {
		t.Fatalf("an unverified CPU was loaded: %+v", l)
	}
	// The upload reads the identity from the project it brought in, so
	// it still checks the pin.
	if r, err := s.UploadWith(ctx, tiad.UploadRequest{Secrets: BenchLogin}); err != nil || r.Pin != tiad.PinMatched {
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
	if _, err := s.UploadWith(ctx, tiad.UploadRequest{Secrets: BenchLogin}); tiad.Kind(err) != tiad.KindDeviceChanged || !strings.Contains(err.Error(), "serial is pinned but was not read now") {
		t.Fatalf("upload that reads no serial, against a pin holding one: %v", err)
	}
	// A read serial that matches is a match; a pin without one ignores it.
	a.SetDevice(t, withSerial, false)
	if r, err := s.UploadWith(ctx, tiad.UploadRequest{Secrets: BenchLogin}); err != nil || r.Pin != tiad.PinMatched {
		t.Fatalf("upload reading the pinned serial: %+v %v", r, err)
	}
	a.SetDevice(t, tiad.DeviceIdentity{Order: withSerial.Order, Firmware: withSerial.Firmware, Serial: str("S-TEST-0002")}, false)
	if _, err := s.UploadWith(ctx, tiad.UploadRequest{Secrets: BenchLogin}); tiad.Kind(err) != tiad.KindDeviceChanged || !strings.Contains(err.Error(), `serial "S-TEST-0001" is now "S-TEST-0002"`) {
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

// benchAttestation is the bench CPU's identity as an operator reads it off
// the device: the order number without its space, and the firmware's full
// version, where the pin holds the station's "V3.1" (#1325).
var benchAttestation = &tiad.DeviceAttestation{By: "test operator", Order: "6es7510-1DK03-0AB0", Firmware: "3.1.3"}

// contractPinAttested (#1325): a pinned CPU the agent cannot identify
// online goes ahead on the operator's attestation of its order number and
// firmware, compared with the pin at the pin's precision: every online
// call, and the download's record keeps the attestation. The refusal
// without one names the route. The pin stands as it was.
func contractPinAttested(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	c := a.Client()
	a.SetDevice(t, BenchDevice(), true)
	if _, err := c.SetPin(ctx, benchCPU, benchPinRequest); err != nil {
		t.Fatal(err)
	}
	dl := tiad.DownloadRequest{Secrets: BenchDownloadSecrets(), Address: benchCPU, Software: true, Announced: true, Decisions: tiad.Decisions{StartAfter: tiad.No()}}
	_, err := s.Download(ctx, dl)
	if tiad.Kind(err) != tiad.KindDeviceUnverified || !strings.Contains(err.Error(), "--attest-order and --attest-firmware") ||
		!strings.Contains(err.Error(), "deviceAttestation: {by, order, firmware}") || !strings.Contains(err.Error(), "off the device itself") {
		t.Fatalf("download to a pinned CPU it cannot identify, no attestation: %v, want device_unverified naming the attestation route", err)
	}
	cmp := BenchCompare
	cmp.DeviceAttestation = benchAttestation
	if _, err := s.CompareWith(ctx, cmp); err != nil {
		t.Fatalf("attested compare: %v", err)
	}
	if _, err := s.Fingerprint(ctx, tiad.FingerprintRequest{Secrets: BenchLogin, DeviceAttestation: benchAttestation}); err != nil {
		t.Fatalf("attested fingerprint read: %v", err)
	}
	if _, err := s.Fingerprint(ctx, tiad.FingerprintRequest{Secrets: BenchLogin}); tiad.Kind(err) != tiad.KindDeviceUnverified {
		t.Fatalf("fingerprint read with no attestation: %v", err)
	}
	dry := dl
	dry.DryRun = true
	dry.DeviceAttestation = benchAttestation
	if _, err := s.Download(ctx, dry); tiad.Kind(err) == tiad.KindDeviceUnverified || tiad.Kind(err) == tiad.KindDeviceChanged {
		t.Fatalf("attested dry run refused on the pin: %v", err)
	}
	dl.DeviceAttestation = benchAttestation
	r, err := s.Download(ctx, dl)
	if err != nil {
		t.Fatalf("attested download: %v", err)
	}
	want := tiad.DeviceAttestationRecord{By: "test operator", Order: "6es7510-1DK03-0AB0", Firmware: "3.1.3", Pinned: BenchDevice()}
	got := r.DeviceAttestation
	if got == nil || got.At == "" {
		t.Fatalf("the download's response does not keep the attestation it rested on: %+v", got)
	}
	got2 := *got
	got2.At = ""
	if !reflect.DeepEqual(got2, want) {
		t.Fatalf("the response's attestation = %+v, want %+v", got2, want)
	}
	l := deploys(t, c, benchCPU)
	if len(l.Deploys) != 1 || l.Deploys[0].DeviceAttestation == nil || l.Deploys[0].DeviceAttestation.By != "test operator" ||
		l.Deploys[0].DeviceAttestation.Pinned != BenchDevice() {
		t.Fatalf("the deploy record does not journal the attestation: %+v", l.Deploys)
	}
	want0 := []tiad.Pin{{Address: benchCPU, Device: BenchDevice(), Holds: []string{"order", "firmware"}}}
	if !reflect.DeepEqual(pinsOf(t, c), want0) {
		t.Fatalf("an attested call changed the pin: %+v", pinsOf(t, c))
	}
}

// contractPinAttestedRefusals: an attestation that differs from the pin
// is device_changed, as a read identity is; one that cannot stand in for
// the pin (less precise firmware, a pin holding a serial) fails closed;
// an identity the agent can read wins over any attestation. Nothing loads.
func contractPinAttestedRefusals(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	c := a.Client()
	a.SetDevice(t, BenchDevice(), true)
	if _, err := c.SetPin(ctx, benchCPU, benchPinRequest); err != nil {
		t.Fatal(err)
	}
	dl := tiad.DownloadRequest{Secrets: BenchDownloadSecrets(), Address: benchCPU, Software: true, Announced: true, Decisions: tiad.Decisions{StartAfter: tiad.No()}}
	for _, x := range []struct {
		name, order, firmware, kind, want string
	}{
		{"other firmware", "6ES7 510-1DK03-0AB0", "V3.2.0", tiad.KindDeviceChanged, `firmware "V3.1" is attested as "V3.2.0"`},
		{"other minor on a longer read", "6ES7 510-1DK03-0AB0", "3.10.1", tiad.KindDeviceChanged, `firmware "V3.1" is attested as "3.10.1"`},
		{"other order", "6ES7 510-1DJ01-0AB0", "V3.1.3", tiad.KindDeviceChanged, `order "6ES7 510-1DK03-0AB0" is attested as "6ES7 510-1DJ01-0AB0"`},
		{"less precise", "6ES7 510-1DK03-0AB0", "V3", tiad.KindDeviceUnverified, `the attested firmware "V3" is less precise than the pin's "V3.1"`},
	} {
		d := dl
		d.DeviceAttestation = &tiad.DeviceAttestation{By: "test operator", Order: x.order, Firmware: x.firmware}
		_, err := s.Download(ctx, d)
		if tiad.Kind(err) != x.kind || !strings.Contains(err.Error(), x.want) || !strings.Contains(err.Error(), "refused before it began") {
			t.Errorf("%s: %v, want %s with %q", x.name, err, x.kind, x.want)
		}
		if x.kind == tiad.KindDeviceChanged && !strings.Contains(err.Error(), `the attestation by "test operator"`) {
			t.Errorf("%s: %v, want the refusal to say the identity is the attestation", x.name, err)
		}
	}
	// The firmware compares at the pin's precision: "3.1" and "V3.01.0"
	// are the pin's "V3.1".
	for _, fw := range []string{"3.1", "V3.01.0"} {
		cmp := BenchCompare
		cmp.DeviceAttestation = &tiad.DeviceAttestation{By: "test operator", Order: "6ES7 510-1DK03-0AB0", Firmware: fw}
		if _, err := s.CompareWith(ctx, cmp); err != nil {
			t.Errorf("attested firmware %q against the pin's V3.1: %v", fw, err)
		}
	}
	if l := a.Loaded(t); len(l) != 0 {
		t.Fatalf("a refused attestation loaded: %+v", l)
	}
	// A pin holding a serial: an attestation states none, so it fails
	// closed however it reads.
	withSerial := tiad.DeviceIdentity{Order: "6ES7 510-1DK03-0AB0", Firmware: "V3.1", Serial: str("S-TEST-0001")}
	if _, err := c.SetPin(ctx, benchCPU, tiad.PinRequest{Device: &withSerial, Replace: true, Announced: true}); err != nil {
		t.Fatal(err)
	}
	cmp := BenchCompare
	cmp.DeviceAttestation = benchAttestation
	if _, err := s.CompareWith(ctx, cmp); tiad.Kind(err) != tiad.KindDeviceUnverified || !strings.Contains(err.Error(), "the pin holds a serial number, which an attestation does not state") {
		t.Fatalf("attested compare against a pin holding a serial: %v", err)
	}
	// An identity the agent reads online wins: an attestation that says
	// the pinned firmware does not let a CPU read as another through.
	if _, err := c.SetPin(ctx, benchCPU, tiad.PinRequest{Device: &tiad.DeviceIdentity{Order: "6ES7 510-1DK03-0AB0", Firmware: "V3.1"}, Replace: true, Announced: true}); err != nil {
		t.Fatal(err)
	}
	a.SetDevice(t, tiad.DeviceIdentity{Order: "6ES7 510-1DK03-0AB0", Firmware: "V3.2"}, false)
	if _, err := s.CompareWith(ctx, cmp); tiad.Kind(err) != tiad.KindDeviceChanged || !strings.Contains(err.Error(), `firmware "V3.1" is now "V3.2"`) {
		t.Fatalf("attested compare to a CPU read online as changed: %v", err)
	}
}

// contractPinAttestationNeverPins: an attestation is never a pin: one to
// an unpinned CPU is not used, and the upload, which pins on first trust,
// takes none.
func contractPinAttestationNeverPins(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	c := a.Client()
	a.SetDevice(t, BenchDevice(), true)
	cmp := BenchCompare
	cmp.DeviceAttestation = &tiad.DeviceAttestation{By: "test operator", Order: "6ES7 999-9XX99-9XX9", Firmware: "V9.9"}
	if _, err := s.CompareWith(ctx, cmp); err != nil {
		t.Fatalf("attested compare to an unpinned CPU: %v", err)
	}
	if ps := pinsOf(t, c); len(ps) != 0 {
		t.Fatalf("an attestation pinned: %+v", ps)
	}
	status, b := rawJSON(t, c, "POST", "/v1/sessions/"+s.ID+"/upload",
		map[string]any{"secrets": map[string]any{"fullAccessPassword": BenchFullAccessPassword}, "deviceAttestation": map[string]any{"by": "x", "order": "y", "firmware": "1.0"}})
	if status != http.StatusBadRequest || !strings.Contains(errMessage(b), "deviceAttestation") {
		t.Fatalf("an upload carrying an attestation: HTTP %d %s, want 400 naming the unknown field", status, b)
	}
	if ps := pinsOf(t, c); len(ps) != 0 {
		t.Fatalf("a refused upload pinned: %+v", ps)
	}
	if r, err := s.UploadWith(ctx, tiad.UploadRequest{Secrets: BenchLogin}); err != nil || r.Pin != tiad.PinFirstTrust || !reflect.DeepEqual(r.Device, BenchDevice()) {
		t.Fatalf("first upload after an attested compare: %+v %v (the pin is the upload's read, never an attestation)", r, err)
	}
}

// contractPinAttestedVerify: a verify is an online call too: refused
// without an attestation, through with one.
func contractPinAttestedVerify(t *testing.T, start Starter) {
	a, d := deployed(t, start)
	c := a.Client()
	a.SetDevice(t, BenchDevice(), true)
	if _, err := c.SetPin(ctx, benchCPU, benchPinRequest); err != nil {
		t.Fatalf("pin: %v", err)
	}
	if _, err := c.VerifyDeploy(ctx, tiad.VerifyRequest{Secrets: BenchLogin, Deploy: d.ID}); tiad.Kind(err) != tiad.KindDeviceUnverified {
		t.Fatalf("verify of a pinned CPU with no attestation: %v", err)
	}
	noSession(t, c)
	if _, err := c.VerifyDeploy(ctx, tiad.VerifyRequest{Secrets: BenchLogin, Deploy: d.ID, DeviceAttestation: benchAttestation}); err != nil {
		t.Fatalf("attested verify: %v", err)
	}
	noSession(t, c)
}

// contractPinAttestedOnline: holding a project online in TIA's window
// (an agent with its UI) is an online call too.
func contractPinAttestedOnline(t *testing.T, start Starter) {
	a, s := compiledBenchWith(t, start, Config{UI: true})
	c := a.Client()
	a.SetDevice(t, BenchDevice(), true)
	if _, err := c.SetPin(ctx, benchCPU, benchPinRequest); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Online(ctx, tiad.OnlineRequest{Online: true, Secrets: BenchLogin}); tiad.Kind(err) != tiad.KindDeviceUnverified {
		t.Fatalf("online to a pinned CPU with no attestation: %v", err)
	}
	if _, err := s.Online(ctx, tiad.OnlineRequest{Online: true, Secrets: BenchLogin, DeviceAttestation: benchAttestation}); err != nil {
		t.Fatalf("attested online: %v", err)
	}
	if _, err := s.Online(ctx, tiad.OnlineRequest{Online: false}); err != nil {
		t.Fatalf("offline: %v", err)
	}
}

// contractPinAttestationBadRequest: a malformed attestation is a bad
// request on every call that takes one, before anything else.
func contractPinAttestationBadRequest(t *testing.T, start Starter) {
	a, s := compiledBenchWith(t, start, Config{UI: true})
	c := a.Client()
	good := map[string]any{"by": "test operator", "order": "6ES7 510-1DK03-0AB0", "firmware": "V3.1.3"}
	with := func(k string, v any) map[string]any {
		d := map[string]any{}
		for k0, v0 := range good {
			d[k0] = v0
		}
		d[k] = v
		return d
	}
	for _, x := range []struct {
		name string
		att  map[string]any
		want string
	}{
		{"no by", with("by", " "), "deviceAttestation.by is empty"},
		{"no order", with("order", ""), "deviceAttestation.order is empty"},
		{"no firmware", with("firmware", ""), "deviceAttestation.firmware is empty"},
		{"not a version", with("firmware", "three"), `deviceAttestation.firmware "three" is not a firmware version`},
		{"empty part", with("firmware", "V3..1"), "is not a firmware version"},
		{"control character", with("order", "6ES7\n510"), "deviceAttestation.order has a control character"},
		{"too long", with("order", strings.Repeat("6", 129)), "deviceAttestation.order is longer than 128"},
		{"unknown field", with("serial", "x"), "serial"},
	} {
		for _, call := range []string{"compare", "online", "fingerprint"} {
			body := map[string]any{"deviceAttestation": x.att}
			if call == "online" {
				body["online"] = true
			}
			status, b := rawJSON(t, c, "POST", "/v1/sessions/"+s.ID+"/"+call, body)
			if status != http.StatusBadRequest || envelopeKind(b) != tiad.KindBadRequest || !strings.Contains(errMessage(b), x.want) {
				t.Errorf("%s on %s: HTTP %d %s, want 400 with %q", x.name, call, status, b, x.want)
			}
		}
	}
}
