//go:build !js

package project

import (
	"fmt"
	"strings"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// checkTakeoverHold checks redundancy.takeover-hold against the manifest's
// tags and drivers (#710 (b2)): the release tag is a BOOL state tag no
// driver and no restart sets, and every safe: tag is an output whose
// driver can write safe values.
func checkTakeoverHold(h *TakeoverHoldConfig, tags []TagConfig, drivers []nio.NamedDriver) error {
	if h == nil {
		return nil
	}
	byKey := make(map[string]TagConfig, len(tags))
	for _, t := range tags {
		byKey[ir.NameKey(t.Name)] = t
	}
	owner := map[string]nio.NamedDriver{}
	for _, d := range drivers {
		o, ok := d.Driver.(interface {
			InputNames() []string
			OutputNames() []string
		})
		if !ok {
			continue
		}
		for _, n := range o.InputNames() {
			owner[ir.NameKey(n)] = d
		}
		for _, n := range o.OutputNames() {
			owner[ir.NameKey(n)] = d
		}
	}

	rel, ok := byKey[ir.NameKey(h.Release)]
	const relWhat = "redundancy.takeover-hold.release %s: "
	switch {
	case !ok:
		return fmt.Errorf(relWhat+"no such tag in tags: (declare a BOOL tag with role: state)", h.Release)
	case !strings.EqualFold(rel.Role, "state"):
		return fmt.Errorf(relWhat+"is role %q: the release tag is a state tag only the program sets (an input, an output or a setpoint could release the hold from outside the program)", h.Release, rel.Role)
	case rel.Retain:
		return fmt.Errorf(relWhat+"is retained: a restart would carry a TRUE into the next hold; the release tag must not be retained", h.Release)
	case rel.Type != "" && !strings.EqualFold(rel.Type, "BOOL"):
		return fmt.Errorf(relWhat+"is of type %s: want BOOL", h.Release, rel.Type)
	}
	if _, isBool := rel.Init.(bool); rel.Init != nil && !isBool {
		return fmt.Errorf(relWhat+"init %v: want a BOOL tag (init: false)", h.Release, rel.Init)
	}
	if d, bound := owner[ir.NameKey(h.Release)]; bound {
		return fmt.Errorf(relWhat+"is bound to driver %q: the release tag is set by the program alone, never by I/O", h.Release, d.Name)
	}

	for tag := range h.Safe {
		t, ok := byKey[ir.NameKey(tag)]
		if !ok || !strings.EqualFold(t.Role, "output") {
			return fmt.Errorf("redundancy.takeover-hold.safe.%s: not an output tag: safe values are written to outputs while the hold holds them", tag)
		}
		d, bound := owner[ir.NameKey(tag)]
		if !bound {
			return fmt.Errorf("redundancy.takeover-hold.safe.%s: no driver writes this output", tag)
		}
		if _, ok := d.Driver.(nio.SafeWriter); !ok {
			return fmt.Errorf("redundancy.takeover-hold.safe.%s: safe values are not yet written for %s (driver %q): only modbus writes them so far. Remove the safe value; the hold still holds the output (it keeps the old leader's last value, or what the device does on comm loss)", tag, driverKind(d.Driver), d.Name)
		}
	}
	return nil
}

// driverKind names a driver's package (modbus, eip, ...) for a message.
func driverKind(d nio.Driver) string {
	s := strings.TrimPrefix(fmt.Sprintf("%T", d), "*")
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	return s
}

// TakeoverHold is the runtime's form of redundancy.takeover-hold, nil
// when the manifest sets none.
func (r *RedundancyConfig) RuntimeTakeoverHold() *runtime.TakeoverHold {
	if r == nil || r.TakeoverHold == nil {
		return nil
	}
	h := r.TakeoverHold
	out := &runtime.TakeoverHold{
		Release:      h.Release,
		Max:          h.MaxOr(),
		Always:       h.On == "always",
		ReleaseAtMax: h.OnMax == "release",
	}
	if len(h.Safe) > 0 {
		out.Safe = make(nio.Values, len(h.Safe))
		for k, v := range h.Safe {
			out.Safe[k] = normalize(v)
		}
	}
	return out
}
