package virtdev

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/joyautomation/nautilus/devprofile"
	"github.com/joyautomation/nautilus/modbus/slave"
	uaserver "github.com/joyautomation/nautilus/opcua/server"
	"github.com/joyautomation/nautilus/runtime"
)

// Endpoint is where one virtual device answers on its wire face.
type Endpoint struct {
	Device   string
	Profile  string
	Protocol string
	// Endpoint is the address the project's driver dials for the real
	// device, as virtual-devices.yaml declares it ("" if it declares none).
	Endpoint string
	// Listen is the address the virtual device is actually bound to.
	Listen string
	// Unit is the Modbus unit id.
	Unit uint8
	// MaxConnections is how many connections the wire face serves at
	// once, the profile's documented limit (the smallest of the devices
	// sharing a listener); 0 is no limit.
	MaxConnections int
}

// Running is a set of virtual devices on their wire faces, with the plant
// running in wall time.
type Running struct {
	Plant     *Plant
	Runtime   *runtime.Runtime
	Endpoints []Endpoint

	redirect map[string]string
	servers  []*slave.Server
	byDevice map[string]*slave.Server // lower-cased device -> its Modbus listener
	ua       []*uaserver.Server
	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once
}

// StartOptions tune Start.
type StartOptions struct {
	Log *slog.Logger
}

// Start brings every device of the set up on its wire face and runs the
// plant in wall time until Stop or ctx ends.
//
// Modbus devices that declare the same endpoint (several unit ids behind
// one gateway address) share one listener, as they share one address in
// the field; so do devices that declare the same listen address.
func Start(ctx context.Context, s *Set, o StartOptions) (*Running, error) {
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	plant, err := NewPlant(s)
	if err != nil {
		return nil, err
	}
	rt, err := runtime.New(plant.Options())
	if err != nil {
		return nil, fmt.Errorf("plant: %w", err)
	}
	r := &Running{Plant: plant, Runtime: rt, redirect: map[string]string{}, byDevice: map[string]*slave.Server{}}

	// Group the Modbus devices onto listeners.
	type group struct {
		listen string
		srv    *slave.Server
		devs   []int
	}
	var groups []*group
	byKey := map[string]*group{}
	for i, d := range s.Devices {
		p := s.Profile(d.Name)
		if p.Protocol != devprofile.ProtoModbus {
			continue
		}
		ep, _ := d.modbusEndpoint(p)
		key := "dev:" + strings.ToLower(d.Name)
		switch {
		case d.Listen != "":
			key = "listen:" + d.Listen
		case ep != "":
			key = "ep:" + ep
		}
		g := byKey[key]
		if g == nil {
			g = &group{listen: d.Listen}
			byKey[key] = g
			groups = append(groups, g)
		}
		if d.Listen != "" && g.listen != d.Listen {
			r.stopServers()
			return nil, fmt.Errorf("devices behind %s listen on both %s and %s", ep, g.listen, d.Listen)
		}
		g.devs = append(g.devs, i)
	}
	for _, g := range groups {
		g.srv = slave.NewServer(g.listen, log)
		// A gateway's connection table is the smallest its devices
		// document: past it, a connection is refused, as on the device.
		limit := 0
		for _, i := range g.devs {
			if m := s.Profile(s.Devices[i].Name).Modbus; m != nil && m.MaxConnections > 0 && (limit == 0 || m.MaxConnections < limit) {
				limit = m.MaxConnections
			}
		}
		g.srv.SetMaxConnections(limit)
		units := map[uint8]string{}
		for _, i := range g.devs {
			d := s.Devices[i]
			p := s.Profile(d.Name)
			u := d.Unit(p)
			if prev, ok := units[u]; ok {
				r.stopServers()
				return nil, fmt.Errorf("devices %s and %s share a listener and unit id %d", prev, d.Name, u)
			}
			units[u] = d.Name
			g.srv.SetUnit(u, plant.stores[strings.ToLower(d.Name)].(*modbusStore).unit)
		}
		if err := g.srv.Start(); err != nil {
			r.stopServers()
			return nil, err
		}
		r.servers = append(r.servers, g.srv)
		for _, i := range g.devs {
			d := s.Devices[i]
			p := s.Profile(d.Name)
			ep, _ := d.modbusEndpoint(p)
			r.Endpoints = append(r.Endpoints, Endpoint{
				Device: d.Name, Profile: p.ID, Protocol: p.Protocol,
				Endpoint: ep, Listen: g.srv.Addr(), Unit: d.Unit(p), MaxConnections: limit,
			})
			r.byDevice[strings.ToLower(d.Name)] = g.srv
			if ep != "" {
				r.redirect[ep] = g.srv.Addr()
			}
		}
	}
	rctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel

	// One OPC UA server per OPC UA device, each over the plant.
	for _, d := range s.Devices {
		p := s.Profile(d.Name)
		if p.Protocol != devprofile.ProtoOPCUA {
			continue
		}
		srv, err := startOPCUA(rctx, rt, s, d, log)
		if err != nil {
			cancel()
			r.stopServers()
			return nil, err
		}
		r.ua = append(r.ua, srv)
		ep, _ := opcuaEndpoint(d, p)
		r.Endpoints = append(r.Endpoints, Endpoint{
			Device: d.Name, Profile: p.ID, Protocol: p.Protocol, Endpoint: ep, Listen: srv.URL(),
		})
		if ep != "" {
			r.redirect[ep] = srv.URL()
		}
	}
	sort.SliceStable(r.Endpoints, func(i, j int) bool { return r.Endpoints[i].Device < r.Endpoints[j].Device })

	r.done = make(chan struct{})
	go func() {
		defer close(r.done)
		rt.Run(rctx)
	}()
	return r, nil
}

// Redirect maps an address the project's driver dials to the virtual
// device answering for it. ok is false for an address no device declared.
func (r *Running) Redirect(addr string) (string, bool) {
	to, ok := r.redirect[addr]
	return to, ok
}

// Refused is how many connections a Modbus device's wire face has closed
// for want of a slot (its profile's max-connections); 0 for a device
// without a limit, an OPC UA device, or a name the set does not have.
func (r *Running) Refused(device string) int {
	if srv := r.byDevice[strings.ToLower(device)]; srv != nil {
		return srv.Refused()
	}
	return 0
}

// Stop stops the plant and closes every listener.
func (r *Running) Stop() {
	r.stopOnce.Do(func() {
		if r.cancel != nil {
			r.cancel()
			<-r.done
		}
		r.stopServers()
	})
}

func (r *Running) stopServers() {
	for _, s := range r.servers {
		s.Stop()
	}
	r.servers = nil
	for _, s := range r.ua {
		s.Stop()
	}
	r.ua = nil
}
