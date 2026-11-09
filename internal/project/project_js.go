//go:build js

// The browser playground's half of the manifest loader (project_full.go is
// the native one). A browser tab has no network, so every field driver is
// replaced by a simulated device, and the servers that would open a port
// (OPC UA, Sparkplug) are left off. Nothing is refused: the project's logic,
// tags, alarms and tests all run, and Project.Notices says what changed.

package project

import (
	"fmt"
	"io/fs"
	"strings"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
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

func buildDriver(_ fs.FS, d DriverConfig) (nio.Driver, error) {
	switch strings.ToLower(d.Type) {
	case "", "memory":
		return nio.NewMemory(), nil
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
	if m.OPCUA != nil {
		out = append(out, "opcua: the OPC UA server is off in the browser (nothing can connect to a tab). Install naut to serve this project's tags over OPC UA.")
	}
	if m.Sparkplug != nil {
		out = append(out, "sparkplug: Sparkplug B publishing is off in the browser (there is no MQTT broker to reach). Install naut to publish this project's tags.")
	}
	return out
}
