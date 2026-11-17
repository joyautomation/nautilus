package virtdev

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/joyautomation/nautilus/modbus/slave"
	"github.com/joyautomation/nautilus/runtime"
)

// Endpoint is where one virtual device answers on its wire face.
type Endpoint struct {
	Device   string
	Profile  string
	Protocol string
	// Endpoint is the address the project's driver dials for the real
	// device, as devices.yaml declares it ("" if it declares none).
	Endpoint string
	// Listen is the address the virtual device is actually bound to.
	Listen string
	// Unit is the Modbus unit id.
	Unit uint8
}

// Running is a set of virtual devices on their wire faces, with the plant
// running in wall time.
type Running struct {
	Plant     *Plant
	Runtime   *runtime.Runtime
	Endpoints []Endpoint

	redirect map[string]string
	servers  []*slave.Server
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
	r := &Running{Plant: plant, Runtime: rt, redirect: map[string]string{}}

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
		if p.Protocol != ProtoModbus {
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
				Endpoint: ep, Listen: g.srv.Addr(), Unit: d.Unit(p),
			})
			if ep != "" {
				r.redirect[ep] = g.srv.Addr()
			}
		}
	}
	sort.SliceStable(r.Endpoints, func(i, j int) bool { return r.Endpoints[i].Listen < r.Endpoints[j].Listen })

	rctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
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
}
