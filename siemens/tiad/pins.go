package tiad

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// DeviceIdentity is what a device pin holds about the CPU at one address
// (siemens-ide.md §3.8): its order number and firmware, read from the
// station an upload brought into the project. Serial and MAC are nil when
// not read, never "": an online read of them is not proven yet, and a
// value is added only through an audited re-pin.
//
// The device pin catches a different model or firmware at the address. It
// does not catch an identical CPU swapped in, nor a machine in the middle
// on the first contact.
//
// The agent holds a second copy of these rules (tools/tiad/core/Pins.cs);
// the contract suite checks both over the wire. Change both or neither.
type DeviceIdentity struct {
	Order    string  `json:"order"`
	Firmware string  `json:"firmware"`
	Serial   *string `json:"serial"`
	MAC      *string `json:"mac"`
}

// MaxDeviceField caps each identity field.
const MaxDeviceField = 128

// Holds names the fields d holds: order and firmware always, serial and
// mac when read.
func (d DeviceIdentity) Holds() []string {
	h := []string{"order", "firmware"}
	if d.Serial != nil {
		h = append(h, "serial")
	}
	if d.MAC != nil {
		h = append(h, "mac")
	}
	return h
}

// Validate reports why d is not an identity tiad can pin.
func (d DeviceIdentity) Validate() error {
	var errs []string
	check := func(name string, v *string, required bool) {
		switch {
		case v == nil:
			if required {
				errs = append(errs, "device."+name+" is empty")
			}
			return
		case *v == "":
			if required {
				errs = append(errs, "device."+name+" is empty")
			} else {
				errs = append(errs, "device."+name+" is empty (leave it out when it was not read)")
			}
			return
		}
		// The agent counts UTF-16 code units; identity fields are ASCII.
		if len(*v) > MaxDeviceField {
			errs = append(errs, "device."+name+" is longer than "+strconv.Itoa(MaxDeviceField)+" characters")
		}
		for _, c := range *v {
			if c < 0x20 || c == 0x7f {
				errs = append(errs, "device."+name+" has a control character")
				break
			}
		}
	}
	check("order", &d.Order, true)
	check("firmware", &d.Firmware, true)
	check("serial", d.Serial, false)
	check("mac", d.MAC, false)
	if len(errs) > 0 {
		return fmt.Errorf("device: %s", strings.Join(errs, "; "))
	}
	return nil
}

// DeviceMismatch is how now differs from the pin, or "" when it matches.
// Every field the pin holds must be read now and be equal; a field the pin
// does not hold is not compared.
func DeviceMismatch(pinned, now DeviceIdentity) string {
	var diffs []string
	cmp := func(name string, p, n *string) {
		switch {
		case p == nil:
		case n == nil:
			diffs = append(diffs, name+" is pinned but was not read now")
		case *p != *n:
			diffs = append(diffs, name+" "+quote(*p)+" is now "+quote(*n))
		}
	}
	cmp("order", &pinned.Order, &now.Order)
	cmp("firmware", &pinned.Firmware, &now.Firmware)
	cmp("serial", pinned.Serial, now.Serial)
	cmp("mac", pinned.MAC, now.MAC)
	return strings.Join(diffs, "; ")
}

// DeviceAttestation is the operator's reading of the CPU's identity off
// the device itself (its label, its display), standing in for the online
// read tiad cannot make yet (#789, #1325). The agent accepts it only where
// that read is missing: it compares it with the device pin at the pin's
// precision, a mismatch is device_changed as a read one is, and it never
// adds or changes a pin. Every use is audited, and a download journals it
// in its deploy record. A "tighten later" loosening (#616): it goes once
// the online read exists.
//
// The agent holds a second copy of these rules (tools/tiad/core/Pins.cs).
// Change both or neither.
type DeviceAttestation struct {
	By       string `json:"by"`
	Order    string `json:"order"`
	Firmware string `json:"firmware"`
}

// Validate reports why a is malformed.
func (a DeviceAttestation) Validate() error {
	var errs []string
	controls := func(v string) bool {
		for _, c := range v {
			if c < 0x20 || c == 0x7f {
				return true
			}
		}
		return false
	}
	switch {
	case strings.Trim(a.By, " ") == "":
		errs = append(errs, "deviceAttestation.by is empty")
	case len(a.By) > MaxAttestationBy:
		errs = append(errs, "deviceAttestation.by is longer than "+strconv.Itoa(MaxAttestationBy)+" bytes")
	case controls(a.By):
		errs = append(errs, "deviceAttestation.by has a control character")
	}
	field := func(name, v string) {
		switch {
		case strings.Trim(v, " ") == "":
			errs = append(errs, "deviceAttestation."+name+" is empty")
		// The agent counts UTF-16 code units; identity fields are ASCII.
		case len(v) > MaxDeviceField:
			errs = append(errs, "deviceAttestation."+name+" is longer than "+strconv.Itoa(MaxDeviceField)+" characters")
		case controls(v):
			errs = append(errs, "deviceAttestation."+name+" has a control character")
		case !printableASCII(v):
			errs = append(errs, "deviceAttestation."+name+" is not printable ASCII (as an order number and a firmware version are)")
		}
	}
	field("order", a.Order)
	field("firmware", a.Firmware)
	if strings.Trim(a.Firmware, " ") != "" && len(a.Firmware) <= MaxDeviceField && printableASCII(a.Firmware) && firmwareParts(a.Firmware) == nil {
		errs = append(errs, "deviceAttestation.firmware "+quote(a.Firmware)+" is not a firmware version (V3.1.3, or 3.1.3)")
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// firmwareParts is a firmware version's numeric parts ("V3.1.3" and
// "3.1.3" are 3, 1, 3), leading zeros dropped; nil when it is not one.
func firmwareParts(v string) []string {
	v = strings.Trim(v, " ")
	if strings.HasPrefix(v, "V") || strings.HasPrefix(v, "v") {
		v = v[1:]
	}
	var parts []string
	for _, p := range strings.Split(v, ".") {
		if p == "" {
			return nil
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return nil
			}
		}
		n := strings.TrimLeft(p, "0")
		if n == "" {
			n = "0"
		}
		parts = append(parts, n)
	}
	return parts
}

// printableASCII is whether v holds printable ASCII only.
func printableASCII(v string) bool {
	for i := 0; i < len(v); i++ {
		if v[i] < 0x20 || v[i] > 0x7e {
			return false
		}
	}
	return true
}

// orderKey is an order number as compared: spaces dropped, ASCII letters
// upper case ("6ES7 510-1DK03-0AB0" is "6ES7510-1DK03-0AB0"). Nothing
// else is folded, so a character outside ASCII matches only itself (the
// attestation holds none).
func orderKey(v string) string {
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c == ' ':
			continue
		case c >= 'a' && c <= 'z':
			c -= 'a' - 'A'
		}
		b.WriteByte(c)
	}
	return b.String()
}

// DeviceAttestationGap is why a cannot stand in for the pin's check at
// all, or "". It states the order number and firmware only, so a pin
// holding a serial or MAC fails closed, and a firmware stated less
// precisely than the pin's cannot match it.
func DeviceAttestationGap(pinned DeviceIdentity, a DeviceAttestation) string {
	var gaps []string
	if pinned.Serial != nil {
		gaps = append(gaps, "the pin holds a serial number, which an attestation does not state")
	}
	if pinned.MAC != nil {
		gaps = append(gaps, "the pin holds a MAC address, which an attestation does not state")
	}
	if p, n := firmwareParts(pinned.Firmware), firmwareParts(a.Firmware); p == nil {
		gaps = append(gaps, "the pin's firmware "+quote(pinned.Firmware)+" is not a version an attestation can be compared with: re-pin it as one (POST /v1/pins/<address> with replace: true, audited)")
	} else if n != nil && len(n) < len(p) {
		gaps = append(gaps, "the attested firmware "+quote(a.Firmware)+" is less precise than the pin's "+quote(pinned.Firmware)+": read the full version off the CPU")
	}
	return strings.Join(gaps, "; ")
}

// DeviceAttestationMismatch is how a differs from the pin, at the pin's
// precision, or "". The order number compares without whitespace or
// case; the firmware on the pin's parts (a "V3.1" pin matches "3.1.3"). A
// pin firmware that is not a version compares exactly.
func DeviceAttestationMismatch(pinned DeviceIdentity, a DeviceAttestation) string {
	var diffs []string
	if orderKey(pinned.Order) != orderKey(a.Order) {
		diffs = append(diffs, "order "+quote(pinned.Order)+" is attested as "+quote(a.Order))
	}
	p, n := firmwareParts(pinned.Firmware), firmwareParts(a.Firmware)
	same := false
	if p == nil || n == nil {
		same = false // DeviceAttestationGap and Validate refuse both first
	} else if len(n) >= len(p) {
		same = true
		for i := range p {
			if p[i] != n[i] {
				same = false
				break
			}
		}
	}
	if !same {
		diffs = append(diffs, "firmware "+quote(pinned.Firmware)+" is attested as "+quote(a.Firmware))
	}
	return strings.Join(diffs, "; ")
}

// DeviceAttestationRecord is a device attestation the agent accepted: what
// the operator stated, the pin it was compared with, and when (the agent's
// clock).
type DeviceAttestationRecord struct {
	By       string         `json:"by"`
	Order    string         `json:"order"`
	Firmware string         `json:"firmware"`
	Pinned   DeviceIdentity `json:"pinned"`
	At       string         `json:"at"`
}

// quote is the agent's Json.Quote for the ASCII identity fields.
func quote(s string) string { return strconv.Quote(s) }

// PinRequest pins the CPU at an address by hand: a first pin before any
// contact (Replace false) or a re-pin after a deliberate change (Replace
// true). Both are audited with the old and new values.
type PinRequest struct {
	Device *DeviceIdentity `json:"device"`
	// Replace must be true exactly when a pin exists.
	Replace bool `json:"replace"`
	// Announced records that the people sharing the CPU were told first.
	Announced bool `json:"announced"`
}

// Validate is the request's own shape, with the agent's messages.
func (r PinRequest) Validate() error {
	if r.Device == nil {
		return fmt.Errorf("pin: device is required")
	}
	if err := r.Device.Validate(); err != nil {
		return fmt.Errorf("pin: %w", err)
	}
	if !r.Announced {
		return fmt.Errorf("pin: announce it first (announced: true): a pin decides which CPU tiad trusts")
	}
	return nil
}

// Pin is one device pin.
type Pin struct {
	Address string         `json:"address"`
	Device  DeviceIdentity `json:"device"`
	// Holds names the fields the pin actually holds.
	Holds []string `json:"holds"`
	// Previous is the pin a SetPin replaced (nil for a first pin, and
	// in a listing).
	Previous *DeviceIdentity `json:"previous,omitempty"`
}

// Pin outcomes of an upload.
const (
	PinFirstTrust = "first-trust" // no pin yet: the upload's identity is now the pin
	PinMatched    = "matched"     // the upload's identity matches the pin
)

// Pins lists the agent's device pins, by address.
func (c *Client) Pins(ctx context.Context) ([]Pin, error) {
	var r struct {
		Pins []Pin `json:"pins"`
	}
	_, err := c.doJSON(ctx, http.MethodGet, "/v1/pins", nil, &r)
	return r.Pins, err
}

// SetPin pins, or with req.Replace re-pins, the CPU at address.
func (c *Client) SetPin(ctx context.Context, address string, req PinRequest) (Pin, error) {
	var r Pin
	if !isIPv4(address) {
		return r, &Error{Kind: KindBadRequest, Message: fmt.Sprintf("pin: address %q is not an IPv4 address", address)}
	}
	if err := req.Validate(); err != nil {
		return r, &Error{Kind: KindBadRequest, Message: err.Error(), Err: err}
	}
	_, err := c.doJSON(ctx, http.MethodPost, "/v1/pins/"+url.PathEscape(address), req, &r)
	return r, err
}
