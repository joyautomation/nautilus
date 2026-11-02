// Copyright 2018-2020 opcua authors. All rights reserved.
// Use of this source code is governed by a MIT-style license that can be
// found in the LICENSE file.

// Package uasc is the OPC UA secure channel layer (Part 6 §6.7) under
// nautilus's OPC UA server and its stack (opcua/internal/uaserver).
//
// Origin: copied from github.com/gopcua/opcua v0.9.1, directory uasc/, under
// the MIT license; the license and gopcua's copyright are in LICENSE beside
// this file. The layers it is built on (ua, uacp, uapolicy) stay an
// ordinary dependency on github.com/gopcua/opcua, and nautilus's OPC UA
// CLIENT driver keeps using gopcua's own uasc through gopcua's client.
//
// It is ours now, as uaserver is: the server side's trust hook, its
// policy/mode allowlist and the accessors a handler needs to see its
// channel's security land here (docs/design/opcua-server-writes.md §2.1).
// A change upstream would want too is offered there separately.
package uasc
