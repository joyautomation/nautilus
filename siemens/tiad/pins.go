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
