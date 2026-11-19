package devprofile

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// The OPC UA side of a profile that a project's device and its virtual
// twin both read, so the manifest a controller is handed and the tree a
// virtual device serves cannot drift apart.

// PlaceholderNamespace is the prefix of a namespace URI a profile made up
// because the maker does not publish its own (docs/design/virtual-devices.md
// §4): a node in such a namespace can be addressed by its index only.
const PlaceholderNamespace = "urn:virtual:"

// IsPlaceholderNamespace reports whether uri is a stand-in, not the URI
// the real device announces.
func IsPlaceholderNamespace(uri string) bool { return strings.HasPrefix(uri, PlaceholderNamespace) }

// Namespace is the URI of namespace index ns in an OPC UA profile's array
// (index 0 is the OPC UA namespace itself, ""); "" when out of range.
func (p *Profile) Namespace(ns int) string {
	if p.OPCUA == nil || ns < 1 || ns > len(p.OPCUA.Namespaces) {
		return ""
	}
	return p.OPCUA.Namespaces[ns-1]
}

// NodeNamespace is a node id's ns= index, 0 without one (or with one that
// is not a number).
func NodeNamespace(nid string) int {
	rest, ok := strings.CutPrefix(nid, "ns=")
	if !ok {
		return 0
	}
	n, _, _ := strings.Cut(rest, ";")
	ns, err := strconv.Atoi(n)
	if err != nil {
		return 0
	}
	return ns
}

// checkNamespace refuses a node id in a namespace the profile's array does
// not have: a binding to it could name neither a URI nor a known index.
func (p *Profile) checkNamespace(nid string) error {
	if ns := NodeNamespace(nid); ns > len(p.OPCUA.Namespaces) {
		return fmt.Errorf("node-id %q: namespace %d is past the profile's namespaces (1-%d)", nid, ns, len(p.OPCUA.Namespaces))
	}
	return nil
}

// Substitute replaces each {param} in s with the instance's value, else
// the profile's default: an ifm master's {location} in its node ids.
func (p *Profile) Substitute(s string, params map[string]string) string {
	if !strings.Contains(s, "{") {
		return s
	}
	for k, v := range p.Params {
		if iv, ok := params[k]; ok {
			v = iv
		}
		s = strings.ReplaceAll(s, "{"+k+"}", v)
	}
	return s
}

// OPCUAEndpoint normalises where an OPC UA device answers, host[:port] or
// opc.tcp://host[:port], to opc.tcp://host:port; the port defaults to the
// profile's, then 4840.
func (p *Profile) OPCUAEndpoint(raw string) (string, error) {
	in := raw
	if !strings.Contains(in, "://") {
		in = "opc.tcp://" + in
	}
	u, err := url.Parse(in)
	if err != nil || u.Scheme != "opc.tcp" || u.Hostname() == "" {
		return "", fmt.Errorf("endpoint %q: want host[:port] or opc.tcp://host[:port]", raw)
	}
	port := u.Port()
	if port == "" {
		n := 4840
		if p.OPCUA != nil && p.OPCUA.Port != 0 {
			n = p.OPCUA.Port
		}
		port = strconv.Itoa(n)
	} else if n, err := strconv.Atoi(port); err != nil || n <= 0 || n > 65535 {
		return "", fmt.Errorf("endpoint %q: bad port", raw)
	}
	return "opc.tcp://" + net.JoinHostPort(u.Hostname(), port), nil
}
