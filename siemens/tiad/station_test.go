package tiad

import (
	"strings"
	"testing"
)

func intp(i int) *int { return &i }

// BenchStation is a valid station: a 1510SP with one module and one
// GSDML device. Addresses are from the documentation range.
func benchStation() Station {
	return Station{
		CPU: CPU{Name: "PLC_1", Order: "6ES7 510-1DK03-0AB0", Firmware: "V3.1",
			Address: "192.0.2.10", SubnetMask: "255.255.255.0", ProfinetName: "plc-1", ConfigurationProtection: ProtectNone},
		WebServer: &WebServer{Enabled: true, Users: []WebUser{{Name: "naut", Rights: []string{"read", "write", "mode"}}}},
		Modules:   []Module{{Slot: 2, Order: "6ES7 131-6BH01-0BA0", InputStart: intp(0)}},
		Profinet: &Profinet{UpdateTime: "16ms", Devices: []Device{{
			GSDML: "GSDML-V2.4-Example-Device-20261117.xml", DAP: "DAP_1",
			Name: "io-device-1", Address: "192.0.2.20",
			Modules: []DeviceModule{{Slot: 1, Subslot: 1, Module: "ID_Mod_In4Out2", InputStart: intp(100), OutputStart: intp(100)}},
		}}},
	}
}

func TestStationValid(t *testing.T) {
	if err := benchStation().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestStationRules(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Station)
		want string
	}{
		{"cpu address", func(s *Station) { s.CPU.Address = "plc.local" }, "cpu.address"},
		{"cpu ipv6", func(s *Station) { s.CPU.Address = "::1" }, "cpu.address"},
		{"cpu order", func(s *Station) { s.CPU.Order = "" }, "cpu.order is empty"},
		{"cpu station name upper", func(s *Station) { s.CPU.ProfinetName = "PLC_1" }, "lowercase"},
		{"device name empty", func(s *Station) { s.Profinet.Devices[0].Name = "" }, "always explicit"},
		{"device name ip-shaped", func(s *Station) { s.Profinet.Devices[0].Name = "1.2.3.4" }, "n.n.n.n"},
		{"device name port-NNN", func(s *Station) { s.Profinet.Devices[0].Name = "port-001" }, "port-xyz"},
		{"device name hyphen", func(s *Station) { s.Profinet.Devices[0].Name = "-dev" }, "hyphen"},
		{"device name long label", func(s *Station) { s.Profinet.Devices[0].Name = strings.Repeat("a", 64) }, "63"},
		{"device name dup of cpu", func(s *Station) { s.Profinet.Devices[0].Name = "plc-1" }, "already used"},
		{"device address dup of cpu", func(s *Station) { s.Profinet.Devices[0].Address = "192.0.2.10" }, "already used"},
		{"update time too short", func(s *Station) { s.Profinet.Devices[0].UpdateTime = "8ms" }, "below the 16ms minimum"},
		{"update time junk", func(s *Station) { s.Profinet.UpdateTime = "fast" }, "not a duration"},
		{"subslot twice", func(s *Station) {
			d := &s.Profinet.Devices[0]
			d.Modules = append(d.Modules, d.Modules[0])
		}, "subslot 1 twice"},
		{"module id empty", func(s *Station) { s.Profinet.Devices[0].Modules[0].Module = "" }, "module is empty"},
		{"local slot twice", func(s *Station) { s.Modules = append(s.Modules, s.Modules[0]) }, "slot 2 twice"},
		{"local slot 1 is the cpu", func(s *Station) { s.Modules[0].Slot = 1 }, "slot 1 is the CPU's own"},
		{"device name n.n.n.n beyond 255", func(s *Station) { s.Profinet.Devices[0].Name = "300.1.1.1" }, "n.n.n.n"},
		{"device name n.n.n.n leading zero", func(s *Station) { s.Profinet.Devices[0].Name = "01.2.3.4" }, "n.n.n.n"},
		{"device name port-xyz-abcde", func(s *Station) { s.Profinet.Devices[0].Name = "port-001-00002" }, "port-xyz"},
		{"negative start", func(s *Station) { s.Modules[0].InputStart = intp(-1) }, "negative"},
		{"unknown right", func(s *Station) { s.WebServer.Users[0].Rights = []string{"admin"} }, "unknown right"},
		{"gsdml empty", func(s *Station) { s.Profinet.Devices[0].GSDML = "" }, "gsdml is empty"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := benchStation()
			c.edit(&s)
			err := s.Validate()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Validate() = %v, want an error containing %q", err, c.want)
			}
		})
	}
}

func TestStationNamesAccepted(t *testing.T) {
	for _, n := range []string{"plc-1", "io-device-1.line2", "a", "port-x", "dev42", "port-1234", "port-001x", "1.2.3", "1.2.3.4.5", "1000.1.1.1", "dev.port-001"} {
		if err := checkStationName(n); err != nil {
			t.Errorf("%q: %v", n, err)
		}
	}
}
