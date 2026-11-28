//go:build !js

package project

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/joyautomation/nautilus/wirebind"
)

// wiredWarnings are what naut check says about the instances' OPC UA
// connections: one that connects with security None, and a from: node read
// by namespace index alone (wirebind). The security is read from the very
// configs the drivers are built from (driverConfigs), so the warning and
// the session cannot disagree. The binding errors themselves are the
// driver's build's (opcuaManifest), at Load.
func wiredWarnings(m *Manifest) []string {
	if m.wired == nil {
		return nil
	}
	conns, problems := m.wired.OPCUAConns()
	if len(problems) > 0 {
		return nil // Load refuses them when the driver is built
	}
	built := map[string]DriverConfig{}
	if cfgs, err := driverConfigs(m); err == nil { // else Load refuses it
		for _, c := range cfgs {
			if c.wiredConn != "" {
				built[strings.ToLower(c.wiredConn)] = c
			}
		}
	}
	var out []string
	for _, oc := range conns {
		out = append(out, oc.Warnings...)
		if oc.Conn.Shared {
			continue
		}
		d, ok := built[strings.ToLower(oc.Conn.ID)]
		switch {
		case !ok:
		case !strings.EqualFold(d.Security, "None") && !strings.EqualFold(d.SecurityMode, "None"):
		case d.wiredFrom == "":
			out = append(out, fmt.Sprintf("instance %s: source %s connects with security None, anonymous: it names no drivers: opcua-client section with its own security, so the session is neither signed nor encrypted even if the server offers more", oc.Instance, oc.Conn.Source))
		default:
			out = append(out, fmt.Sprintf("instance %s: source %s connects with security None, as its drivers: section %s sets", oc.Instance, oc.Conn.Source, d.wiredFrom))
		}
	}
	return out
}

// withWiredOPCUA hands the instances' OPC UA connections to their drivers:
// a driver per per-instance connection, and the shared ones' bindings to
// the entries they name (wired). A per-instance source's section is looked
// up in raw, the drivers: list as written, so one section may be the
// template of devices: rows and of instances alike; a section only
// instances use as a template is not built itself. The configs are copied,
// not changed.
func withWiredOPCUA(configs, raw []DriverConfig, set *wirebind.Set) ([]DriverConfig, error) {
	out := append([]DriverConfig(nil), configs...)
	names := driverNames(out)
	rawNames := driverNames(raw)
	taken := map[string]bool{}
	for _, n := range names {
		taken[strings.ToLower(n)] = true
	}
	template := map[string]bool{} // raw section names used as templates
	shared := map[string]bool{}   // built entries instances share
	var made []DriverConfig
	for _, in := range set.InstancesList() {
		for _, c := range in.Conns {
			if c.DriverType != "opcua-client" {
				continue
			}
			if c.Shared {
				at, err := opcuaEntry(out, names, c.Driver)
				if err != nil {
					return nil, fmt.Errorf("instance %s: source %s: %w", in.Name, c.Source, err)
				}
				if at < 0 {
					return nil, fmt.Errorf("instance %s: source %s gives no endpoint, so it shares the session of the drivers: entry it names, and the project has no opcua-client entry %s: add one with its endpoint and manifest, or give the source endpoint: for a session of the instance's own", in.Name, c.Source, c.Driver)
				}
				if out[at].Endpoint == "" {
					return nil, fmt.Errorf("instance %s: source %s gives no endpoint, so it shares the session of drivers: %s, which has none either: give the entry its endpoint, or the source one for a session of the instance's own", in.Name, c.Source, names[at])
				}
				shared[strings.ToLower(names[at])] = true
				out[at].wired = set
				out[at].wiredByType = out[at].wiredByType || strings.EqualFold(c.Driver, "opcua-client")
				continue
			}
			at, err := opcuaEntry(raw, rawNames, c.Driver)
			if err != nil {
				return nil, fmt.Errorf("instance %s: source %s: %w", in.Name, c.Source, err)
			}
			if taken[strings.ToLower(c.ID)] {
				return nil, fmt.Errorf("instance %s: source %s is a connection of the instance's own, whose driver is named after it, and the project already has a driver %s: rename one", in.Name, c.Source, c.ID)
			}
			taken[strings.ToLower(c.ID)] = true
			// With no section to make it from, the session is None: the
			// strongest offered would need a pki-dir a source cannot name
			// (naut check says so, as for a devices: row).
			d := DriverConfig{Type: "opcua-client", Security: "None", SecurityMode: "None"}
			if at >= 0 {
				if err := opcuaWiredTemplate(raw[at], rawNames[at]); err != nil {
					return nil, fmt.Errorf("instance %s: source %s: %w", in.Name, c.Source, err)
				}
				template[strings.ToLower(rawNames[at])] = true
				d = raw[at]
				d.wiredFrom = rawNames[at]
			}
			d.Name, d.Endpoint = c.ID, fmt.Sprint(c.Keys["endpoint"])
			d.wired, d.wiredConn = set, c.ID
			made = append(made, d)
		}
	}
	kept := out[:0:0]
	for i, c := range out {
		n := strings.ToLower(names[i])
		if !template[n] {
			kept = append(kept, c)
			continue
		}
		if shared[n] {
			return nil, fmt.Errorf("drivers: %s is a template for instances' own OPC UA sessions and a session instances share: give each its own entry", names[i])
		}
	}
	return append(kept, made...), nil
}

// opcuaWiredTemplate refuses a section an instance's own session cannot be
// made from: it may set only how the session connects (templateKeys); the
// source gives the endpoint and the wiring the bindings.
func opcuaWiredTemplate(c DriverConfig, name string) error {
	var set []string
	v := reflect.ValueOf(c)
	for i := 0; i < v.NumField(); i++ {
		f := v.Type().Field(i)
		key, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if f.PkgPath != "" || key == "" || key == "-" || key == "name" || templateKeys[key] || key == "reassert-on-reconnect" || v.Field(i).IsZero() {
			continue
		}
		set = append(set, key)
	}
	if len(set) > 0 {
		return fmt.Errorf("driver: %s is the section the instance's own session is made from, which may set only security, security-mode, pki-dir, username, read-mode, scan-rate and reassert-on-reconnect, not %s (the source gives the endpoint, the wiring the bindings)", name, strings.Join(set, ", "))
	}
	return nil
}

// opcuaEntry is the opcua-client entry a source's driver: names: by name,
// or by the type when the project has exactly one; -1 for the type with
// none (a per-instance source then connects with no template).
func opcuaEntry(configs []DriverConfig, names []string, drv string) (int, error) {
	for i, n := range names {
		if strings.EqualFold(n, drv) && configs[i].Name != "" {
			if !strings.EqualFold(configs[i].Type, "opcua-client") {
				return 0, fmt.Errorf("driver: %s is a %s driver; an OPC UA source names an opcua-client entry", drv, configs[i].Type)
			}
			return i, nil
		}
	}
	if !strings.EqualFold(drv, "opcua-client") {
		return 0, fmt.Errorf("driver: %s is not an opcua-client entry of drivers:", drv)
	}
	at := -1
	for i, c := range configs {
		if strings.EqualFold(c.Type, "opcua-client") && c.opcuaDevice == "" && c.wiredConn == "" {
			if at >= 0 {
				return 0, fmt.Errorf("driver: opcua-client, and the project has more than one opcua-client driver: name the drivers: entry instead")
			}
			at = i
		}
	}
	return at, nil
}
