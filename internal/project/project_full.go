//go:build !js

// The protocol half of the manifest loader: every field driver, the
// Sparkplug edge node and the OPC UA server. The js/wasm build (the
// browser playground) has no network, so it swaps this file for
// project_js.go and links none of these packages.

package project

import (
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/joyautomation/nautilus/eip"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/modbus"
	"github.com/joyautomation/nautilus/opcua"
	opcuaserver "github.com/joyautomation/nautilus/opcua/server"
	"github.com/joyautomation/nautilus/optommp"
	"github.com/joyautomation/nautilus/plcnext"
	"github.com/joyautomation/nautilus/prom"
	"github.com/joyautomation/nautilus/redfish"
	"github.com/joyautomation/nautilus/replay"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/snmp"
	"github.com/joyautomation/nautilus/sparkplug"
	sphost "github.com/joyautomation/nautilus/sparkplug/host"
)

// checkNetworkSections validates the server sections that open sockets,
// offline, so naut check reports them before anything listens.
func checkNetworkSections(m Manifest) error {
	if m.OPCUA != nil && m.OPCUA.Endpoint != "" {
		if _, _, err := opcuaserver.ParseEndpoint(m.OPCUA.Endpoint); err != nil {
			return err
		}
	}
	if m.OPCUA != nil && m.OPCUA.ApplicationURI != "" {
		if err := opcuaserver.CheckApplicationURI(m.OPCUA.ApplicationURI); err != nil {
			return err
		}
	}
	return nil
}

func (p *Project) Sparkplug(rt *runtime.Runtime) (*sparkplug.Node, error) {
	c := p.sparkplug
	if c == nil {
		return nil, nil
	}
	if c.Broker == "" || c.GroupID == "" {
		return nil, fmt.Errorf("sparkplug: broker and group-id are required")
	}
	edge := c.EdgeNode
	if edge == "" {
		edge = p.Name
	}
	cfg := sparkplug.Config{
		BrokerURL:       c.Broker,
		GroupID:         c.GroupID,
		EdgeNode:        edge,
		ClientID:        c.ClientID,
		Username:        c.Username,
		Password:        os.Getenv("NAUTILUS_MQTT_PASSWORD"),
		PrimaryHostID:   c.PrimaryHost,
		PublishInterval: time.Duration(c.PublishInterval),
		BdSeqFile:       c.BdSeqFile,
	}
	if c.PrimaryHostOffline == "disconnect" { // Load refused anything but buffer/disconnect
		cfg.PrimaryHostOffline = sparkplug.HostOfflineDisconnect
	}
	rbe := func(r RBEConfig) sparkplug.RBE {
		return sparkplug.RBE{
			Deadband:    r.Deadband,
			MinInterval: time.Duration(r.MinInterval),
			MaxInterval: time.Duration(r.MaxInterval),
			Disable:     r.EveryChange,
		}
	}
	var opts []sparkplug.Option
	if c.StoreForward > 0 {
		opts = append(opts, sparkplug.WithStoreForward(c.StoreForward))
		if c.StoreForwardFile != "" { // Load refused it without store-forward
			opts = append(opts, sparkplug.WithStoreForwardFile(c.StoreForwardFile))
		}
	}
	if c.FlattenUDTs {
		opts = append(opts, sparkplug.WithFlattenUDTs())
	}
	if c.DefaultClass != nil {
		opts = append(opts, sparkplug.WithDefaultRBE(rbe(*c.DefaultClass)))
	}
	for name, r := range c.Classes {
		opts = append(opts, sparkplug.WithPublishClass(name, rbe(r)))
	}
	for class, patterns := range c.MetricClasses {
		opts = append(opts, sparkplug.WithMetricClass(class, patterns...))
	}
	devices, err := sparkplugDevices(p.drivers)
	if err != nil {
		return nil, err
	}
	for _, d := range declaredOnly(devices, declaredNames(p.Runtime, rt)) {
		opts = append(opts, sparkplug.WithDevice(d))
	}
	opts = append(opts, sparkplug.WithWritable(c.Writable...))
	return sparkplug.New(rt, cfg, opts...)
}

// sparkplugDevices makes each field connection a Sparkplug device
// (edge-collector.md §3): one per source of a driver that holds several
// (modbus, snmp, redfish, prometheus), named by the source id, and one per
// driver that is a single connection (eip, sparkplug-host, replay), named
// by the driver. Each births and dies with its own connection, so one dead
// meter never takes another device's DBIRTH with it. The memory loopback
// is no connection and makes no device; its tags, like every tag no
// connection delivers (program state, outputs), publish at node level.
func sparkplugDevices(drivers []nio.NamedDriver) ([]sparkplug.Device, error) {
	var out []sparkplug.Device
	from := map[string]string{} // device id → where it came from, for a collision
	add := func(d sparkplug.Device, origin string) error {
		if prev, dup := from[d.ID]; dup {
			return fmt.Errorf("sparkplug: device %q is both %s and %s — each connection is its own Sparkplug device, so rename one", d.ID, prev, origin)
		}
		from[d.ID] = origin
		out = append(out, d)
		return nil
	}
	for _, nd := range drivers {
		switch drv := nd.Driver.(type) {
		case nio.ConnectionReporter:
			for _, c := range drv.Connections() {
				id := c.ID
				err := add(sparkplug.Device{
					ID:     id,
					Tags:   c.Inputs,
					Health: func() bool { return drv.ConnectionUp(id) },
				}, fmt.Sprintf("source %q of driver %q", id, nd.Name))
				if err != nil {
					return nil, err
				}
			}
		case interface{ InputNames() []string }:
			err := add(sparkplug.Device{
				ID:     nd.Name,
				Tags:   drv.InputNames(),
				Health: driverHealth(nd.Driver),
			}, fmt.Sprintf("driver %q", nd.Name))
			if err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// declaredOnly keeps, of each device's tags, the ones the project declares
// in any role. The runtime never puts an undeclared driver input in the tag
// store, so an undeclared tag (a driver companion such as
// <source>__Outages that no tag file names) would never arrive: the device
// would wait out its birth delay for it and be born without it anyway. Left
// out, it neither delays the DBIRTH nor appears in it. A declared tag of
// any role stays, so a polled writable binding declared role: output keeps
// riding its device.
//
// A device that had tags and has none declared is dropped, as it was before
// the filter: its tags could never arrive, so it was never born. A device
// that delivers no tags at all is kept (it births on health alone).
func declaredOnly(devs []sparkplug.Device, declared map[string]bool) []sparkplug.Device {
	out := make([]sparkplug.Device, 0, len(devs))
	for _, d := range devs {
		if len(d.Tags) == 0 {
			out = append(out, d)
			continue
		}
		var tags []string
		for _, name := range d.Tags {
			if declared[name] {
				tags = append(tags, name)
			}
		}
		if len(tags) == 0 {
			continue
		}
		d.Tags = tags
		out = append(out, d)
	}
	return out
}

// driverHealth adapts a field driver's connection state onto the bool a
// Sparkplug device gates DBIRTH/DDEATH with, for a driver that is one
// connection as a whole. nil means the driver has no notion of a
// connection (replay) and its device is always healthy.
//
// eip is healthy once connected AND primed: its snapshot fills tag by tag
// during a poll, and on a controller where the first poll is slow (struct
// roots refused, read member by member) a DBIRTH sent as soon as the link
// came up carried only the tags read so far, and the rest rebirthed the node.
func driverHealth(d nio.Driver) func() bool {
	switch drv := d.(type) {
	case *eip.Driver:
		return func() bool { h := drv.Health(); return h.Connected && h.Primed }
	case *sphost.Driver:
		return func() bool { return drv.Status().Connected }
	case *opcua.Driver:
		return drv.Online
	}
	return nil
}

func buildDriver(fsys fs.FS, d DriverConfig) (nio.Driver, error) {
	switch strings.ToLower(d.Type) {
	case "", "memory":
		// Loopback for bring-up: inputs read back whatever was written,
		// so logic and HMI run before any field hardware exists.
		return nio.NewMemory(), nil
	case "eip":
		if d.Host == "" {
			return nil, fmt.Errorf("driver eip: host is required")
		}
		if d.Manifest == "" {
			return nil, fmt.Errorf("driver eip: manifest (the imported tag manifest .yaml) is required")
		}
		raw, err := fs.ReadFile(fsys, path.Clean(d.Manifest))
		if err != nil {
			return nil, fmt.Errorf("driver eip: %w", err)
		}
		var em eip.Manifest
		dec := yaml.NewDecoder(strings.NewReader(string(raw)))
		dec.KnownFields(true)
		if err := dec.Decode(&em); err != nil {
			return nil, fmt.Errorf("driver eip: %s: %w", d.Manifest, err)
		}
		// The controller's connection status, as a BOOL input the logic
		// checks before trusting its values: "<driver name>__Online" —
		// eip__Online for an unnamed driver: section.
		opts := []eip.Option{eip.WithSlot(d.Slot), eip.WithOnlineTag(onlineTagFor(d.Name, "eip"))}
		// "host:port" reaches a controller (or `naut logix emulate`) off
		// the standard 44818 — a bare host keeps the default.
		host := d.Host
		if h, p, err := net.SplitHostPort(host); err == nil {
			port, err := strconv.Atoi(p)
			if err != nil || port <= 0 || port > 65535 {
				return nil, fmt.Errorf("driver eip: host %q: bad port %q", d.Host, p)
			}
			host = h
			opts = append(opts, eip.WithPort(port))
		}
		if d.ScanRate != 0 {
			opts = append(opts, eip.WithScanRate(time.Duration(d.ScanRate)))
		}
		for name, rate := range d.ScanClasses {
			opts = append(opts, eip.WithScanClass(name, time.Duration(rate)))
		}
		for class, patterns := range d.TagClasses {
			opts = append(opts, eip.WithTagClass(class, patterns...))
		}
		return eip.New(host, em, opts...)
	case "sparkplug-host":
		// A Sparkplug B host application: consume a whole group of edge
		// nodes as INPUT tags, send operator writes back as NCMD/DCMD.
		//
		// host.New NEVER dials — buildDriver runs inside `naut check`
		// and `naut build`, i.e. in CI with no broker — so everything
		// that can fail on bad configuration fails here, offline, and the
		// connection is Start's job. Same split as eip.
		if d.Broker == "" {
			return nil, fmt.Errorf("driver sparkplug-host: broker is required (tcp://host:1883)")
		}
		if d.HostID == "" {
			return nil, fmt.Errorf("driver sparkplug-host: host-id is required (the STATE topic spBv1.0/STATE/<host-id>)")
		}
		if d.Manifest == "" {
			return nil, fmt.Errorf("driver sparkplug-host: manifest (the imported sparkplug manifest .yaml) is required")
		}
		// A host that subscribed to every group would consume other
		// projects' traffic silently; name the group you mean.
		if d.GroupID == "" && len(d.GroupIDs) == 0 {
			return nil, fmt.Errorf("driver sparkplug-host: group-id (or group-ids) is required")
		}
		raw, err := fs.ReadFile(fsys, path.Clean(d.Manifest))
		if err != nil {
			return nil, fmt.Errorf("driver sparkplug-host: %w", err)
		}
		// ParseManifest decodes with KnownFields(true), so a typo is an
		// error rather than a silently dropped binding.
		hm, err := sphost.ParseManifest(raw)
		if err != nil {
			return nil, fmt.Errorf("driver sparkplug-host: %s: %w", d.Manifest, err)
		}
		groups := d.GroupIDs
		if len(groups) == 0 {
			groups = []string{d.GroupID}
		}
		cfg := sphost.Config{
			BrokerURL: d.Broker,
			HostID:    d.HostID,
			GroupIDs:  groups,
			ClientID:  d.ClientID,
			Username:  d.Username,
			Password:  os.Getenv("NAUTILUS_MQTT_PASSWORD"),
			Keepalive: time.Duration(d.Keepalive),
			// primary:, rebirth-on-start: and rebirth-requests: all DEFAULT
			// TRUE, so they are pointers here — an absent key is "yes", and
			// only an explicit `false` opts out.
			Primary:           d.Primary == nil || *d.Primary,
			StateForm:         d.StateForm,
			ReorderTimeout:    time.Duration(d.ReorderTimeout),
			StaleAfter:        time.Duration(d.StaleAfter),
			NoRebirthOnStart:  d.RebirthOnStart != nil && !*d.RebirthOnStart,
			NoRebirthRequests: d.RebirthRequests != nil && !*d.RebirthRequests,
		}
		opts := []sphost.Option{sphost.WithLogger(slog.Default().With("driver", "sparkplug-host"))}
		if d.OnUnknown != "" {
			opts = append(opts, sphost.WithDiscovery(d.OnUnknown))
		}
		return sphost.New(hm, cfg, opts...)
	case "modbus":
		// Modbus TCP: every polled field device on a skid or site (brief
		// docs/design/modbus.md). modbus.New NEVER dials — it validates the
		// manifest and computes the block-read plan offline, so `nautilus
		// check` and `build` pass with no device in sight, the same split
		// eip and sparkplug-host make. The config keys are eip's, reused
		// wholesale: manifest, scan-rate, scan-classes, tag-classes.
		if d.Manifest == "" {
			return nil, fmt.Errorf("driver modbus: manifest (the imported modbus_manifest.yaml) is required")
		}
		raw, err := fs.ReadFile(fsys, path.Clean(d.Manifest))
		if err != nil {
			return nil, fmt.Errorf("driver modbus: %w", err)
		}
		// ParseManifest decodes with KnownFields semantics, so a typo is an
		// error rather than a silently dropped binding.
		mm, err := modbus.ParseManifest(raw)
		if err != nil {
			return nil, fmt.Errorf("driver modbus: %s: %w", d.Manifest, err)
		}
		opts := []modbus.Option{modbus.WithLogger(slog.Default().With("driver", "modbus"))}
		if d.ScanRate != 0 {
			opts = append(opts, modbus.WithScanRate(time.Duration(d.ScanRate)))
		}
		for name, rate := range d.ScanClasses {
			opts = append(opts, modbus.WithScanClass(name, time.Duration(rate)))
		}
		for class, patterns := range d.TagClasses {
			opts = append(opts, modbus.WithTagClass(class, patterns...))
		}
		return modbus.New(mm, opts...)
	case "snmp", "redfish", "prometheus":
		// The IT-hardware drivers (brief docs/design/it-drivers.md): a
		// switch, a PDU or a UPS over SNMP, a server BMC over Redfish, a
		// commodity host over node_exporter — all delivering the same UDT
		// set on hw.Base, and all wired exactly like modbus: a generated
		// manifest, New never dials, the eip keys reused as poll intervals.
		// Secrets never enter the manifest; an unset credential variable
		// is a `naut check` warning, not a load error, because check runs
		// on laptops that have no secrets.
		if d.Manifest == "" {
			return nil, fmt.Errorf("driver %s: manifest (the imported %s_manifest.yaml) is required", d.Type, d.Type)
		}
		raw, err := fs.ReadFile(fsys, path.Clean(d.Manifest))
		if err != nil {
			return nil, fmt.Errorf("driver %s: %w", d.Type, err)
		}
		log := slog.Default().With("driver", d.Type)
		switch d.Type {
		case "snmp":
			m, err := snmp.ParseManifest(raw)
			if err != nil {
				return nil, fmt.Errorf("driver snmp: %s: %w", d.Manifest, err)
			}
			opts := []snmp.Option{snmp.WithLogger(log)}
			if d.ScanRate != 0 {
				opts = append(opts, snmp.WithScanRate(time.Duration(d.ScanRate)))
			}
			for name, rate := range d.ScanClasses {
				opts = append(opts, snmp.WithScanClass(name, time.Duration(rate)))
			}
			for class, patterns := range d.TagClasses {
				opts = append(opts, snmp.WithTagClass(class, patterns...))
			}
			return snmp.New(m, opts...)
		case "redfish":
			m, err := redfish.ParseManifest(raw)
			if err != nil {
				return nil, fmt.Errorf("driver redfish: %s: %w", d.Manifest, err)
			}
			opts := []redfish.Option{redfish.WithLogger(log)}
			if d.ScanRate != 0 {
				opts = append(opts, redfish.WithScanRate(time.Duration(d.ScanRate)))
			}
			for name, rate := range d.ScanClasses {
				opts = append(opts, redfish.WithScanClass(name, time.Duration(rate)))
			}
			for class, patterns := range d.TagClasses {
				opts = append(opts, redfish.WithTagClass(class, patterns...))
			}
			return redfish.New(m, opts...)
		default:
			m, err := prom.ParseManifest(raw)
			if err != nil {
				return nil, fmt.Errorf("driver prometheus: %s: %w", d.Manifest, err)
			}
			opts := []prom.Option{prom.WithLogger(log)}
			if d.ScanRate != 0 {
				opts = append(opts, prom.WithScanRate(time.Duration(d.ScanRate)))
			}
			for name, rate := range d.ScanClasses {
				opts = append(opts, prom.WithScanClass(name, time.Duration(rate)))
			}
			for class, patterns := range d.TagClasses {
				opts = append(opts, prom.WithTagClass(class, patterns...))
			}
			return prom.New(m, opts...)
		}
	case "plcnext":
		// The runtime ON a Phoenix Contact PLCnext controller, driving its
		// local Axioline I/O through the firmware's GDS over a Unix socket
		// (brief docs/design/open-controllers.md). New never dials, so
		// check and build pass on the laptop that cross-compiles for the
		// controller; the socket only exists on the controller itself.
		if d.Manifest == "" {
			return nil, fmt.Errorf("driver plcnext: manifest (the plcnext_manifest.yaml) is required")
		}
		raw, err := fs.ReadFile(fsys, path.Clean(d.Manifest))
		if err != nil {
			return nil, fmt.Errorf("driver plcnext: %w", err)
		}
		m, err := plcnext.ParseManifest(raw)
		if err != nil {
			return nil, fmt.Errorf("driver plcnext: %s: %w", d.Manifest, err)
		}
		opts := []plcnext.Option{plcnext.WithLogger(slog.Default().With("driver", "plcnext"))}
		if d.ScanRate != 0 {
			opts = append(opts, plcnext.WithScanRate(time.Duration(d.ScanRate)))
		}
		return plcnext.New(m, opts...)
	case "optommp":
		// An Opto 22 groov EPIC / groov RIO chassis over OptoMMP, from the
		// processor's own LAN address or 127.0.0.1 when nautilus runs on
		// it (brief docs/design/open-controllers.md §2). New never dials.
		if d.Manifest == "" {
			return nil, fmt.Errorf("driver optommp: manifest (the optommp_manifest.yaml `naut optommp import` writes) is required")
		}
		raw, err := fs.ReadFile(fsys, path.Clean(d.Manifest))
		if err != nil {
			return nil, fmt.Errorf("driver optommp: %w", err)
		}
		m, err := optommp.ParseManifest(raw)
		if err != nil {
			return nil, fmt.Errorf("driver optommp: %s: %w", d.Manifest, err)
		}
		opts := []optommp.Option{optommp.WithLogger(slog.Default().With("driver", "optommp"))}
		if d.ScanRate != 0 {
			opts = append(opts, optommp.WithScanRate(time.Duration(d.ScanRate)))
		}
		return optommp.New(m, opts...)
	case "opcua-client":
		// The OPC UA client: one server, polled per scan class (brief
		// docs/design/opcua-client.md). opcua.New NEVER dials — it
		// validates the manifest and parses every node id offline, the same
		// split eip, modbus and sparkplug-host make.
		if d.Endpoint == "" {
			return nil, fmt.Errorf("driver opcua-client: endpoint is required (opc.tcp://host:4840)")
		}
		if d.Manifest == "" {
			return nil, fmt.Errorf("driver opcua-client: manifest (the opcua_manifest.yaml `naut opcua import` writes) is required")
		}
		raw, err := fs.ReadFile(fsys, path.Clean(d.Manifest))
		if err != nil {
			return nil, fmt.Errorf("driver opcua-client: %w", err)
		}
		om, err := opcua.ParseManifest(raw)
		if err != nil {
			return nil, fmt.Errorf("driver opcua-client: %s: %w", d.Manifest, err)
		}
		// The session's status, as a BOOL input the logic checks before
		// trusting its values: "<driver name>__Online" — opcua_client__Online for
		// an unnamed driver: section, the rule eip keeps.
		name := d.Name
		if name == "" {
			name = "opcua-client"
		}
		opts := []opcua.Option{
			opcua.WithLogger(slog.Default().With("driver", name)),
			opcua.WithName(name),
			opcua.WithOnlineTag(onlineTagFor(d.Name, "opcua-client")),
			opcua.WithSecurity(d.Security, d.SecurityMode),
			opcua.WithPKIDir(d.PKIDir),
		}
		if d.Username != "" {
			opts = append(opts, opcua.WithAuth(d.Username, os.Getenv("NAUTILUS_OPCUA_PASSWORD")))
		}
		if d.ScanRate != 0 {
			opts = append(opts, opcua.WithScanRate(time.Duration(d.ScanRate)))
		}
		for name, rate := range d.ScanClasses {
			opts = append(opts, opcua.WithScanClass(name, time.Duration(rate)))
		}
		for class, patterns := range d.TagClasses {
			opts = append(opts, opcua.WithTagClass(class, patterns...))
		}
		return opcua.New(d.Endpoint, om, opts...)
	case "replay":
		// A recording played back as live tags at a steerable clock: the
		// baseline of a plant simulation. The recording itself is read on
		// Start, so check and build pass where it is absent.
		if d.Manifest == "" {
			return nil, fmt.Errorf("driver replay: manifest (the replay_manifest.yaml) is required")
		}
		raw, err := fs.ReadFile(fsys, path.Clean(d.Manifest))
		if err != nil {
			return nil, fmt.Errorf("driver replay: %w", err)
		}
		m, err := replay.ParseManifest(raw)
		if err != nil {
			return nil, fmt.Errorf("driver replay: %s: %w", d.Manifest, err)
		}
		return replay.New(m, fsys, replay.WithLogger(slog.Default().With("driver", "replay")))
	default:
		return nil, fmt.Errorf("driver type %q: manifest projects support memory, eip, sparkplug-host, modbus, opcua-client, snmp, redfish, prometheus, plcnext, optommp and replay — custom buses are the Go tier (io.Driver)", d.Type)
	}
}

// OPCUA builds the manifest's OPC UA server over a compiled runtime, or
// (nil, nil) when the manifest has no opcua section. Nothing listens until
// Start.
func (p *Project) OPCUAServer(rt *runtime.Runtime) (*opcuaserver.Server, error) {
	c := p.OPCUA
	if c == nil {
		return nil, nil
	}
	pki := c.PKI
	if pki == "" {
		pki = opcuaserver.DefaultPKI
	}
	srv, err := opcuaserver.New(rt, opcuaserver.Options{Endpoint: c.Endpoint, Name: p.Name, ApplicationURI: c.ApplicationURI, PKI: pki, Writable: c.Writable, AnonymousWrites: c.AnonymousWrites})
	if err != nil {
		return nil, err
	}
	if p.WriteGate != nil {
		srv.SetWriteGate(p.WriteGate)
	}
	return srv, nil
}

// simulateDrivers and platformNotices are the browser playground's
// (project_js.go); a real controller runs the drivers it was given.
func simulateDrivers([]runtime.TagDef, nio.Driver) {}

func platformNotices(Manifest) []string { return nil }
