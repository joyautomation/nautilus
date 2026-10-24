package server

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
)

// What the black-box run of 2026-10-24 (001b) found in the subscription and
// monitored-item services, each kept from coming back.

func TestRevisePublishing(t *testing.T) {
	sweep := 100 * time.Millisecond
	cases := []struct{ ask, want float64 }{
		{0, 100}, {-5, 100}, {math.NaN(), 100}, {1, 100}, {99.9, 100}, {100, 100}, {250, 250},
		{maxPublishingInterval, maxPublishingInterval}, {1e300, maxPublishingInterval}, {math.Inf(1), maxPublishingInterval},
	}
	for _, c := range cases {
		if got := revisePublishing(c.ask, sweep); got != c.want {
			t.Errorf("revisePublishing(%v) = %v, want %v", c.ask, got, c.want)
		}
	}
}

// F-25: a subscription asked for with publishing interval 0 ("as fast as
// you can") is revised to the sweep, not booked as 0 and acted on — gopcua
// did the latter and the ticker behind it panicked, taking the controller
// with it. Anything a Duration cannot hold is revised the same way.
func TestPublishingIntervalIsRevisedNotActedOn(t *testing.T) {
	rt, _ := newRuntime(t)
	s, c := startServer(t, rt)
	ctx := context.Background()
	sweep := float64(s.opts.Sweep / time.Millisecond)
	for _, ask := range []float64{0, -1, math.NaN(), 1e300} {
		var resp *ua.CreateSubscriptionResponse
		req := &ua.CreateSubscriptionRequest{RequestHeader: &ua.RequestHeader{}, RequestedPublishingInterval: ask, RequestedLifetimeCount: 1000, RequestedMaxKeepAliveCount: 10, PublishingEnabled: true}
		err := c.Send(ctx, req, func(v ua.Response) error {
			r, ok := v.(*ua.CreateSubscriptionResponse)
			if !ok {
				return fmt.Errorf("%T", v)
			}
			resp = r
			return nil
		})
		if err != nil {
			t.Fatalf("CreateSubscription(%v): %v", ask, err)
		}
		if resp.RevisedPublishingInterval < sweep || resp.RevisedPublishingInterval > maxPublishingInterval {
			t.Errorf("asked %v, granted %v; want within [%v, %v]", ask, resp.RevisedPublishingInterval, sweep, maxPublishingInterval)
		}
		if resp.RevisedLifetimeCount < 3*resp.RevisedMaxKeepAliveCount {
			t.Errorf("lifetime %d is under three keep-alives (%d)", resp.RevisedLifetimeCount, resp.RevisedMaxKeepAliveCount)
		}
	}
	// A lifetime shorter than three keep-alives is raised to them.
	var resp *ua.CreateSubscriptionResponse
	err := c.Send(ctx, &ua.CreateSubscriptionRequest{RequestHeader: &ua.RequestHeader{}, RequestedPublishingInterval: 100, RequestedLifetimeCount: 1, RequestedMaxKeepAliveCount: 10, PublishingEnabled: true}, func(v ua.Response) error {
		resp, _ = v.(*ua.CreateSubscriptionResponse)
		return nil
	})
	if err != nil || resp == nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	if resp.RevisedLifetimeCount != 30 {
		t.Errorf("lifetime 1 with keep-alive 10 revised to %d, want 30", resp.RevisedLifetimeCount)
	}
	// A client told a revised interval realigns its counts with
	// ModifySubscription (asyncua does); it is answered with what the
	// subscription has, not refused.
	var mod *ua.ModifySubscriptionResponse
	err = c.Send(ctx, &ua.ModifySubscriptionRequest{RequestHeader: &ua.RequestHeader{}, SubscriptionID: resp.SubscriptionID, RequestedPublishingInterval: 5000, RequestedLifetimeCount: 60, RequestedMaxKeepAliveCount: 20}, func(v ua.Response) error {
		mod, _ = v.(*ua.ModifySubscriptionResponse)
		return nil
	})
	if err != nil || mod == nil {
		t.Fatalf("ModifySubscription: %v", err)
	}
	if mod.RevisedPublishingInterval != 100 || mod.RevisedLifetimeCount != 30 || mod.RevisedMaxKeepAliveCount != 10 {
		t.Errorf("ModifySubscription granted %v/%d/%d, want the subscription's own 100/30/10", mod.RevisedPublishingInterval, mod.RevisedLifetimeCount, mod.RevisedMaxKeepAliveCount)
	}
	if err := c.Send(ctx, &ua.ModifySubscriptionRequest{RequestHeader: &ua.RequestHeader{}, SubscriptionID: 4242}, func(ua.Response) error { return nil }); !errors.Is(err, ua.StatusBadSubscriptionIDInvalid) {
		t.Errorf("ModifySubscription of a subscription that is not there: %v", err)
	}
	time.Sleep(4 * s.opts.Sweep)
	// The controller is still here, and its subscriptions ticked.
	if dv := read(t, c, s.NodeID("Level"), ua.AttributeIDValue); dv.Status != ua.StatusGood {
		t.Fatalf("read after the pub-0 subscriptions: %s", dv.Status)
	}
	if st := s.Status(); st.Subscriptions != 5 {
		t.Errorf("%d subscriptions booked, want 5", st.Subscriptions)
	}
}

// count drains notify for d and counts the data changes for each client
// handle.
func count(notify <-chan *opcua.PublishNotificationData, d time.Duration) map[uint32]int {
	got := map[uint32]int{}
	deadline := time.After(d)
	for {
		select {
		case n := <-notify:
			if dc, ok := n.Value.(*ua.DataChangeNotification); ok {
				for _, m := range dc.MonitoredItems {
					got[m.ClientHandle]++
				}
			}
		case <-deadline:
			return got
		}
	}
}

// F-27: SetMonitoringMode(Disabled) silences an item, an item created
// Disabled is silent from the start, and one set back to Reporting hears
// the current value first. An id that is not an item is refused in its own
// result — gopcua dereferenced it, and the controller died.
func TestMonitoringModeIsHonoured(t *testing.T) {
	rt, _ := newRuntime(t)
	s, c := startServer(t, rt)
	ctx := context.Background()
	stop := make(chan struct{})
	defer close(stop)
	go func() { // Count moves every 10 ms
		var n int64
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
				n++
				rt.Tags().Set("Count", n)
			}
		}
	}()
	notify := make(chan *opcua.PublishNotificationData, 256)
	sub, err := c.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: 20 * time.Millisecond}, notify)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel(ctx)
	disabled := opcua.NewMonitoredItemCreateRequestWithDefaults(s.NodeID("Count"), ua.AttributeIDValue, 2)
	disabled.MonitoringMode = ua.MonitoringModeDisabled
	resp, err := sub.Monitor(ctx, ua.TimestampsToReturnBoth, opcua.NewMonitoredItemCreateRequestWithDefaults(s.NodeID("Count"), ua.AttributeIDValue, 1), disabled)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range resp.Results {
		if r.StatusCode != ua.StatusGood {
			t.Fatalf("item %d: %s", i, r.StatusCode)
		}
	}
	live, quiet := resp.Results[0].MonitoredItemID, resp.Results[1].MonitoredItemID

	got := count(notify, 300*time.Millisecond)
	if got[1] == 0 {
		t.Fatal("the Reporting item heard nothing")
	}
	if got[2] != 0 {
		t.Errorf("the item created Disabled heard %d changes", got[2])
	}

	mode, err := sub.SetMonitoringMode(ctx, ua.MonitoringModeDisabled, live)
	if err != nil {
		t.Fatal(err)
	}
	if mode.Results[0] != ua.StatusGood {
		t.Fatalf("SetMonitoringMode(Disabled): %s", mode.Results[0])
	}
	count(notify, 100*time.Millisecond) // what was already on its way
	if got := count(notify, 300*time.Millisecond); got[1] != 0 {
		t.Errorf("Disabled, yet %d changes in 300 ms", got[1])
	}

	if _, err := sub.SetMonitoringMode(ctx, ua.MonitoringModeReporting, live, quiet); err != nil {
		t.Fatal(err)
	}
	got = count(notify, 300*time.Millisecond)
	if got[1] == 0 || got[2] == 0 {
		t.Errorf("back to Reporting, heard %d and %d changes", got[1], got[2])
	}

	// Ids that are nothing are refused per item and nothing else happens.
	// (The gopcua client checks ids itself, so these go as raw requests,
	// the way a foreign client sends them.)
	var modeResults, delResults []ua.StatusCode
	if err := c.Send(ctx, &ua.SetMonitoringModeRequest{RequestHeader: &ua.RequestHeader{}, SubscriptionID: sub.SubscriptionID, MonitoringMode: ua.MonitoringModeDisabled, MonitoredItemIDs: []uint32{424242, live}}, func(v ua.Response) error {
		if r, ok := v.(*ua.SetMonitoringModeResponse); ok {
			modeResults = r.Results
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(modeResults) != 2 || modeResults[0] != ua.StatusBadMonitoredItemIDInvalid || modeResults[1] != ua.StatusGood {
		t.Errorf("SetMonitoringMode(bogus, live) = %v", modeResults)
	}
	if err := c.Send(ctx, &ua.DeleteMonitoredItemsRequest{RequestHeader: &ua.RequestHeader{}, SubscriptionID: sub.SubscriptionID, MonitoredItemIDs: []uint32{424242, quiet}}, func(v ua.Response) error {
		if r, ok := v.(*ua.DeleteMonitoredItemsResponse); ok {
			delResults = r.Results
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(delResults) != 2 || delResults[0] != ua.StatusBadMonitoredItemIDInvalid || delResults[1] != ua.StatusGood {
		t.Errorf("DeleteMonitoredItems(bogus, quiet) = %v", delResults)
	}
	// Another session's subscription is as invalid as none.
	if err := c.Send(ctx, &ua.SetMonitoringModeRequest{RequestHeader: &ua.RequestHeader{}, SubscriptionID: sub.SubscriptionID + 100, MonitoringMode: ua.MonitoringModeDisabled, MonitoredItemIDs: []uint32{live}}, func(ua.Response) error { return nil }); !errors.Is(err, ua.StatusBadSubscriptionIDInvalid) {
		t.Errorf("SetMonitoringMode on a subscription that is not there: %v", err)
	}
	if st := s.Status(); st.MonitoredItems != 1 {
		t.Errorf("%d items booked after deleting one of two", st.MonitoredItems)
	}
	if dv := read(t, c, s.NodeID("Level"), ua.AttributeIDValue); dv.Status != ua.StatusGood {
		t.Fatalf("the server after the bogus ids: %s", dv.Status)
	}
}

// F-26: creating monitored items on a subscription that is not draining
// its channel — one whose goroutine has died, here, as a stalled client's
// does — returns, and leaves the monitored-item lock free for the sweep
// and for Status. gopcua sent each item's first value from a goroutine
// holding that lock, blocked on the full channel for good, and the server
// froze with the process up.
func TestCreateMonitoredItemsNeverBlocksOnAStalledSubscription(t *testing.T) {
	rt, _ := newRuntime(t)
	s, c := startServer(t, rt)
	ctx := context.Background()
	notify := make(chan *opcua.PublishNotificationData, 1)
	sub, err := c.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: 20 * time.Millisecond}, notify)
	if err != nil {
		t.Fatal(err)
	}
	subs := s.srv.SubscriptionService
	subs.Mu.Lock()
	stalled := subs.Subs[sub.SubscriptionID]
	subs.Mu.Unlock()
	if stalled == nil {
		t.Fatal("subscription not booked")
	}
	subs.DeleteSubscription(sub.SubscriptionID) // its goroutine is gone…
	time.Sleep(200 * time.Millisecond)          // (and has run its own deferred delete)
	subs.Mu.Lock()
	subs.Subs[sub.SubscriptionID] = stalled // …but the client's requests still find it
	subs.Mu.Unlock()
	for len(stalled.NotifyChannel) < cap(stalled.NotifyChannel) {
		stalled.NotifyChannel <- &ua.MonitoredItemNotification{}
	}

	var items []*ua.MonitoredItemCreateRequest
	for i := 0; i < 150; i++ { // ns=0: the path that sent under the lock
		items = append(items, opcua.NewMonitoredItemCreateRequestWithDefaults(ua.NewNumericNodeID(0, id.Server_ServerStatus_CurrentTime), ua.AttributeIDValue, uint32(i+1)))
	}
	items = append(items, opcua.NewMonitoredItemCreateRequestWithDefaults(s.NodeID("Level"), ua.AttributeIDValue, 1000))
	done := make(chan error, 1)
	go func() {
		resp, err := sub.Monitor(ctx, ua.TimestampsToReturnBoth, items...)
		if err == nil && len(resp.Results) != len(items) {
			err = fmt.Errorf("%d results for %d items", len(resp.Results), len(items))
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CreateMonitoredItems did not return")
	}
	status := make(chan Status, 1)
	go func() { status <- s.Status() }()
	select {
	case st := <-status:
		if st.MonitoredItems != len(items) {
			t.Errorf("%d items booked, want %d", st.MonitoredItems, len(items))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Status is blocked on the monitored-item lock")
	}
	// The sweep is alive too: a second client hears a change.
	c2, err := opcua.NewClient(s.URL(), opcua.SecurityMode(ua.MessageSecurityModeNone), opcua.AuthAnonymous(), opcua.AutoReconnect(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := c2.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer c2.Close(ctx)
	notify2 := make(chan *opcua.PublishNotificationData, 16)
	sub2, err := c2.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: 20 * time.Millisecond}, notify2)
	if err != nil {
		t.Fatal(err)
	}
	defer sub2.Cancel(ctx)
	if _, err := sub2.Monitor(ctx, ua.TimestampsToReturnBoth, opcua.NewMonitoredItemCreateRequestWithDefaults(s.NodeID("Level"), ua.AttributeIDValue, 7)); err != nil {
		t.Fatal(err)
	}
	rt.Tags().Set("Level", 9.75)
	deadline := time.After(2 * time.Second)
	for {
		select {
		case n := <-notify2:
			if dc, ok := n.Value.(*ua.DataChangeNotification); ok {
				for _, m := range dc.MonitoredItems {
					if m.ClientHandle == 7 && m.Value.Value.Value() == 9.75 {
						return
					}
				}
			}
		case <-deadline:
			t.Fatal("the sweep is not delivering")
		}
	}
}

// A new item hears the current value once, from the sweep, and nothing
// more until the value moves.
func TestNewItemHearsTheCurrentValueOnce(t *testing.T) {
	rt, _ := newRuntime(t)
	s, c := startServer(t, rt)
	ctx := context.Background()
	notify := make(chan *opcua.PublishNotificationData, 64)
	sub, err := c.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: 20 * time.Millisecond}, notify)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel(ctx)
	if _, err := sub.Monitor(ctx, ua.TimestampsToReturnBoth,
		opcua.NewMonitoredItemCreateRequestWithDefaults(s.NodeID("Level"), ua.AttributeIDValue, 1),
		opcua.NewMonitoredItemCreateRequestWithDefaults(ua.NewNumericNodeID(0, id.Server_ServerStatus_State), ua.AttributeIDValue, 2),
	); err != nil {
		t.Fatal(err)
	}
	got := count(notify, 400*time.Millisecond)
	if got[1] != 1 || got[2] != 1 {
		t.Errorf("initial values: Level %d times, ServerStatus.State %d times; want once each", got[1], got[2])
	}
}

// F-32: a monitored item's IndexRange means what a Read's does: the item
// is told its slice, only when the slice changes, and a range the value
// does not reach, or one that is not a range, refuses the item when it is
// created. The item was created Good for any range and told the whole
// array.
func TestMonitoredItemHonoursIndexRange(t *testing.T) {
	rt, _ := newRuntime(t)
	s, c := startServer(t, rt)
	ctx := context.Background()
	notify := make(chan *opcua.PublishNotificationData, 64)
	sub, err := c.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: 20 * time.Millisecond}, notify)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel(ctx)
	ranged := func(rng string, handle uint32) *ua.MonitoredItemCreateRequest {
		req := opcua.NewMonitoredItemCreateRequestWithDefaults(s.NodeID("Levels"), ua.AttributeIDValue, handle)
		req.ItemToMonitor.IndexRange = rng
		return req
	}
	resp, err := sub.Monitor(ctx, ua.TimestampsToReturnBoth, ranged("1", 1), ranged("0:1", 2), ranged("3", 3), ranged("x", 4),
		ranged("0,0", 5), opcua.NewMonitoredItemCreateRequestWithDefaults(ua.NewNumericNodeID(0, id.Server_NamespaceArray), ua.AttributeIDValue, 6),
		ranged("1:9", 7)) // F-35: an upper bound past the end is the partial result
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []ua.StatusCode{ua.StatusGood, ua.StatusGood, ua.StatusBadIndexRangeNoData, ua.StatusBadIndexRangeInvalid, ua.StatusBadIndexRangeNoData, ua.StatusGood, ua.StatusGood} {
		if got := resp.Results[i].StatusCode; got != want {
			t.Errorf("item %d: %s, want %s", i+1, got, want)
		}
	}
	values := func(d time.Duration) map[uint32][]any {
		got := map[uint32][]any{}
		deadline := time.After(d)
		for {
			select {
			case n := <-notify:
				if dc, ok := n.Value.(*ua.DataChangeNotification); ok {
					for _, m := range dc.MonitoredItems {
						got[m.ClientHandle] = append(got[m.ClientHandle], m.Value.Value.Value())
					}
				}
			case <-deadline:
				return got
			}
		}
	}
	got := values(300 * time.Millisecond)
	if !reflect.DeepEqual(got[1], []any{[]float64{2}}) || !reflect.DeepEqual(got[2], []any{[]float64{1, 2}}) {
		t.Errorf("initial values: Levels[1] told %v, Levels[0:1] told %v", got[1], got[2])
	}
	if len(got[6]) != 1 {
		t.Errorf("the Server-object item was told %v", got[6])
	}
	if !reflect.DeepEqual(got[7], []any{[]float64{2, 3}}) {
		t.Errorf("initial value: Levels[1:9] told %v, want [[2 3]]", got[7])
	}
	// A move inside the slice is heard as the slice; one outside it is not heard.
	if err := rt.Tags().SetPath("Levels[1]", 5.0); err != nil {
		t.Fatal(err)
	}
	got = values(300 * time.Millisecond)
	if !reflect.DeepEqual(got[1], []any{[]float64{5}}) || !reflect.DeepEqual(got[2], []any{[]float64{1, 5}}) {
		t.Errorf("after Levels[1] = 5: Levels[1] told %v, Levels[0:1] told %v", got[1], got[2])
	}
	if err := rt.Tags().SetPath("Levels[2]", 9.0); err != nil {
		t.Fatal(err)
	}
	got = values(300 * time.Millisecond)
	if len(got[1]) != 0 || len(got[2]) != 0 {
		t.Errorf("after Levels[2] = 9: Levels[1] told %v, Levels[0:1] told %v; neither slice moved", got[1], got[2])
	}
	if dv := read(t, c, s.NodeID("Levels"), ua.AttributeIDValue); !reflect.DeepEqual(dv.Value.Value(), []float64{1, 5, 9}) {
		t.Errorf("Levels = %v", dv.Value.Value())
	}
}
