// Package uaserver is the OPC UA server stack under nautilus's OPC UA server
// (opcua/server): channels, sessions, the service handlers, subscriptions
// and the node namespaces.
//
// Origin: copied from github.com/gopcua/opcua v0.9.1, directory server/
// (with server/attrs and server/refs), under the MIT license; the license
// and gopcua's copyright are in LICENSE beside this file. The protocol
// layers it is built on (ua, id, uasc, uacp, uapolicy, schema) stay an
// ordinary dependency on github.com/gopcua/opcua.
//
// It is ours now: fixes land here, and code nautilus overrides and no
// longer needs is removed, so it is not kept in step with upstream. A fix
// that upstream would want too is offered there separately.
package uaserver
