package project

import (
	"github.com/joyautomation/nautilus/eip"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/modbus"
	"github.com/joyautomation/nautilus/optommp"
	"github.com/joyautomation/nautilus/plcnext"
	"github.com/joyautomation/nautilus/prom"
	"github.com/joyautomation/nautilus/redfish"
	"github.com/joyautomation/nautilus/replay"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/snmp"
	sphost "github.com/joyautomation/nautilus/sparkplug/host"
)

// Which inputs read as their init (or zero) until the first delivery.
//
// An input a driver has never delivered is absent from the tag store, and a
// program that reads it faults its scan with "undefined tag". For LOCAL I/O
// that is the right answer: a PLCnext's Axioline bus or a groov EPIC's own
// modules either deliver every channel or the configuration is wrong, and a
// loud fault is how you find out. For a COMMUNICATIONS driver it is the
// wrong one. A Modbus meter that is unplugged when the controller boots, a
// Logix partner that is down for a firmware update, a remote site whose
// edge node has not birthed — each of those would stop every program that
// reads one of its tags, every scan, for as long as it stays down, and the
// local interlocks in the same program would stop with them. No PLC behaves
// like that. A Logix controller keeps scanning with the consumed tag at its
// last (or initial) value, and the logic checks the connection's fault bit;
// an S7 does the same with the PROFINET device's status. That is the model
// here: a protocol input is seeded with its init: (or the zero of its type)
// and the logic checks the source's __Online companion.
//
// The decision is per TAG, by the driver that delivers it, because a
// drivers: set (io.Multi) can mix a Modbus skid with local I/O in one
// project.
//
//	driver           kind     an undelivered input…
//	───────────────  ───────  ─────────────────────────────────────────
//	modbus           comms    reads init/zero; <source>__Online
//	eip              comms    reads init/zero; <driver name>__Online
//	sparkplug-host   comms    reads init/zero; <site>__Online (+ device)
//	snmp             comms    reads init/zero; <source>__Online
//	redfish          comms    reads init/zero; <source>__Online
//	prometheus       comms    reads init/zero; <source>__Online
//	plcnext          local    faults the scan (unchanged)
//	optommp          local    faults the scan (unchanged)
//	replay           local    faults the scan (unchanged): a recording
//	                          missing a tag is a configuration error, and
//	                          there is no link to be down
//	memory           local    faults the scan (unchanged): a loopback
//	                          has no source to wait for
//
// optommp can reach a chassis over the LAN as well as from the processor
// itself, but its job is the controller's own I/O modules and it is
// classified by that job, not by its transport.

// commsDriver reports whether d is a communications driver — one whose
// inputs come from a device on the far end of a link that can be down.
// Unknown driver types (a Go-tier io.Driver, which never comes through the
// manifest loader) are not.
func commsDriver(d nio.Driver) bool {
	switch d.(type) {
	case *modbus.Driver, *eip.Driver, *sphost.Driver,
		*snmp.Driver, *redfish.Driver, *prom.Driver:
		return true
	case *plcnext.Driver, *optommp.Driver, *replay.Driver, *nio.Memory:
		return false
	}
	return false
}

// inputTyper is the optional driver method that names the IEC type a
// binding delivers — runtime.TagDef.DriverType, the last-resort seed type.
type inputTyper interface {
	InputType(name string) string
}

// markComms sets Comms (and DriverType, where the driver knows it) on every
// input the project's communications drivers deliver. A tag is matched to
// its driver by the driver's own InputNames — the same ownership io.Multi
// routes by — so a tag declared role: input that no driver claims stays
// strict: nothing will ever deliver it, and the fault says so.
func markComms(defs []runtime.TagDef, d nio.Driver) {
	type owner interface{ InputNames() []string }
	owned := map[string]nio.Driver{}
	var walk func(nio.Driver)
	walk = func(d nio.Driver) {
		if m, ok := d.(*nio.Multi); ok {
			for _, c := range m.Children() {
				walk(c.Driver)
			}
			return
		}
		if !commsDriver(d) {
			return
		}
		if o, ok := d.(owner); ok {
			for _, name := range o.InputNames() {
				owned[name] = d
			}
		}
	}
	walk(d)
	if len(owned) == 0 {
		return
	}
	for i := range defs {
		if defs[i].Role != runtime.RoleInput {
			continue
		}
		drv, ok := owned[defs[i].Name]
		if !ok {
			continue
		}
		defs[i].Comms = true
		if t, ok := drv.(inputTyper); ok {
			defs[i].DriverType = t.InputType(defs[i].Name)
		}
	}
}
