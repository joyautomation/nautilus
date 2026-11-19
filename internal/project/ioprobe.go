//go:build !js

package project

// Live validation (#706 PR 4, decision 3): before an apply, each changed
// device is probed on a connection of its own. The probe is the candidate's
// own driver for that device, built as the apply would build it, its write
// gate closed so it never writes, started until every tag has read once or
// the deadline passes. It runs off the scan and holds no runtime lock.
//
// A device is probed live or checked statically by, in order: its
// devices: row's validate:, its driver's validate:, then its profile's
// default (devbind.Set.Validate: static when the profile documents Modbus
// max-connections of 2 or fewer). A source of a manifest file, with no
// profile, is live unless its driver says static.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/devbind"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/modbus"
	"github.com/joyautomation/nautilus/opcua"
)

// IOProbe is one changed device's live check.
type IOProbe struct {
	Driver string `json:"driver"`
	Device string `json:"device,omitempty"`
	// Mode is live (probed) or static (not probed), and Why is why.
	Mode string `json:"mode"`
	Why  string `json:"why"`
	OK   bool   `json:"ok"`
	// Detail is what went wrong, and what to do about it.
	Detail string `json:"detail,omitempty"`
	// Bad are the tags that did not read, with their quality.
	Bad []string `json:"bad,omitempty"`
}

// probeCap bounds one probe.
var probeCap = 5 * time.Second

// probeRetries is how many failed connection attempts end a probe early.
const probeRetries = 3

// probeTarget is one device to probe: a driver for it alone, built and not
// started, the data tags it must read, and how to ask if it is connected.
type probeTarget struct {
	IOProbe
	addr   string
	build  func() (nio.Driver, error)
	inputs func(nio.Driver) []string
	health func(nio.Driver) (connected bool, lastErr string, retries uint64)
}

// modeFor resolves how the device (a Modbus source, or the OPC UA device
// an opcua-client driver is) of the candidate's driver is checked.
func modeFor(c *IOCandidate, driver, device string) (mode, why string) {
	cfg := c.configs[driver]
	name := device
	if name == "" {
		name = cfg.opcuaDevice
	}
	if name != "" && c.m != nil {
		if row, def, defWhy, ok := c.m.DeviceSet().Validate(name); ok {
			switch {
			case row != "":
				return row, "set on device " + name + "'s devices: row"
			case cfg.Validate != "":
				return cfg.Validate, "set on driver " + driver
			}
			return def, defWhy
		}
	}
	if cfg.Validate != "" {
		return cfg.Validate, "set on driver " + driver
	}
	return devbind.ValidateLive, "no connection limit documented (not a device of the library)"
}

// probeTargets are what the changes put on the wire: a changed Modbus
// device, each device of a Modbus driver swapped whole, an OPC UA driver.
func probeTargets(c *IOCandidate, changes []IOChange) []probeTarget {
	var out []probeTarget
	modbusSource := func(driver string, u ioUnit) probeTarget {
		mode, why := modeFor(c, driver, u.device)
		src, tags := u.src, append([]modbus.TagBinding(nil), u.tags...)
		return probeTarget{
			IOProbe: IOProbe{Driver: driver, Device: u.device, Mode: mode, Why: why},
			addr:    endpointOf(src),
			build: func() (nio.Driver, error) {
				return modbus.New(modbus.Manifest{Sources: []modbus.Source{src}, Tags: tags},
					modbus.WithScanRate(50*time.Millisecond), modbus.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
			},
			inputs: func(nio.Driver) []string {
				var names []string
				for _, t := range tags {
					if !t.WriteOnly {
						names = append(names, t.Name)
					}
				}
				return names
			},
			health: func(d nio.Driver) (bool, string, uint64) {
				h := d.(*modbus.Driver).Health()
				if len(h.Sources) == 0 {
					return false, "", 0
				}
				s := h.Sources[0]
				return s.State == "connected", s.LastError, s.Retries
			},
		}
	}
	for _, ch := range changes {
		switch {
		case ch.Device != "":
			out = append(out, modbusSource(ch.Driver, c.units[unitKey(ch.Driver, ch.Device)]))
		case c.units[ch.Driver].typ == "modbus":
			for _, k := range sortedKeys(c.units) {
				if u := c.units[k]; u.driver == ch.Driver && u.device != "" {
					out = append(out, modbusSource(ch.Driver, u))
				}
			}
		case c.units[ch.Driver].typ == "opcua-client":
			cfg, fsys := c.configs[ch.Driver], c.fsys
			mode, why := modeFor(c, ch.Driver, "")
			out = append(out, probeTarget{
				IOProbe: IOProbe{Driver: ch.Driver, Device: cfg.opcuaDevice, Mode: mode, Why: why},
				addr:    cfg.Endpoint,
				build:   func() (nio.Driver, error) { return buildDriver(fsys, cfg) },
				inputs: func(d nio.Driver) []string {
					ua := opcuaOf(d)
					var names []string
					for _, n := range ua.InputNames() {
						if n != ua.OnlineTagName() {
							names = append(names, n)
						}
					}
					return names
				},
				health: func(d nio.Driver) (bool, string, uint64) {
					h := opcuaOf(d).Health()
					return h.State == "connected", h.LastError, h.Retries
				},
			})
		}
	}
	return out
}

// opcuaOf is the OPC UA client driver d is or wraps (devbind.OPCUADriver).
func opcuaOf(d nio.Driver) *opcua.Driver {
	for d != nil {
		if ua, ok := d.(*opcua.Driver); ok {
			return ua
		}
		u, ok := d.(interface{ Unwrap() nio.Driver })
		if !ok {
			return nil
		}
		d = u.Unwrap()
	}
	return nil
}

// probe runs the live targets, at once, and returns every target's result.
func probe(targets []probeTarget) []IOProbe {
	out := make([]IOProbe, len(targets))
	var wg sync.WaitGroup
	for i, tg := range targets {
		if tg.Mode != devbind.ValidateLive {
			out[i] = tg.IOProbe
			out[i].OK = true
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = runProbe(tg)
		}()
	}
	wg.Wait()
	return out
}

// runProbe starts a driver for the target alone, never writing, until every
// data tag has read once (passed) or probeCap passes (failed).
func runProbe(tg probeTarget) IOProbe {
	p := tg.IOProbe
	d, err := tg.build()
	if err != nil {
		p.Detail = "the check's own driver did not build: " + err.Error()
		return p
	}
	if g, ok := d.(interface{ SetWriteGate(func() bool) }); ok {
		g.SetWriteGate(func() bool { return false }) // a check never writes
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeCap)
	defer cancel()
	if s, ok := d.(interface{ Start(context.Context) }); ok {
		s.Start(ctx)
	}
	defer func() {
		if s, ok := d.(interface{ Stop() }); ok {
			s.Stop()
		}
	}()
	inputs := tg.inputs(d)
	quality := func() map[string]nio.Quality {
		if q, ok := d.(nio.QualityReporter); ok {
			return q.Quality()
		}
		return nil
	}
	everConnected, lastErr := false, ""
	read := map[string]bool{} // the tags that have read once
	refused := func() {
		p.Detail = fmt.Sprintf("the device at %s did not answer a connection of the check's own", tg.addr)
		if lastErr != "" {
			p.Detail += " (" + lastErr + ")"
		}
		p.Detail += "; it may allow only one connection: set validate: static on its devices: row or its driver"
	}
	for {
		connected, le, retries := tg.health(d)
		everConnected = everConnected || connected
		if le != "" {
			lastErr = le
		}
		if !everConnected && retries >= probeRetries {
			refused() // refused outright, again and again: no need to wait out the cap
			return p
		}
		if connected {
			// Each tag must read once; one that read and went stale since
			// (a later block's exception upsetting the source) has.
			q := quality()
			all := true
			for _, n := range inputs {
				if qq, bad := q[n]; !bad || qq.IsGood() {
					read[n] = true
				}
				all = all && read[n]
			}
			if all {
				p.OK = true
				return p
			}
		}
		select {
		case <-ctx.Done():
			q := quality()
			for _, n := range inputs {
				if !read[n] {
					p.Bad = append(p.Bad, fmt.Sprintf("%s (%s)", n, q[n]))
				}
			}
			sort.Strings(p.Bad)
			if !everConnected {
				refused()
				return p
			}
			p.Detail = fmt.Sprintf("the device at %s answered, but %d of %d tags did not read", tg.addr, len(p.Bad), len(inputs))
			if lastErr != "" {
				p.Detail += " (" + lastErr + ")"
			}
			return p
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// liveCheck folds the probes into the validation's live check.
func liveCheck(probes []IOProbe) IOCheck {
	ck := IOCheck{Name: "live", OK: true}
	live := 0
	var problems, static []string
	for _, p := range probes {
		what := "driver " + p.Driver
		if p.Device != "" {
			what = "device " + p.Device
		}
		if p.Mode != devbind.ValidateLive {
			static = append(static, fmt.Sprintf("%s: static (%s)", what, p.Why))
			continue
		}
		live++
		if !p.OK {
			ck.OK = false
			msg := what + ": " + p.Detail
			if len(p.Bad) > 0 {
				msg += ": " + strings.Join(p.Bad, ", ")
			}
			problems = append(problems, msg)
		}
	}
	switch {
	case len(problems) > 0:
		ck.Detail = strings.Join(problems, "; ")
	case live == 0:
		ck.Skipped = true
		ck.Detail = "not probed: " + strings.Join(static, "; ")
	case len(static) > 0:
		ck.Detail = "the rest not probed: " + strings.Join(static, "; ")
	}
	return ck
}
