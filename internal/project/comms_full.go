//go:build !js

package project

import (
	"github.com/joyautomation/nautilus/eip"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/modbus"
	"github.com/joyautomation/nautilus/opcua"
	"github.com/joyautomation/nautilus/optommp"
	"github.com/joyautomation/nautilus/plcnext"
	"github.com/joyautomation/nautilus/prom"
	"github.com/joyautomation/nautilus/redfish"
	"github.com/joyautomation/nautilus/replay"
	"github.com/joyautomation/nautilus/snmp"
	sphost "github.com/joyautomation/nautilus/sparkplug/host"
)

// commsDriver reports whether d is a communications driver — one whose
// inputs come from a device on the far end of a link that can be down.
// Unknown driver types (a Go-tier io.Driver, which never comes through the
// manifest loader) are not.
func commsDriver(d nio.Driver) bool {
	switch d.(type) {
	case *modbus.Driver, *eip.Driver, *sphost.Driver, *opcua.Driver,
		*snmp.Driver, *redfish.Driver, *prom.Driver:
		return true
	case *plcnext.Driver, *optommp.Driver, *replay.Driver, *nio.Memory:
		return false
	}
	return false
}
