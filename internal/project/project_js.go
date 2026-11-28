//go:build js

// The browser playground's half of the manifest loader (project_full.go is
// the native one). A browser tab has no network, so every field driver is
// replaced by a simulated device, and the servers that would open a port
// (OPC UA, Sparkplug) are left off. Nothing is refused: the project's logic,
// tags, alarms and tests all run, and Project.Notices says what changed.

package project

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/wirebind"
)

// simulated stands in for a field driver: a loopback that is always
// online. The first one in a project claims its declared inputs, so they
// read their init: until something writes them (a comms input, the way a
// real driver's input reads before first delivery) and every __Online
// companion reads TRUE: the simulated device is answering.
type simulated struct {
	*nio.Memory
	typ    string
	inputs []string
}

func (s *simulated) InputNames() []string { return s.inputs }

func checkNetworkSections(Manifest) error { return nil }

func checkDriverOutputs([]runtime.TagDef, nio.Driver) error { return nil }

func markReplicas(nio.Driver, bool) {}

// offlineChannel stands in for a channel in the browser: a tab reaches no
// peer, so every request fails as unreachable (STATUS -2), the way a
// channel whose peer is down does.
type offlineChannel struct{}

func (offlineChannel) Read(context.Context, string, ir.Value, string) (ir.Value, error) {
	return ir.Value{}, errOffline
}
func (offlineChannel) Write(context.Context, string, ir.Value, string) error { return errOffline }
func (offlineChannel) Close() error                                          { return nil }

var errOffline = errors.New("channels do not reach a peer in the browser")

func buildChannel(ChannelConfig) (runtime.ChannelIO, error) { return offlineChannel{}, nil }

func buildDriver(_ fs.FS, d DriverConfig) (nio.Driver, error) {
	if err := statusTagsOnlyOnRuntime(d); err != nil {
		return nil, err
	}
	switch strings.ToLower(d.Type) {
	case "", "memory":
		return nio.NewMemory(), nil
	case "runtime": // the runtime's own health: real in the browser too
		return newRuntimeStatus(d)
	}
	return &simulated{Memory: nio.NewMemory(), typ: strings.ToLower(d.Type)}, nil
}

func driverHealth(nio.Driver) func() bool { return nil }

func commsDriver(d nio.Driver) bool {
	_, ok := d.(*simulated)
	return ok
}

func simulateDrivers(defs []runtime.TagDef, d nio.Driver) {
	var first *simulated
	var find func(nio.Driver)
	find = func(d nio.Driver) {
		if first != nil {
			return
		}
		if m, ok := d.(*nio.Multi); ok {
			for _, c := range m.Children() {
				find(c.Driver)
			}
			return
		}
		if s, ok := d.(*simulated); ok {
			first = s
		}
	}
	find(d)
	if first == nil {
		return
	}
	online := nio.Values{}
	for _, def := range defs {
		if def.Role != runtime.RoleInput {
			continue
		}
		first.inputs = append(first.inputs, def.Name)
		if strings.HasSuffix(def.Name, "__Online") {
			online[def.Name] = true
		}
	}
	_ = first.WriteOutputs(online)
}

func platformNotices(m Manifest) []string {
	var out []string
	configs := m.Drivers
	if m.Driver.Type != "" {
		configs = append([]DriverConfig{m.Driver}, configs...)
	}
	for _, c := range configs {
		t := strings.ToLower(c.Type)
		if t == "" || t == "memory" {
			continue
		}
		name := c.Name
		if name == "" {
			name = t
		}
		out = append(out, fmt.Sprintf("driver %s (%s) is simulated: its inputs hold their init: values and its devices read as online. Install naut to run it against real equipment.", name, t))
	}
	for _, c := range m.Channels {
		out = append(out, fmt.Sprintf("channel %s (%s) reaches no peer in the browser: its READ and WRITE blocks report ERROR with STATUS -2. Install naut to run them against real equipment.", c.Name, strings.ToLower(c.Type)))
	}
	if m.wired != nil && len(m.wired.Instances) > 0 {
		out = append(out, "instances: their Modbus and OPC UA connections are simulated, so a device member reads as not connected until a test gives it, and each member's address (a Modbus register, an OPC UA node) is checked by naut check, not here. Install naut to run them against real equipment.")
	}
	if m.OPCUA != nil {
		out = append(out, "opcua: the OPC UA server is off in the browser (nothing can connect to a tab). Install naut to serve this project's tags over OPC UA.")
	}
	if m.Sparkplug != nil {
		out = append(out, "sparkplug: Sparkplug B publishing is off in the browser (there is no MQTT broker to reach). Install naut to publish this project's tags.")
	}
	return out
}

// oneSimulated: in the browser every field driver is simulated, and a
// simulated driver declares no outputs, so an io.Multi of them cannot route
// (it runs alone). When every driver of a set is simulated, one stands in
// for them all: the first, which claims every declared input and reads
// every __Online TRUE (simulateDrivers), as a lone driver does.
func oneSimulated(named []nio.NamedDriver) (nio.Driver, bool) {
	for _, n := range named {
		if _, ok := n.Driver.(*simulated); !ok {
			return nil, false
		}
	}
	return named[0].Driver, len(named) > 0
}

// withWiredOPCUA: in the browser every driver is simulated and an
// address is checked by naut check, not here, so an instance's own OPC UA
// connection only needs a driver of its name for its inputs and companion.
func withWiredOPCUA(configs, _ []DriverConfig, set *wirebind.Set) ([]DriverConfig, error) {
	for _, in := range set.InstancesList() {
		for _, c := range in.Conns {
			if c.DriverType == "opcua-client" && !c.Shared {
				configs = append(configs, DriverConfig{Type: "opcua-client", Name: c.ID})
			}
		}
	}
	return configs, nil
}

// wiredWarnings: the playground simulates the instances' connections, so
// it has nothing to say about how they connect (naut check does).
func wiredWarnings(*Manifest) []string { return nil }

func checkTakeoverHold(*TakeoverHoldConfig, []TagConfig, []nio.NamedDriver) error { return nil }

// The playground runs no pair: redundancy.takeover-hold is checked by the
// controller's build only (takeoverhold.go).
func checkTakeoverHoldConfig(*TakeoverHoldConfig) error { return nil }
