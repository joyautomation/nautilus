package virtdev

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/devprofile"
	uaserver "github.com/joyautomation/nautilus/opcua/server"
	"github.com/joyautomation/nautilus/runtime"
)

// opcuaSweep is how often an OPC UA device's server looks for changes to
// tell subscribers: fast enough for a 100 ms subscription.
const opcuaSweep = 20 * time.Millisecond

// Mapped is the address space an OPC UA instance serves: the profile's
// tree, with the instance's params substituted, each Variable bound to the
// instance's plant tag. Writable lists the tags a client may write.
func (s *Set) Mapped(d Instance) (m *uaserver.Mapped, writable []string, err error) {
	p := s.Profile(d.Name)
	if p == nil || p.Protocol != devprofile.ProtoOPCUA {
		return nil, nil, fmt.Errorf("device %s is not an OPC UA device", d.Name)
	}
	subst := func(s string) string { return p.Substitute(s, d.Params) }
	objNS := p.OPCUA.ObjectNamespace
	if objNS == 0 {
		objNS = 1
	}
	objects := map[string]uaserver.MappedNode{}
	for _, o := range p.OPCUA.Objects {
		path := subst(o.Path)
		objects[path] = uaserver.MappedNode{NodeID: subst(o.NodeID), Desc: o.Desc}
	}
	m = &uaserver.Mapped{
		Namespaces: p.OPCUA.Namespaces,
		Product:    &uaserver.Product{Manufacturer: p.Maker, Name: p.Model, URI: "urn:virtual:" + p.ID},
	}
	placed := map[string]string{} // path -> node id of an Object already placed
	var ensure func(path string) (string, error)
	ensure = func(path string) (string, error) {
		if id, ok := placed[path]; ok {
			return id, nil
		}
		parent := ""
		name := path
		if i := strings.LastIndex(path, "/"); i >= 0 {
			pid, err := ensure(path[:i])
			if err != nil {
				return "", err
			}
			parent, name = pid, path[i+1:]
		}
		n, ok := objects[path]
		if !ok {
			n = uaserver.MappedNode{NodeID: "ns=" + strconv.Itoa(objNS) + ";s=" + path}
		}
		n.Parent, n.Name = parent, name
		m.Nodes = append(m.Nodes, n)
		placed[path] = n.NodeID
		return n.NodeID, nil
	}
	for _, pt := range p.Points {
		path := subst(pt.Path)
		parent, name := "", path
		if i := strings.LastIndex(path, "/"); i >= 0 {
			if parent, err = ensure(path[:i]); err != nil {
				return nil, nil, err
			}
			name = path[i+1:]
		}
		tag := d.Name + "_" + pt.Name
		m.Nodes = append(m.Nodes, uaserver.MappedNode{
			NodeID: subst(pt.NodeID), Parent: parent, Name: name, Desc: pt.Desc, Tag: tag,
			// Octets on the wire, hex in the tag: as a ByteString or Byte[],
			// as the profile says (opcua/bytes.go).
			Wire: pt.Wire,
		})
		if pt.Writable {
			writable = append(writable, tag)
		}
	}
	return m, writable, nil
}

// opcuaPKI is an instance's certificate store, relative to the project;
// "" serves None alone.
func opcuaPKI(s *Set, d Instance) string {
	if d.OPCUA == nil || d.OPCUA.PKI == "" {
		return ""
	}
	if filepath.IsAbs(d.OPCUA.PKI) {
		return d.OPCUA.PKI
	}
	return filepath.Join(s.Dir, d.OPCUA.PKI)
}

// opcuaListen is the host:port an OPC UA instance's wire face binds:
// Listen, else an ephemeral loopback port.
func opcuaListen(d Instance) string {
	if d.Listen != "" {
		return d.Listen
	}
	return "127.0.0.1:0"
}

// opcuaEndpoint normalises an instance's Endpoint to opc.tcp://host:port,
// applying the profile's port; "" when it declares none.
func opcuaEndpoint(d Instance, p *devprofile.Profile) (string, error) {
	if d.Endpoint == "" {
		return "", nil
	}
	return p.OPCUAEndpoint(d.Endpoint)
}

// startOPCUA brings one OPC UA instance's server up over the plant.
func startOPCUA(ctx context.Context, rt *runtime.Runtime, s *Set, d Instance, log *slog.Logger) (*uaserver.Server, error) {
	m, writable, err := s.Mapped(d)
	if err != nil {
		return nil, err
	}
	srv, err := uaserver.New(rt, uaserver.Options{
		Endpoint: opcuaListen(d),
		Name:     d.Name,
		Sweep:    opcuaSweep,
		Mapped:   m,
		Writable: writable,
		// A virtual device takes writes the way the real ones do in their
		// default setup: anonymously, on the None endpoint.
		AnonymousWrites: len(writable) > 0,
		PKI:             opcuaPKI(s, d),
		Logger:          log,
	})
	if err != nil {
		return nil, fmt.Errorf("device %s: %w", d.Name, err)
	}
	if err := srv.Start(ctx); err != nil {
		return nil, fmt.Errorf("device %s: %w", d.Name, err)
	}
	return srv, nil
}
