package devprofile

import (
	"fmt"
	"strconv"
	"strings"
)

// The IO-Link side of a master's profile (docs/design/iolink.md §3): where
// the master keeps each port's process data, identity and status. It
// names the profile's own points by template, never a node id or a
// register, so the binding stays the profile's one copy and a new master
// is profile data, not code.

// IOLinkSpec is a master profile's iolink: block.
type IOLinkSpec struct {
	// Ports is how many IO-Link ports the master has, numbered 1 to Ports.
	Ports int `yaml:"ports"`
	// PDIn is the point holding a port's raw process data in.
	PDIn *PDCarrier `yaml:"pd-in"`
	// PDOut is the point a port's process data out is written to; nil
	// when the master offers none.
	PDOut *PDCarrier `yaml:"pd-out"`
	// PDValid names a BOOL point that says the master holds valid process
	// data in for the port (optional).
	PDValid string `yaml:"pd-valid"`
	// VendorID and DeviceID name integer points holding the attached
	// device's IO-Link identity (optional; both or neither).
	VendorID string `yaml:"vendor-id"`
	DeviceID string `yaml:"device-id"`
	// Status names a point holding the port's mode or connection status
	// (optional).
	Status string `yaml:"status"`
}

// PDCarrier is where a master keeps a port's process data in one
// direction, and in what form.
type PDCarrier struct {
	// Point is a point-name template with {n} for the port number.
	Point string `yaml:"point"`
	// Form is how the point carries the octets: FormU32 or FormHex.
	Form string `yaml:"form"`
	// Doc cites where the form (and, for u32, where a short image sits)
	// is documented or was measured, "<docs id> <where>", as a point's
	// doc: does. The form is a claim of its own, beyond the point's.
	Doc string `yaml:"doc"`
	// Unverified marks a form no document or measurement supports.
	// A carrier needs Doc or Unverified.
	Unverified bool `yaml:"unverified"`
}

// Process data forms.
const (
	// FormU32 is an unsigned 32-bit integer holding up to four octets
	// (the P+F ICE3's PDI Data Unsigned32): written out big-endian, a
	// shorter image is its last octets (docs/design/iolink.md §3.1).
	FormU32 = "u32"
	// FormHex is text, two hex digits per octet (the ifm AL1590's pdin).
	FormHex = "hex"
)

// IOLinkPorts is how many IO-Link ports an IO-Link master profile has: the
// numbers a devices: row's ports: may use. 0 for a profile without an
// iolink: block, so a row's ports: is refused rather than ignored.
func (p *Profile) IOLinkPorts() int {
	if p.IOLink == nil {
		return 0
	}
	return p.IOLink.Ports
}

// PortPoint expands a template of the iolink: block for port n.
func PortPoint(template string, n int) string {
	return strings.ReplaceAll(template, "{n}", strconv.Itoa(n))
}

// What a template's point must be, per key.
var (
	u32Types = map[string]bool{"UDINT": true, "DWORD": true}
	// A vendor id is 16 bits, a device id 24: a 16-bit point cannot hold
	// a device id.
	vendorIDTypes = map[string]bool{"UINT": true, "WORD": true, "DINT": true, "UDINT": true, "DWORD": true, "LINT": true, "ULINT": true}
	deviceIDTypes = map[string]bool{"DINT": true, "UDINT": true, "DWORD": true, "LINT": true, "ULINT": true}
	anyPointType  = map[string]bool(nil)
)

// validateIOLink checks the iolink: block: the port count, the forms, and
// that every template names, for each port, a point the profile has, of a
// type its key can carry and in the direction it is used.
func (p *Profile) validateIOLink() error {
	s := p.IOLink
	if s == nil {
		return nil
	}
	if s.Ports < 1 || s.Ports > 64 {
		return fmt.Errorf("iolink: ports %d is not 1-64", s.Ports)
	}
	if s.PDIn == nil {
		return fmt.Errorf("iolink: pd-in is required (where the master keeps a port's process data)")
	}
	if (s.VendorID == "") != (s.DeviceID == "") {
		return fmt.Errorf("iolink: vendor-id and device-id go together: the identity check needs both")
	}
	type check struct {
		key, tmpl string
		types     map[string]bool
		writable  *bool
	}
	no, yes := false, true
	carrier := func(key string, c *PDCarrier, writable *bool) (check, error) {
		switch c.Form {
		case FormU32:
			return check{key, c.Point, u32Types, writable}, nil
		case FormHex:
			return check{key, c.Point, map[string]bool{"STRING": true}, writable}, nil
		case "":
			return check{}, fmt.Errorf("iolink: %s: form is required (%s or %s)", key, FormU32, FormHex)
		}
		return check{}, fmt.Errorf("iolink: %s: form %q is not %s or %s", key, c.Form, FormU32, FormHex)
	}
	for _, c := range []struct {
		key string
		c   *PDCarrier
	}{{"pd-in", s.PDIn}, {"pd-out", s.PDOut}} {
		if c.c == nil {
			continue
		}
		if err := p.citation(c.c.Doc, c.c.Unverified); err != nil {
			return fmt.Errorf("iolink: %s: %w", c.key, err)
		}
	}
	var checks []check
	in, err := carrier("pd-in", s.PDIn, &no)
	if err != nil {
		return err
	}
	checks = append(checks, in)
	if s.PDOut != nil {
		out, err := carrier("pd-out", s.PDOut, &yes)
		if err != nil {
			return err
		}
		checks = append(checks, out)
	}
	for _, c := range []check{
		{"pd-valid", s.PDValid, map[string]bool{"BOOL": true}, &no},
		{"vendor-id", s.VendorID, vendorIDTypes, &no},
		{"device-id", s.DeviceID, deviceIDTypes, &no},
		{"status", s.Status, anyPointType, &no},
	} {
		if c.tmpl != "" {
			checks = append(checks, c)
		}
	}
	for _, c := range checks {
		if !strings.Contains(c.tmpl, "{n}") {
			return fmt.Errorf("iolink: %s: %q has no {n}: each port needs a point of its own", c.key, c.tmpl)
		}
		for n := 1; n <= s.Ports; n++ {
			name := PortPoint(c.tmpl, n)
			pt, ok := p.Point(name)
			if !ok {
				return fmt.Errorf("iolink: %s: port %d's point %s is not in the profile", c.key, n, name)
			}
			if c.types != nil && !c.types[pt.Type] {
				return fmt.Errorf("iolink: %s: point %s is %s, which %s cannot be carried in", c.key, name, pt.Type, c.key)
			}
			if c.writable != nil && pt.Writable != *c.writable {
				if *c.writable {
					return fmt.Errorf("iolink: %s: point %s is not writable: the controller writes process data out", c.key, name)
				}
				return fmt.Errorf("iolink: %s: point %s is writable: the master, not the controller, produces it", c.key, name)
			}
		}
	}
	return nil
}

// citation applies a point's provenance rule to another claim: a doc: whose
// first word is the id of one of the profile's documents, or
// unverified: true.
func (p *Profile) citation(doc string, unverified bool) error {
	if doc == "" {
		if !unverified {
			return fmt.Errorf("cite a document for the form (doc:) or mark it unverified: true")
		}
		return nil
	}
	id, _, _ := strings.Cut(doc, " ")
	for _, d := range p.Docs {
		if d.ID == id {
			return nil
		}
	}
	return fmt.Errorf("doc %q is not the id of a document under docs", id)
}
