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
//   - CreateMonitoredItems: gopcua answered Good for any node id at all,
//     and the bad ones surfaced later as a notification carrying an error.
//     An item on a node that does not exist, or on an Object, is refused
//     at creation with the code a Read of it would give.
//
// gopcua honours a handler registered before Start and keeps it, so these
// are installed between New and Start; the ones that need gopcua's own
// bookkeeping (monitored items) delegate to its service for the items that
// pass.
package server

import (
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/gopcua/opcua/id"
	opcserver "github.com/gopcua/opcua/server"
	"github.com/gopcua/opcua/ua"
	"github.com/gopcua/opcua/uasc"
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
// must run before srv.Start: gopcua registers its own handlers there and
// keeps whichever was first.
func registerServices(srv *opcserver.Server, ns *namespace) {
	srv.RegisterHandler(id.GetEndpointsRequest_Encoding_DefaultBinary, func(_ *uasc.SecureChannel, r ua.Request, _ uint32) (ua.Response, error) {
		return getEndpoints(srv, r)
	})
	srv.RegisterHandler(id.ReadRequest_Encoding_DefaultBinary, func(_ *uasc.SecureChannel, r ua.Request, _ uint32) (ua.Response, error) {
		return readAttributes(srv, ns, r)
	})
	srv.RegisterHandler(id.WriteRequest_Encoding_DefaultBinary, func(_ *uasc.SecureChannel, r ua.Request, _ uint32) (ua.Response, error) {
		return writeAttributes(srv, ns, r)
	})
	srv.RegisterHandler(id.CreateMonitoredItemsRequest_Encoding_DefaultBinary, func(sc *uasc.SecureChannel, r ua.Request, reqID uint32) (ua.Response, error) {
		return createMonitoredItems(srv, ns, sc, r, reqID)
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
	all := srv.Endpoints()
	want := normURL(req.EndpointURL)
	var match []*ua.EndpointDescription
	for _, ep := range all {
		if normURL(ep.EndpointURL) == want {
			match = append(match, ep)
		}
	}
	if len(match) == 0 {
		match = all
	}
	return &ua.GetEndpointsResponse{ResponseHeader: header(req.RequestHeader), Endpoints: match}, nil
}

func normURL(u string) string { return strings.ToLower(strings.TrimSuffix(u, "/")) }

// readAttributes is the Read service: each node's attribute from its
// namespace, Bad_NodeIdUnknown for a namespace the server does not have,
// and the access attributes filled in where gopcua's nodes lack them.
func readAttributes(srv *opcserver.Server, ns *namespace, r ua.Request) (ua.Response, error) {
	req, ok := r.(*ua.ReadRequest)
	if !ok {
		return nil, ua.StatusBadRequestTypeInvalid
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
		results[i] = dv
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

// createMonitoredItems checks each item before gopcua books it: an item
// on a node id that is not a node, or on an attribute the node does not
// have (a Value on an Object or the folder), is answered with that code in
// its own result, and only the rest are created. gopcua alone answers
// Good for everything and lets the bad ones fail inside a notification,
// which a client that trusts the create result never reads.
func createMonitoredItems(srv *opcserver.Server, ns *namespace, sc *uasc.SecureChannel, r ua.Request, reqID uint32) (ua.Response, error) {
	req, ok := r.(*ua.CreateMonitoredItemsRequest)
	if !ok {
		return nil, ua.StatusBadRequestTypeInvalid
	}
	mi := srv.MonitoredItemService
	if mi == nil {
		return nil, ua.StatusBadInternalError
	}
	results := make([]*ua.MonitoredItemCreateResult, len(req.ItemsToCreate))
	fwd := &ua.CreateMonitoredItemsRequest{
		RequestHeader:      req.RequestHeader,
		SubscriptionID:     req.SubscriptionID,
		TimestampsToReturn: req.TimestampsToReturn,
	}
	var slots []int // results index of each forwarded item
	for i, item := range req.ItemsToCreate {
		if code := monitorable(srv, ns, item); code != ua.StatusGood {
			results[i] = &ua.MonitoredItemCreateResult{StatusCode: code, FilterResult: ua.NewExtensionObject(nil)}
			continue
		}
		fwd.ItemsToCreate = append(fwd.ItemsToCreate, item)
		slots = append(slots, i)
	}
	resp, err := mi.CreateMonitoredItems(sc, fwd, reqID)
	if err != nil {
		return nil, err
	}
	out, ok := resp.(*ua.CreateMonitoredItemsResponse)
	if !ok {
		return resp, nil
	}
	for j, i := range slots {
		if j < len(out.Results) {
			results[i] = out.Results[j]
		}
	}
	out.Results = results
	return out, nil
}

// monitorable is the code CreateMonitoredItems answers for one item: Good
// when the node exists and has the attribute, otherwise what a Read of it
// says.
func monitorable(srv *opcserver.Server, ns *namespace, item *ua.MonitoredItemCreateRequest) ua.StatusCode {
	if item == nil || item.ItemToMonitor == nil || item.ItemToMonitor.NodeID == nil {
		return ua.StatusBadNodeIDInvalid
	}
	nid, attr := item.ItemToMonitor.NodeID, item.ItemToMonitor.AttributeID
	if nid.Namespace() == ns.id {
		dv := ns.Attribute(nid, attr)
		if dv == nil {
			return ua.StatusBadNodeIDUnknown
		}
		switch dv.Status {
		case ua.StatusBadNodeIDUnknown, ua.StatusBadNodeIDInvalid, ua.StatusBadAttributeIDInvalid:
			return dv.Status
		}
		return ua.StatusGood
	}
	if srv.Node(nid) == nil {
		return ua.StatusBadNodeIDUnknown
	}
	return ua.StatusGood
}
