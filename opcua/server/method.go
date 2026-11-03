// method.go is the Call service. The address space has no methods of its
// own; the Server object has the four ServerType declares, and the two
// that belong to the subscription services are implemented here:
//
//   - GetMonitoredItems (Part 5 §9.1): the server and client handles of a
//     subscription's items, which a client that has just transferred a
//     subscription uses to find its items again;
//   - ResendData (Part 5 §9.2): every Reporting item of a subscription is
//     sent its current value in the next publishing cycle.
//
// SetSubscriptionDurable and RequestServerStateChange are there, as the
// node set declares them, but their Executable reads false and a Call of
// either is Bad_NotExecutable. Until #431 every Call was
// Bad_ServiceUnsupported, a refusal of the whole service set rather than
// an answer for the method asked for.
package server

import (
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	opcserver "github.com/joyautomation/nautilus/opcua/internal/uaserver"
)

// maxNodesPerMethodCall is the most method calls one Call may carry,
// advertised in the Server object's OperationLimits (server.go); past it
// the request is Bad_TooManyOperations. Each call is judged with a few
// Browses (the object, its type and supertypes), so a hundred is a tenth
// of a second at worst on the server's one message loop.
const maxNodesPerMethodCall = 100

// method is an implemented method: its input arguments in, its output
// arguments or a status out, for the session that sent hdr.
type method func(srv *opcserver.Server, ns *namespace, hdr *ua.RequestHeader, args []*ua.Variant) *ua.CallMethodResult

// implemented is the method a Call of methodID on objectID runs, or nil.
// The method may be named by the Server object's own Method node or by the
// ServerType declaration it instantiates (Part 4 §5.12.2).
func implemented(objectID, methodID *ua.NodeID) method {
	if !isNS0(objectID, id.Server) {
		return nil
	}
	switch {
	case isNS0(methodID, id.Server_GetMonitoredItems), isNS0(methodID, id.ServerType_GetMonitoredItems):
		return getMonitoredItems
	case isNS0(methodID, id.Server_ResendData), isNS0(methodID, id.ServerType_ResendData):
		return resendData
	}
	return nil
}

// isNS0 reports whether nid is ns=0;i=n, however it was encoded: a client
// may send a numeric id in its two-byte, four-byte or full form, and
// gopcua keeps the form it decoded.
func isNS0(nid *ua.NodeID, n uint32) bool {
	return nid != nil && nid.Equal(ua.NewNumericNodeID(0, n))
}

// executable reports whether a Method node is one a Call runs: what its
// Executable and UserExecutable attributes read (accessAttribute).
func executable(nid *ua.NodeID) bool {
	return implemented(ua.NewNumericNodeID(0, id.Server), nid) != nil
}

// callMethods is the Call service (Part 4 §5.12.2): each method call is
// answered in its own result. An object id that is not a node is
// Bad_NodeIdUnknown, one that is not an Object or ObjectType
// Bad_NodeIdInvalid; a method id that is not a Method of that object (a
// component of it or of its type or a supertype) Bad_MethodInvalid; a
// method that is one but is not implemented Bad_NotExecutable.
func callMethods(srv *opcserver.Server, ns *namespace, r ua.Request) (ua.Response, error) {
	req, ok := r.(*ua.CallRequest)
	if !ok {
		return nil, ua.StatusBadRequestTypeInvalid
	}
	if len(req.MethodsToCall) == 0 {
		return nil, ua.StatusBadNothingToDo
	}
	if len(req.MethodsToCall) > maxNodesPerMethodCall {
		return nil, ua.StatusBadTooManyOperations
	}
	results := make([]*ua.CallMethodResult, len(req.MethodsToCall))
	for i, m := range req.MethodsToCall {
		results[i] = callOne(srv, ns, req.RequestHeader, m)
	}
	return &ua.CallResponse{ResponseHeader: header(req.RequestHeader), Results: results, DiagnosticInfos: []*ua.DiagnosticInfo{}}, nil
}

func callOne(srv *opcserver.Server, ns *namespace, hdr *ua.RequestHeader, m *ua.CallMethodRequest) *ua.CallMethodResult {
	if m == nil || m.ObjectID == nil {
		return callResult(ua.StatusBadNodeIDInvalid)
	}
	if m.MethodID == nil {
		return callResult(ua.StatusBadMethodInvalid)
	}
	class, code := nodeClassOf(srv, m.ObjectID)
	if code != ua.StatusGood {
		return callResult(code)
	}
	if class != ua.NodeClassObject && class != ua.NodeClassObjectType {
		return callResult(ua.StatusBadNodeIDInvalid)
	}
	if !hasMethod(srv, m.ObjectID, class, m.MethodID) {
		return callResult(ua.StatusBadMethodInvalid)
	}
	run := implemented(m.ObjectID, m.MethodID)
	if run == nil {
		return callResult(ua.StatusBadNotExecutable)
	}
	return run(srv, ns, hdr, m.InputArguments)
}

// callResult is a Call result with only a status.
func callResult(code ua.StatusCode) *ua.CallMethodResult {
	return &ua.CallMethodResult{StatusCode: code, InputArgumentResults: []ua.StatusCode{}, InputArgumentDiagnosticInfos: []*ua.DiagnosticInfo{}, OutputArguments: []*ua.Variant{}}
}

// nodeClassOf is a node's NodeClass as a Read of it answers, or the code a
// Read gives for a node that is not there.
func nodeClassOf(srv *opcserver.Server, nid *ua.NodeID) (ua.NodeClass, ua.StatusCode) {
	space, err := srv.Namespace(int(nid.Namespace()))
	if err != nil {
		return 0, ua.StatusBadNodeIDUnknown
	}
	dv := space.Attribute(nid, ua.AttributeIDNodeClass)
	if dv == nil || dv.Status != ua.StatusGood || dv.Value == nil {
		return 0, ua.StatusBadNodeIDUnknown
	}
	switch v := dv.Value.Value().(type) {
	case int32:
		return ua.NodeClass(v), ua.StatusGood
	case uint32:
		return ua.NodeClass(v), ua.StatusGood
	case ua.NodeClass:
		return v, ua.StatusGood
	}
	return 0, ua.StatusBadNodeIDUnknown
}

// hasMethod reports whether methodID is a Method of the object: the target
// of a HasComponent (or subtype) reference from the object, from its type,
// or from a supertype of that; of an ObjectType, from the type itself (Part
// 4 §5.12.2). References are as a Browse lists them.
func hasMethod(srv *opcserver.Server, objectID *ua.NodeID, class ua.NodeClass, methodID *ua.NodeID) bool {
	has := func(nid *ua.NodeID) bool {
		res := browsePage(srv, &ua.BrowseDescription{NodeID: nid, BrowseDirection: ua.BrowseDirectionForward, ReferenceTypeID: ua.NewNumericNodeID(0, id.HasComponent), IncludeSubtypes: true, NodeClassMask: uint32(ua.NodeClassMethod), ResultMask: uint32(ua.BrowseResultMaskAll)}, 0, 0)
		for _, r := range res.References {
			if r.NodeID != nil && r.NodeID.NodeID != nil && r.NodeID.NodeID.Equal(methodID) {
				return true
			}
		}
		return false
	}
	if has(objectID) {
		return true
	}
	if class != ua.NodeClassObject {
		return false
	}
	typ := related(srv, objectID, id.HasTypeDefinition, ua.BrowseDirectionForward)
	for depth := 0; typ != nil && depth < 32; depth++ { // ServerType is a few levels down; 32 guards a cycle
		if has(typ) {
			return true
		}
		typ = related(srv, typ, id.HasSubtype, ua.BrowseDirectionInverse)
	}
	return false
}

// related is the first node a Browse of nid finds over refType in dir, or nil.
func related(srv *opcserver.Server, nid *ua.NodeID, refType uint32, dir ua.BrowseDirection) *ua.NodeID {
	res := browsePage(srv, &ua.BrowseDescription{NodeID: nid, BrowseDirection: dir, ReferenceTypeID: ua.NewNumericNodeID(0, refType), ResultMask: uint32(ua.BrowseResultMaskAll)}, 0, 0)
	for _, r := range res.References {
		if r.NodeID != nil && r.NodeID.NodeID != nil {
			return r.NodeID.NodeID
		}
	}
	return nil
}

// subscriptionArg checks the one argument GetMonitoredItems and ResendData
// take, a subscription id, and finds the subscription: Bad_ArgumentsMissing
// or Bad_TooManyArguments for the wrong count, Bad_InvalidArgument (with
// Bad_TypeMismatch for the argument) when it is not a UInt32,
// Bad_SubscriptionIdInvalid when there is no such subscription, and
// Bad_UserAccessDenied when it is another session's (Part 5 §9.1, §9.2).
func subscriptionArg(srv *opcserver.Server, hdr *ua.RequestHeader, args []*ua.Variant) (*opcserver.Subscription, *ua.CallMethodResult) {
	switch {
	case len(args) == 0:
		return nil, callResult(ua.StatusBadArgumentsMissing)
	case len(args) > 1:
		return nil, callResult(ua.StatusBadTooManyArguments)
	}
	var subID uint32
	ok := false
	if args[0] != nil {
		subID, ok = args[0].Value().(uint32)
	}
	if !ok {
		r := callResult(ua.StatusBadInvalidArgument)
		r.InputArgumentResults = []ua.StatusCode{ua.StatusBadTypeMismatch}
		return nil, r
	}
	subs := srv.SubscriptionService
	if subs == nil {
		return nil, callResult(ua.StatusBadInternalError)
	}
	subs.Mu.Lock()
	sub := subs.Subs[subID]
	subs.Mu.Unlock()
	if sub == nil {
		return nil, callResult(ua.StatusBadSubscriptionIDInvalid)
	}
	if !sub.OwnedBy(srv.Session(hdr)) {
		return nil, callResult(ua.StatusBadUserAccessDenied)
	}
	return sub, nil
}

// getMonitoredItems answers a subscription's items: their server handles
// (the MonitoredItemIds) and client handles, in the same order.
func getMonitoredItems(srv *opcserver.Server, _ *namespace, hdr *ua.RequestHeader, args []*ua.Variant) *ua.CallMethodResult {
	sub, fail := subscriptionArg(srv, hdr, args)
	if fail != nil {
		return fail
	}
	server, client := []uint32{}, []uint32{}
	if mi := srv.MonitoredItemService; mi != nil {
		mi.Mu.Lock()
		for _, item := range mi.Subs[sub.ID] {
			if !watching(item, item.Req) {
				continue
			}
			server = append(server, item.ID)
			client = append(client, item.Req.RequestedParameters.ClientHandle)
		}
		mi.Mu.Unlock()
	}
	r := callResult(ua.StatusGood)
	r.InputArgumentResults = []ua.StatusCode{ua.StatusGood}
	r.OutputArguments = []*ua.Variant{ua.MustVariant(server), ua.MustVariant(client)}
	return r
}

// resendData has every Reporting item of a subscription sent its current
// value in the next publishing cycle.
func resendData(srv *opcserver.Server, ns *namespace, hdr *ua.RequestHeader, args []*ua.Variant) *ua.CallMethodResult {
	sub, fail := subscriptionArg(srv, hdr, args)
	if fail != nil {
		return fail
	}
	resendValues(srv, ns, sub)
	r := callResult(ua.StatusGood)
	r.InputArgumentResults = []ua.StatusCode{ua.StatusGood}
	return r
}
