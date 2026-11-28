package project

import (
	"strings"
)

// OPC UA sources of struct wiring (#1458 slice 9a-1). A source with its own
// endpoint is a connection of the instance's own: an opcua-client driver
// named after the connection (Conn.ID, so its companion is
// <Instance>__Online), made from the drivers: section the source names as
// a template, exactly as a devices: row's driver: is (withDevices). A source
// with no endpoint shares the one session of the drivers: entry it names:
// its bindings join that driver's manifest, and its companion is that
// driver's own <name>__Online.

// sharedOPCUACompanion is a shared OPC UA source's companion: the
// <name>__Online of the driver it names (an entry by name, else the
// project's opcua-client entry), as that driver is built. Which entry is
// checked when the driver is built (withWiredOPCUA); this only names it.
func (m *Manifest) sharedOPCUACompanion(drv string) string {
	configs := m.Drivers
	if len(configs) == 0 && m.Driver.Type != "" {
		configs = []DriverConfig{m.Driver}
	}
	names := driverNames(configs)
	for _, n := range names {
		if strings.EqualFold(n, drv) {
			return onlineTagFor(n, "opcua-client")
		}
	}
	for i, c := range configs {
		if strings.EqualFold(c.Type, "opcua-client") {
			return onlineTagFor(names[i], "opcua-client")
		}
	}
	return onlineTagFor("", "opcua-client")
}
