// monitoring.go is the subscription and monitored-item services this
// package answers itself. Each is here because gopcua's own, found by the
// black-box run of 2026-10-24 against PR #416, would:
//
//   - CreateSubscription: book a publishing interval of 0 (a legal request,
//     "as fast as you can") and then panic in the subscription goroutine
//     on time.NewTicker(0), taking the whole controller with it (F-25). An
//     interval too large for a Duration does the same.
//   - CreateMonitoredItems: send each new item's first value from a
//     goroutine that holds the monitored-item lock while it writes to the
//     subscription's channel; a channel nobody drains (the subscription
//     died, or the client stopped publishing) leaves the lock held for
//     good, and the sweep, Status and every later subscription call wait
//     on it: the server freezes with the process up (F-26). It also booked
//     an item's MonitoringMode without reading it (F-27).
//   - SetMonitoringMode and DeleteMonitoredItems: answer an item id that is
//     not an item by dereferencing it, which is a panic a client can cause
//     with one request.
//
// Here the items are booked under gopcua's lock without sending anything.
// A project tag's first value comes from the sweep, which never blocks; a
// node outside the project namespace (the Server object's variables) is
// sent its one value with no lock held. The sweep reads each item's mode
// under the lock and sends only to items that are Reporting.
package server

import (
	"math"
	"time"

	"github.com/gopcua/opcua/ua"
	"github.com/gopcua/opcua/uasc"
	opcserver "github.com/joyautomation/nautilus/opcua/internal/uaserver"
)

// maxPublishingInterval is the longest publishing interval a subscription
// may be granted, in milliseconds: an hour. Part 4 §5.13.2 has the server
// revise the request to a rate it supports; the ticker behind gopcua's
// subscription cannot take 0, a negative, NaN or anything that overflows
// a Duration, so the request is revised before gopcua sees it.
const maxPublishingInterval = float64(time.Hour / time.Millisecond)

// revisePublishing is the publishing interval granted, in milliseconds:
// what was asked, no faster than the sweep (0 and anything below it ask
// for the fastest, and the sweep is the fastest) and no slower than
// maxPublishingInterval.
func revisePublishing(requested float64, sweep time.Duration) float64 {
	sw := float64(sweep) / float64(time.Millisecond)
	switch {
	case math.IsNaN(requested), requested < sw:
		return sw
	case requested > maxPublishingInterval:
		return maxPublishingInterval
	}
	return requested
}

// createSubscription revises the request to what the server supports and
// lets gopcua book it: gopcua answers with the request's own numbers, so
// revising the request in place revises the subscription and the response
// together.
func createSubscription(srv *opcserver.Server, ns *namespace, sc *uasc.SecureChannel, r ua.Request, reqID uint32) (ua.Response, error) {
	req, ok := r.(*ua.CreateSubscriptionRequest)
	if !ok {
		return nil, ua.StatusBadRequestTypeInvalid
	}
	subs := srv.SubscriptionService
	if subs == nil {
		return nil, ua.StatusBadInternalError
	}
	if srv.Session(req.RequestHeader) == nil {
		return nil, ua.StatusBadSessionIDInvalid
	}
	req.RequestedPublishingInterval = revisePublishing(req.RequestedPublishingInterval, ns.sweep)
	// Part 4 §5.13.2.2: the lifetime count is at least three keep-alives.
	if floor := uint64(req.RequestedMaxKeepAliveCount) * 3; uint64(req.RequestedLifetimeCount) < floor {
		req.RequestedLifetimeCount = uint32(min(floor, math.MaxUint32))
	}
	return subs.CreateSubscription(sc, req, reqID)
}

// modifySubscription answers with what the subscription has. gopcua's
// publish loop owns its ticker and its counts and reads them without a
// lock, so nothing here changes them: the publishing interval and counts
// are the ones granted at creation, and the response says so, which Part 4
// §5.13.3 allows (the server revises the request to what it supports).
// This exists because a client that is told a revised publishing interval
// — asyncua, for one — modifies the subscription to realign its counts,
// and gopcua's Bad_ServiceUnsupported there made every such subscription
// fail outright.
func modifySubscription(srv *opcserver.Server, r ua.Request) (ua.Response, error) {
	req, ok := r.(*ua.ModifySubscriptionRequest)
	if !ok {
		return nil, ua.StatusBadRequestTypeInvalid
	}
	sub, code := subscriptionOf(srv, req.RequestHeader, req.SubscriptionID)
	if code != ua.StatusGood {
		return nil, code
	}
	return &ua.ModifySubscriptionResponse{
		ResponseHeader:            header(req.RequestHeader),
		RevisedPublishingInterval: sub.RevisedPublishingInterval,
		RevisedLifetimeCount:      sub.RevisedLifetimeCount,
		RevisedMaxKeepAliveCount:  sub.RevisedMaxKeepAliveCount,
	}, nil
}

// subscriptionOf is the subscription a request names, when it is the
// requesting session's. Subscription ids are per session (Part 4 §5.13),
// so another session's is as invalid as one that does not exist.
func subscriptionOf(srv *opcserver.Server, hdr *ua.RequestHeader, id uint32) (*opcserver.Subscription, ua.StatusCode) {
	subs := srv.SubscriptionService
	if subs == nil {
		return nil, ua.StatusBadInternalError
	}
	sess := srv.Session(hdr)
	if sess == nil {
		return nil, ua.StatusBadSessionIDInvalid
	}
	subs.Mu.Lock()
	sub := subs.Subs[id]
	subs.Mu.Unlock()
	if sub == nil || sub.Session == nil || sub.Session.AuthTokenID == nil || sess.AuthTokenID == nil || sub.Session.AuthTokenID.String() != sess.AuthTokenID.String() {
		return nil, ua.StatusBadSubscriptionIDInvalid
	}
	return sub, ua.StatusGood
}

// createMonitoredItems books the items that can be monitored and answers
// each one: an item on a node id that is not a node, or on an attribute
// the node does not have (a Value on an Object or the folder), gets the
// code a Read of it would give, in its own result, and is not created.
//
// The sampling interval is revised to a multiple of the sweep (the sweep
// is MinimumSamplingInterval), and the sweep holds each item to the rate
// granted here. Nothing is sent while gopcua's lock is held: a project
// tag's first value is the sweep's to send, and a node outside the project
// namespace is sent its one value afterwards, with no lock held.
func createMonitoredItems(srv *opcserver.Server, ns *namespace, sc *uasc.SecureChannel, r ua.Request, reqID uint32) (ua.Response, error) {
	req, ok := r.(*ua.CreateMonitoredItemsRequest)
	if !ok {
		return nil, ua.StatusBadRequestTypeInvalid
	}
	mi := srv.MonitoredItemService
	if mi == nil {
		return nil, ua.StatusBadInternalError
	}
	sub, code := subscriptionOf(srv, req.RequestHeader, req.SubscriptionID)
	if code != ua.StatusGood {
		return nil, code
	}
	if len(req.ItemsToCreate) == 0 {
		return nil, ua.StatusBadNothingToDo
	}
	results := make([]*ua.MonitoredItemCreateResult, len(req.ItemsToCreate))
	var foreign []*opcserver.MonitoredItem // outside the project namespace: owed one value, below
	mi.Mu.Lock()
	for i, item := range req.ItemsToCreate {
		if code := monitorable(srv, ns, item); code != ua.StatusGood {
			results[i] = &ua.MonitoredItemCreateResult{StatusCode: code, FilterResult: ua.NewExtensionObject(nil)}
			continue
		}
		booked := &opcserver.MonitoredItem{ID: mi.NextID(), Sub: sub, Req: item, Mode: item.MonitoringMode}
		key := item.ItemToMonitor.NodeID.String()
		mi.Items[booked.ID] = booked
		mi.Nodes[key] = append(mi.Nodes[key], booked)
		mi.Subs[sub.ID] = append(mi.Subs[sub.ID], booked)
		results[i] = &ua.MonitoredItemCreateResult{
			StatusCode:              ua.StatusGood,
			MonitoredItemID:         booked.ID,
			RevisedSamplingInterval: reviseSampling(item.RequestedParameters.SamplingInterval, sub.RevisedPublishingInterval, ns.sweep),
			RevisedQueueSize:        1,
			FilterResult:            ua.NewExtensionObject(nil),
		}
		if item.ItemToMonitor.NodeID.Namespace() != ns.id && item.MonitoringMode == ua.MonitoringModeReporting {
			foreign = append(foreign, booked)
		}
	}
	mi.Mu.Unlock()
	for _, item := range foreign {
		go initialValue(srv, item)
	}
	return &ua.CreateMonitoredItemsResponse{ResponseHeader: header(req.RequestHeader), Results: results, DiagnosticInfos: []*ua.DiagnosticInfo{}}, nil
}

// initialValue sends an item on a node outside the project namespace the
// one value it will ever get: those nodes are gopcua's (the Server object
// and its variables) and nothing sweeps them. The send holds no lock and
// gives up when the subscription is not draining its channel, so a
// subscription that has died cannot hold anything else up.
func initialValue(srv *opcserver.Server, item *opcserver.MonitoredItem) {
	if !watching(item) {
		return
	}
	nid, attr := item.Req.ItemToMonitor.NodeID, item.Req.ItemToMonitor.AttributeID
	space, err := srv.Namespace(int(nid.Namespace()))
	if err != nil {
		return
	}
	dv := space.Attribute(nid, attr)
	if dv == nil {
		dv = status(ua.StatusBadNodeIDUnknown)
	}
	if rng := item.Req.ItemToMonitor.IndexRange; rng != "" {
		dv = indexRange(dv, rng)
	}
	select {
	case item.Sub.NotifyChannel <- &ua.MonitoredItemNotification{ClientHandle: item.Req.RequestedParameters.ClientHandle, Value: dv}:
	case <-time.After(5 * time.Second):
	}
}

// reviseSampling is the sampling interval granted, in milliseconds: the
// nearest multiple of the sweep to what was requested, never faster than
// the sweep; 0 (fastest) is the sweep, and a negative request (sample at
// the publishing interval) takes publishing as the request.
func reviseSampling(requested, publishing float64, sweep time.Duration) float64 {
	sw := float64(sweep) / float64(time.Millisecond)
	want := requested
	if want < 0 {
		want = publishing
	}
	if want <= sw {
		return sw
	}
	return math.Round(want/sw) * sw
}

// monitorable is the code CreateMonitoredItems answers for one item: Good
// when the node exists and has the attribute, otherwise what a Read of it
// says; Bad_MonitoringModeInvalid for a mode that is not one. An
// IndexRange is judged the way a Read judges it (indexrange.go): one the
// value does not reach, or one that is not a range, refuses the item.
func monitorable(srv *opcserver.Server, ns *namespace, item *ua.MonitoredItemCreateRequest) ua.StatusCode {
	if item == nil || item.ItemToMonitor == nil || item.ItemToMonitor.NodeID == nil {
		return ua.StatusBadNodeIDInvalid
	}
	if item.RequestedParameters == nil {
		return ua.StatusBadInvalidArgument
	}
	if item.MonitoringMode > ua.MonitoringModeReporting {
		return ua.StatusBadMonitoringModeInvalid
	}
	nid, attr, rng := item.ItemToMonitor.NodeID, item.ItemToMonitor.AttributeID, item.ItemToMonitor.IndexRange
	var dv *ua.DataValue
	if nid.Namespace() == ns.id {
		dv = ns.Attribute(nid, attr)
	} else if space, err := srv.Namespace(int(nid.Namespace())); err == nil && srv.Node(nid) != nil {
		dv = space.Attribute(nid, attr)
	}
	if dv == nil {
		return ua.StatusBadNodeIDUnknown
	}
	switch dv.Status {
	case ua.StatusBadNodeIDUnknown, ua.StatusBadNodeIDInvalid, ua.StatusBadAttributeIDInvalid:
		return dv.Status
	}
	if rng != "" {
		switch code := indexRange(dv, rng).Status; code {
		case ua.StatusBadIndexRangeInvalid, ua.StatusBadIndexRangeNoData:
			return code
		}
	}
	return ua.StatusGood
}

// setMonitoringMode records each item's mode, which the sweep reads: a
// Disabled or Sampling item is not reported to until it is Reporting
// again, and then hears the current value first (Part 4 §5.12.4); what it
// had queued but not yet published is dropped (uaserver's publish loop
// checks each notification's item as it publishes). An id
// that is not an item of the named subscription is Bad_MonitoredItemIdInvalid
// in its own result; gopcua dereferenced it.
func setMonitoringMode(srv *opcserver.Server, r ua.Request) (ua.Response, error) {
	req, ok := r.(*ua.SetMonitoringModeRequest)
	if !ok {
		return nil, ua.StatusBadRequestTypeInvalid
	}
	mi := srv.MonitoredItemService
	if mi == nil {
		return nil, ua.StatusBadInternalError
	}
	sub, code := subscriptionOf(srv, req.RequestHeader, req.SubscriptionID)
	if code != ua.StatusGood {
		return nil, code
	}
	if len(req.MonitoredItemIDs) == 0 {
		return nil, ua.StatusBadNothingToDo
	}
	if req.MonitoringMode > ua.MonitoringModeReporting {
		return nil, ua.StatusBadMonitoringModeInvalid
	}
	results := make([]ua.StatusCode, len(req.MonitoredItemIDs))
	mi.Mu.Lock()
	for i, id := range req.MonitoredItemIDs {
		item := mi.Items[id]
		if item == nil || item.Sub != sub {
			results[i] = ua.StatusBadMonitoredItemIDInvalid
			continue
		}
		item.Mode = req.MonitoringMode
		results[i] = ua.StatusGood
	}
	mi.Mu.Unlock()
	// A value queued while the item was Reporting is dropped at publish
	// time if it is not Reporting now; the fence makes sure no publish
	// checked before this change is still on its way when the client hears
	// Good (black-box F-27, an intermittent notification after Disabled).
	sub.Fence()
	return &ua.SetMonitoringModeResponse{ResponseHeader: header(req.RequestHeader), Results: results, DiagnosticInfos: []*ua.DiagnosticInfo{}}, nil
}

// deleteMonitoredItems removes each item of the named subscription that
// the request names, before it answers, and refuses the rest in their own
// results; gopcua answered Good for every id and dereferenced the ones
// that were nothing.
func deleteMonitoredItems(srv *opcserver.Server, r ua.Request) (ua.Response, error) {
	req, ok := r.(*ua.DeleteMonitoredItemsRequest)
	if !ok {
		return nil, ua.StatusBadRequestTypeInvalid
	}
	mi := srv.MonitoredItemService
	if mi == nil {
		return nil, ua.StatusBadInternalError
	}
	sub, code := subscriptionOf(srv, req.RequestHeader, req.SubscriptionID)
	if code != ua.StatusGood {
		return nil, code
	}
	if len(req.MonitoredItemIDs) == 0 {
		return nil, ua.StatusBadNothingToDo
	}
	results := make([]ua.StatusCode, len(req.MonitoredItemIDs))
	var gone []uint32
	mi.Mu.Lock()
	for i, id := range req.MonitoredItemIDs {
		item := mi.Items[id]
		if item == nil || item.Sub != sub {
			results[i] = ua.StatusBadMonitoredItemIDInvalid
			continue
		}
		results[i] = ua.StatusGood
		gone = append(gone, id)
	}
	mi.Mu.Unlock()
	for _, id := range gone {
		mi.DeleteMonitoredItem(id) // takes the lock itself
	}
	sub.Fence() // as in setMonitoringMode: nothing of a deleted item is published after this answer
	return &ua.DeleteMonitoredItemsResponse{ResponseHeader: header(req.RequestHeader), Results: results, DiagnosticInfos: []*ua.DiagnosticInfo{}}, nil
}
