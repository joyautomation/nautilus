//go:build !js

package camera

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/ipv4"
)

// WS-Discovery (#724 slice 4c): `naut camera discover` asks the local network which
// ONVIF cameras are on it. It is a command a person runs on a machine on the cameras'
// network (the probe is multicast and does not cross routers); a controller never does
// it by itself, and nothing it finds is used until a person writes a manifest row.
//
// What answers is unauthenticated UDP from anyone on the network, so every answer is
// data: size-bounded, parsed without trusting its shape, cleaned of control
// characters (a name must not move a terminal's cursor or rewrite its title), at most
// MaxFound devices, and each address checked against where the answer came from.

// MaxPerSource bounds how many devices one address may answer for (a bridge or an
// NVR answering for its cameras is a few; a host sending hundreds is not a camera).
// A variable so a test can lift it to reach MaxFound from one loopback address.
var MaxPerSource = 16

const (
	// DiscoveryGroup is WS-Discovery's multicast address.
	DiscoveryGroup = "239.255.255.250:3702"
	// MaxFound bounds how many devices are kept (a flood of answers is not memory).
	MaxFound = 256
	// maxDatagram is the longest answer read; a real ProbeMatch is about a kilobyte.
	maxDatagram = 16 << 10
)

// Found is one device that answered.
type Found struct {
	// Endpoint is the device's own identifier (usually urn:uuid:...).
	Endpoint string `json:"endpoint"`
	// XAddrs are the device service addresses it gave: http or https, at most four.
	XAddrs []string `json:"xaddrs"`
	// Name and Hardware come from its ONVIF scopes, when it gave them.
	Name     string `json:"name"`
	Hardware string `json:"hardware"`
	// From is the address the answer came from.
	From string `json:"from"`
	// Elsewhere says an address it gave is not on the host it answered from: a camera
	// behind NAT or a bridge, or something pretending. A person looks before trusting it.
	Elsewhere bool `json:"elsewhere"`
}

// DiscoverOptions shapes a probe.
type DiscoverOptions struct {
	// Timeout is how long answers are waited for (default 3 s).
	Timeout time.Duration
	// Interface limits the probe to one network interface by name; empty is every
	// interface that is up and multicast.
	Interface string
	// Target replaces the multicast group: a test sends the probe to a unicast address.
	Target string
}

// Discover probes the network and returns what answered, sorted by address.
func Discover(ctx context.Context, o DiscoverOptions) ([]Found, error) {
	if o.Timeout <= 0 {
		o.Timeout = 3 * time.Second
	}
	if o.Timeout > time.Minute {
		o.Timeout = time.Minute
	}
	var conns []net.PacketConn
	var send []func() error
	probe := probeMessage()
	closeAll := func() {
		for _, c := range conns {
			c.Close()
		}
	}
	defer closeAll()
	if o.Target != "" {
		dst, err := net.ResolveUDPAddr("udp4", o.Target)
		if err != nil {
			return nil, err
		}
		c, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		conns = append(conns, c)
		send = append(send, func() error { _, err := c.WriteTo(probe, dst); return err })
	} else {
		ifs, err := net.Interfaces()
		if err != nil {
			return nil, err
		}
		group, _ := net.ResolveUDPAddr("udp4", DiscoveryGroup)
		var lastErr error
		for _, ifc := range ifs {
			ifc := ifc
			if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagMulticast == 0 || ifc.Flags&net.FlagLoopback != 0 {
				continue
			}
			if o.Interface != "" && ifc.Name != o.Interface {
				continue
			}
			ip := firstIPv4(&ifc)
			if ip == nil {
				continue
			}
			c, err := net.ListenPacket("udp4", ip.String()+":0")
			if err != nil {
				lastErr = err
				continue
			}
			pc := ipv4.NewPacketConn(c)
			if err := pc.SetMulticastInterface(&ifc); err != nil {
				c.Close()
				lastErr = err
				continue
			}
			_ = pc.SetMulticastTTL(1) // this network only
			_ = pc.SetMulticastLoopback(false)
			conns = append(conns, c)
			send = append(send, func() error { _, err := c.WriteTo(probe, group); return err })
		}
		if len(conns) == 0 {
			if lastErr != nil {
				return nil, fmt.Errorf("no interface could send the probe: %w", lastErr)
			}
			if o.Interface != "" {
				return nil, fmt.Errorf("no interface %q that is up, multicast and has an IPv4 address", o.Interface)
			}
			return nil, errors.New("no network interface that is up, multicast and has an IPv4 address")
		}
	}
	sent := 0
	for _, s := range send {
		if s() == nil {
			sent++
		}
	}
	if sent == 0 {
		return nil, errors.New("the probe could not be sent on any interface")
	}
	deadline := time.Now().Add(o.Timeout)
	type pkt struct {
		b    []byte
		from net.Addr
	}
	ch := make(chan pkt, 64)
	for _, c := range conns {
		c := c
		go func() {
			buf := make([]byte, maxDatagram)
			for {
				_ = c.SetReadDeadline(deadline)
				n, from, err := c.ReadFrom(buf)
				if err != nil {
					var ne net.Error
					if errors.Is(err, net.ErrClosed) || (errors.As(err, &ne) && ne.Timeout()) || time.Now().After(deadline) {
						return
					}
					continue // an oversized datagram or an ICMP reset (Windows reports both as errors): the next may be real
				}
				b := make([]byte, n)
				copy(b, buf[:n])
				select {
				case ch <- pkt{b, from}:
				default: // a flood faster than it is read: dropped
				}
			}
		}()
	}
	found := map[string]Found{}
	perSource := map[string]int{}
	timer := time.NewTimer(o.Timeout + 200*time.Millisecond)
	defer timer.Stop()
loop:
	for {
		select {
		case p := <-ch:
			if f, ok := parseProbeMatch(p.b, p.from); ok {
				// one host that answers for many devices cannot fill the table
				if _, dup := found[f.Endpoint]; !dup && len(found) < MaxFound && perSource[f.From] < MaxPerSource {
					found[f.Endpoint] = f
					perSource[f.From]++
				}
			}
		case <-timer.C:
			break loop
		case <-ctx.Done():
			break loop
		}
	}
	out := make([]Found, 0, len(found))
	for _, f := range found {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].From < out[j].From || out[i].From == out[j].From && out[i].Endpoint < out[j].Endpoint
	})
	return out, nil
}

func firstIPv4(ifc *net.Interface) net.IP {
	addrs, err := ifc.Addrs()
	if err != nil {
		return nil
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
			return ipn.IP.To4()
		}
	}
	return nil
}

func probeMessage() []byte {
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	return []byte(`<?xml version="1.0" encoding="UTF-8"?><e:Envelope xmlns:e="http://www.w3.org/2003/05/soap-envelope" xmlns:w="http://schemas.xmlsoap.org/ws/2004/08/addressing" xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery" xmlns:dn="http://www.onvif.org/ver10/network/wsdl"><e:Header><w:MessageID>uuid:` +
		hex.EncodeToString(id[:4]) + "-" + hex.EncodeToString(id[4:6]) + "-" + hex.EncodeToString(id[6:8]) + "-" + hex.EncodeToString(id[8:10]) + "-" + hex.EncodeToString(id[10:]) +
		`</w:MessageID><w:To>urn:schemas-xmlsoap-org:ws:2005:04:discovery</w:To><w:Action>http://schemas.xmlsoap.org/ws/2005/04/discovery/Probe</w:Action></e:Header><e:Body><d:Probe><d:Types>dn:NetworkVideoTransmitter</d:Types></d:Probe></e:Body></e:Envelope>`)
}

// parseProbeMatch reads one answer. It returns false for anything that is not a
// ProbeMatch with an endpoint and at least one usable address.
func parseProbeMatch(b []byte, from net.Addr) (Found, bool) {
	var env struct {
		Matches []struct {
			Endpoint string `xml:"EndpointReference>Address"`
			Scopes   string `xml:"Scopes"`
			XAddrs   string `xml:"XAddrs"`
		} `xml:"Body>ProbeMatches>ProbeMatch"`
	}
	if len(b) >= maxDatagram || xml.Unmarshal(b, &env) != nil || len(env.Matches) == 0 {
		return Found{}, false
	}
	m := env.Matches[0]
	f := Found{Endpoint: cleanText(m.Endpoint, 80)}
	if f.Endpoint == "" {
		return Found{}, false
	}
	host := ""
	if ua, ok := from.(*net.UDPAddr); ok {
		host = ua.IP.String()
		f.From = host
	}
	for _, a := range strings.Fields(m.XAddrs) {
		if len(f.XAddrs) >= 4 || len(a) > 512 {
			continue
		}
		u, err := url.Parse(a)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
			continue
		}
		clean := cleanText(a, 512)
		if clean != a {
			continue
		}
		f.XAddrs = append(f.XAddrs, a)
		if host != "" && u.Hostname() != host {
			f.Elsewhere = true
		}
	}
	if len(f.XAddrs) == 0 {
		return Found{}, false
	}
	for _, s := range strings.Fields(m.Scopes) {
		switch {
		case strings.HasPrefix(s, "onvif://www.onvif.org/name/") && f.Name == "":
			f.Name = scopeText(strings.TrimPrefix(s, "onvif://www.onvif.org/name/"))
		case strings.HasPrefix(s, "onvif://www.onvif.org/hardware/") && f.Hardware == "":
			f.Hardware = scopeText(strings.TrimPrefix(s, "onvif://www.onvif.org/hardware/"))
		}
	}
	return f, true
}

// scopeText reads an ONVIF scope value: percent-encoded, device text, cleaned and short.
func scopeText(s string) string {
	if v, err := url.PathUnescape(s); err == nil {
		s = v
	}
	return cleanText(s, 48)
}
