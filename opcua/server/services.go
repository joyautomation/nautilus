// services.go is the handful of OPC UA services this package answers
// itself instead of leaving to gopcua, and the endpoint list it advertises.
// Each one is here because a foreign client (asyncua, UAExpert, a gateway)
// would otherwise see something the guide does not promise — found by the
// black-box run of 2026-10-22 against PR #416:
//
//   - GetEndpoints: gopcua answers only a request whose URL equals a
//     configured endpoint string, so a server on 0.0.0.0 told no client
//     anything. The server now advertises one endpoint per name it answers
//     to (every interface address, the host name, loopback), and a request
//     for a URL it does not know gets the whole list, as Part 4 §5.4.4 says.
//   - Write: gopcua's built-in Server object accepted anonymous writes to
//     NamespaceArray, ProductName, ServerStatus… and kept them for every
//     session. Nothing is writable here; the Server object included.
//   - Read: a node id in a namespace the server does not have answered the
//     bare Bad; it is Bad_NodeIdUnknown. The four access attributes Part 3
//     makes mandatory, which gopcua's built-in nodes lack, read as read-only.
//   - Read, again (2026-10-24): IndexRange was ignored, so a client asking
//     for one element of an array got the whole array, and an index past
//     its end got data instead of Bad_IndexRangeNoData (indexrange.go). And
//     the Server object advertised MaxNodesPerRead = 32 while a Read of 500
//     nodes succeeded; the limit is now one the server has and enforces.
//   - Browse: gopcua's Server object lists most of its references two or
//     three times (browse.go).
//   - CreateSubscription, ModifySubscription, CreateMonitoredItems,
//     SetMonitoringMode, DeleteMonitoredItems: monitoring.go, each for a
//     crash, a freeze or a Good that meant nothing.
//
// They are installed between New and Start, and uaserver (gopcua's server
// package, copied in) no longer carries its own versions of them, so these
// are the only ones; the ones that need its bookkeeping (subscriptions,
// monitored items) work on its exported state under its locks.
package server

import (
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	"github.com/gopcua/opcua/uasc"
	opcserver "github.com/joyautomation/nautilus/opcua/internal/uaserver"
)

// endpointHosts decides where the server listens for the host an author
// wrote and which other names it advertises for the same listener, so a
// client that dials any of them finds a matching endpoint:
//
//   - a wildcard (0.0.0.0, ::, empty) listens on every interface and
//     advertises every interface address, the host name and loopback;
//   - a name (localhost, a host name) listens on its IPv4 address when it
//     has one — the loopback a Windows SCADA host dials is 127.0.0.1, and
//     a listener on one address cannot take both — and advertises the name;
//   - an address listens there, and advertises localhost when it is
//     loopback.
//
// Hosts are returned in URL form: an IPv6 literal is bracketed.
func endpointHosts(host string) (listen string, aliases []string) {
	seen := map[string]bool{}
	add := func(h string) {
		if h == "" || seen[h] {
			return
		}
		seen[h] = true
		aliases = append(aliases, h)
	}
	ip := net.ParseIP(host)
	switch {
	case host == "" || (ip != nil && ip.IsUnspecified()):
		listen = "0.0.0.0"
		if ip != nil {
			listen = hostLiteral(ip)
		}
		seen[host], seen[listen] = true, true // the wildcard is not a name to dial
		if addrs, err := net.InterfaceAddrs(); err == nil {
			for _, a := range addrs {
				ipn, ok := a.(*net.IPNet)
				if !ok || ipn.IP.IsLoopback() || ipn.IP.IsLinkLocalUnicast() {
					continue
				}
				add(hostLiteral(ipn.IP))
			}
		}
		if hn, err := os.Hostname(); err == nil {
			add(hn)
		}
		add("localhost")
		add("127.0.0.1")
		add("[::1]")
	case ip != nil:
		listen = hostLiteral(ip)
		if ip.IsLoopback() {
			add("localhost")
		}
	default:
		listen = host
		if ips, err := net.LookupIP(host); err == nil && len(ips) > 0 {
			pick := ips[0]
			for _, cand := range ips {
				if cand.To4() != nil {
					pick = cand
					break
				}
			}
			listen = hostLiteral(pick)
			add(host)
		}
	}
	return listen, aliases
}

// hostLiteral is an IP as it appears in a URL.
func hostLiteral(ip net.IP) string {
	if ip.To4() == nil {
		return "[" + ip.String() + "]"
	}
	return ip.String()
}

// endpointURL is the opc.tcp URL of a host (already in URL form) and port.
func endpointURL(host string, port int) string {
	return fmt.Sprintf("opc.tcp://%s:%d", host, port)
}

// registerServices installs this package's service handlers on srv. It
// must run before srv.Start, which registers uaserver's own handlers and
// keeps whichever was first; uaserver has none for these services, so one
// not registered here is Bad_ServiceUnsupported.
func registerServices(srv *opcserver.Server, ns *namespace) {
	// CreateSession's ServerEndpoints are the list GetEndpoints gives, so a
	// client that verifies one against the other finds them equal (#430).
	srv.EndpointsFor = func(url string) []*ua.EndpointDescription { return endpointsFor(srv, url) }
	srv.RegisterHandler(id.GetEndpointsRequest_Encoding_DefaultBinary, func(_ *uasc.SecureChannel, r ua.Request, _ uint32) (ua.Response, error) {
		return getEndpoints(srv, r)
	})
	srv.RegisterHandler(id.FindServersRequest_Encoding_DefaultBinary, func(_ *uasc.SecureChannel, r ua.Request, _ uint32) (ua.Response, error) {
		return findServers(srv, r)
	})
	srv.RegisterHandler(id.ReadRequest_Encoding_DefaultBinary, func(_ *uasc.SecureChannel, r ua.Request, _ uint32) (ua.Response, error) {
		return readAttributes(srv, ns, r)
	})
	srv.RegisterHandler(id.WriteRequest_Encoding_DefaultBinary, func(_ *uasc.SecureChannel, r ua.Request, _ uint32) (ua.Response, error) {
		return writeAttributes(srv, ns, r)
	})
	srv.RegisterHandler(id.BrowseRequest_Encoding_DefaultBinary, func(_ *uasc.SecureChannel, r ua.Request, _ uint32) (ua.Response, error) {
		return browseNodes(srv, r)
	})
	srv.RegisterHandler(id.BrowseNextRequest_Encoding_DefaultBinary, func(_ *uasc.SecureChannel, r ua.Request, _ uint32) (ua.Response, error) {
		return browseNextNodes(srv, r)
	})
	srv.RegisterHandler(id.CreateSubscriptionRequest_Encoding_DefaultBinary, func(sc *uasc.SecureChannel, r ua.Request, reqID uint32) (ua.Response, error) {
		return createSubscription(srv, ns, sc, r, reqID)
	})
	srv.RegisterHandler(id.ModifySubscriptionRequest_Encoding_DefaultBinary, func(_ *uasc.SecureChannel, r ua.Request, _ uint32) (ua.Response, error) {
		return modifySubscription(srv, r)
	})
	srv.RegisterHandler(id.CreateMonitoredItemsRequest_Encoding_DefaultBinary, func(sc *uasc.SecureChannel, r ua.Request, reqID uint32) (ua.Response, error) {
		return createMonitoredItems(srv, ns, sc, r, reqID)
	})
	srv.RegisterHandler(id.SetMonitoringModeRequest_Encoding_DefaultBinary, func(_ *uasc.SecureChannel, r ua.Request, _ uint32) (ua.Response, error) {
		return setMonitoringMode(srv, r)
	})
	srv.RegisterHandler(id.DeleteMonitoredItemsRequest_Encoding_DefaultBinary, func(_ *uasc.SecureChannel, r ua.Request, _ uint32) (ua.Response, error) {
		return deleteMonitoredItems(srv, r)
	})
}

// header is the response header of a service that succeeded as a whole
// (per-item results carry their own codes).
func header(req *ua.RequestHeader) *ua.ResponseHeader {
	var handle uint32
	if req != nil {
		handle = req.RequestHandle
	}
	return &ua.ResponseHeader{
		Timestamp:          time.Now(),
		RequestHandle:      handle,
		ServiceResult:      ua.StatusOK,
		ServiceDiagnostics: &ua.DiagnosticInfo{},
		StringTable:        []string{},
		AdditionalHeader:   ua.NewExtensionObject(nil),
	}
}

// getEndpoints answers with the endpoints whose URL the client asked for,
// and with every endpoint when it asked for one the server does not know:
// the URL in the request is the network path the client used to reach us,
// not a filter that may leave it with nothing (Part 4 §5.4.4).
func getEndpoints(srv *opcserver.Server, r ua.Request) (ua.Response, error) {
	req, ok := r.(*ua.GetEndpointsRequest)
	if !ok {
		return nil, ua.StatusBadRequestTypeInvalid
	}
	return &ua.GetEndpointsResponse{ResponseHeader: header(req.RequestHeader), Endpoints: endpointsFor(srv, req.EndpointURL)}, nil
}

// endpointsFor is the endpoint list for a client that sent url, in
// GetEndpoints and in CreateSession's ServerEndpoints alike: the advertised
// endpoints whose URL is the one asked for, or all of them when none is.
// Part 4 §5.6.2 has CreateSession return the endpoints available for the
// server and use endpointUrl only to choose URLs, and a client that reached
// us through a NAT address or an alias verifies its session against this
// list, so an empty one is a session it refuses.
func endpointsFor(srv *opcserver.Server, url string) []*ua.EndpointDescription {
	all := advertised(srv.Endpoints())
	want := normURL(url)
	var match []*ua.EndpointDescription
	for _, ep := range all {
		if normURL(ep.EndpointURL) == want {
			match = append(match, ep)
		}
	}
	if len(match) == 0 {
		return all
	}
	return match
}

func normURL(u string) string { return strings.ToLower(strings.TrimSuffix(u, "/")) }

// advertised drops the endpoint gopcua listens on when that is a wildcard:
// 0.0.0.0 is the address the server binds, not a name a client can dial, so
// the aliases registered beside it are the whole list a client sees.
func advertised(eps []*ua.EndpointDescription) []*ua.EndpointDescription {
	out := make([]*ua.EndpointDescription, 0, len(eps))
	for _, ep := range eps {
		if ep == nil || wildcardURL(ep.EndpointURL) {
			continue
		}
		out = append(out, ep)
	}
	return out
}

func wildcardURL(u string) bool {
	rest := strings.TrimPrefix(u, "opc.tcp://")
	host, _, err := net.SplitHostPort(rest)
	if err != nil {
		return false
	}
	switch host {
	case "0.0.0.0", "::", "":
		return true
	}
	return false
}

// findServers is the FindServers service: the server's own description,
// as gopcua answers it, with the wildcard kept out of the discovery URLs
// the same way getEndpoints keeps it out of the endpoints.
func findServers(srv *opcserver.Server, r ua.Request) (ua.Response, error) {
	req, ok := r.(*ua.FindServersRequest)
	if !ok {
		return nil, ua.StatusBadRequestTypeInvalid
	}
	var servers []*ua.ApplicationDescription
	if eps := srv.Endpoints(); len(eps) > 0 && eps[0] != nil && eps[0].Server != nil {
		app := *eps[0].Server
		app.DiscoveryURLs = nil
		for _, u := range eps[0].Server.DiscoveryURLs {
			if !wildcardURL(u) {
				app.DiscoveryURLs = append(app.DiscoveryURLs, u)
			}
		}
		servers = append(servers, &app)
	}
	return &ua.FindServersResponse{ResponseHeader: header(req.RequestHeader), Servers: servers}, nil
}

// maxNodesPerRead is the most nodes one Read may name, and the number the
// Server object's OperationLimits advertise: ten thousand, so a project of
// a few thousand tags is still one call, well inside the 2 MB message cap.
// gopcua advertised 32 and enforced nothing (black-box F-30); a limit a
// client reads is one it chunks its reads to, so it has to be true.
const maxNodesPerRead = 10000

// readAttributes is the Read service: each node's attribute from its
// namespace, Bad_NodeIdUnknown for a namespace the server does not have,
// the access attributes filled in where gopcua's nodes lack them, and an
// IndexRange applied to the value.
func readAttributes(srv *opcserver.Server, ns *namespace, r ua.Request) (ua.Response, error) {
	req, ok := r.(*ua.ReadRequest)
	if !ok {
		return nil, ua.StatusBadRequestTypeInvalid
	}
	if len(req.NodesToRead) == 0 {
		return nil, ua.StatusBadNothingToDo
	}
	if len(req.NodesToRead) > maxNodesPerRead {
		return nil, ua.StatusBadTooManyOperations
	}
	results := make([]*ua.DataValue, len(req.NodesToRead))
	for i, n := range req.NodesToRead {
		if n == nil || n.NodeID == nil {
			results[i] = status(ua.StatusBadNodeIDInvalid)
			continue
		}
		space, err := srv.Namespace(int(n.NodeID.Namespace()))
		if err != nil {
			results[i] = status(ua.StatusBadNodeIDUnknown)
			continue
		}
		dv := space.Attribute(n.NodeID, n.AttributeID)
		if dv == nil {
			dv = status(ua.StatusBadNodeIDUnknown)
		}
		if dv.Status == ua.StatusBadAttributeIDInvalid && n.NodeID.Namespace() != ns.id {
			dv = accessAttribute(srv, n.NodeID, n.AttributeID, dv)
		}
		if n.IndexRange != "" {
			dv = indexRange(dv, n.IndexRange)
		}
		results[i] = wireValue(dv) // "" as an empty String, not null (#434)
	}
	return &ua.ReadResponse{ResponseHeader: header(req.RequestHeader), Results: results}, nil
}

// accessAttribute answers the access attributes Part 3 §5.2 makes
// mandatory on every node — WriteMask and UserWriteMask on all, AccessLevel
// and UserAccessLevel on Variables — for a built-in node that has none:
// read-only, like everything this server serves. Any other attribute, or a
// node id that is not a node, keeps the answer it had.
func accessAttribute(srv *opcserver.Server, nid *ua.NodeID, attr ua.AttributeID, had *ua.DataValue) *ua.DataValue {
	node := srv.Node(nid)
	if node == nil {
		return had
	}
	switch attr {
	case ua.AttributeIDWriteMask, ua.AttributeIDUserWriteMask:
		return value(uint32(0))
	case ua.AttributeIDAccessLevel, ua.AttributeIDUserAccessLevel:
		// gopcua's built-in variables do not all carry a NodeClass; one
		// that answers a Value is a Variable whatever it says.
		if _, err := node.Attribute(ua.AttributeIDValue); err == nil || node.NodeClass() == ua.NodeClassVariable {
			return value(uint8(ua.AccessLevelTypeCurrentRead))
		}
	}
	return had
}

// writeAttributes is the Write service. The project's nodes refuse as the
// namespace says (Bad_UserAccessDenied on a value, Bad_NotWritable on the
// rest); every other node the server has — gopcua's Server object and its
// variables — is refused the same way; a node id that is nothing is
// Bad_NodeIdUnknown. gopcua's own Write would let an anonymous session
// rewrite NamespaceArray or ServerStatus for every other client.
func writeAttributes(srv *opcserver.Server, ns *namespace, r ua.Request) (ua.Response, error) {
	req, ok := r.(*ua.WriteRequest)
	if !ok {
		return nil, ua.StatusBadRequestTypeInvalid
	}
	results := make([]ua.StatusCode, len(req.NodesToWrite))
	for i, n := range req.NodesToWrite {
		switch {
		case n == nil || n.NodeID == nil:
			results[i] = ua.StatusBadNodeIDInvalid
		case n.NodeID.Namespace() == ns.id:
			results[i] = ns.SetAttribute(n.NodeID, n.AttributeID, n.Value)
		case srv.Node(n.NodeID) == nil:
			results[i] = ua.StatusBadNodeIDUnknown
		case n.AttributeID == ua.AttributeIDValue:
			results[i] = ua.StatusBadUserAccessDenied
		default:
			results[i] = ua.StatusBadNotWritable
		}
	}
	return &ua.WriteResponse{ResponseHeader: header(req.RequestHeader), Results: results, DiagnosticInfos: []*ua.DiagnosticInfo{}}, nil
}
