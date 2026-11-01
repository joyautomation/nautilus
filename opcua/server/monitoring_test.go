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
	// ModifySubscription (asyncua does); it is not refused, and its request
	// is revised exactly as CreateSubscription's is (#433).
	for _, m := range []struct {
		ask                 float64
		lifetime, keepalive uint32
		want                float64
		wantLifetime        uint32
	}{
		{0, 1, 10, sweep, 30},
		{math.NaN(), 60, 20, sweep, 60},
		{5000, 60, 20, 5000, 60},
		{1e300, 100, 20, maxPublishingInterval, 100},
	} {
		var mod *ua.ModifySubscriptionResponse
		err = c.Send(ctx, &ua.ModifySubscriptionRequest{RequestHeader: &ua.RequestHeader{}, SubscriptionID: resp.SubscriptionID, RequestedPublishingInterval: m.ask, RequestedLifetimeCount: m.lifetime, RequestedMaxKeepAliveCount: m.keepalive}, func(v ua.Response) error {
			mod, _ = v.(*ua.ModifySubscriptionResponse)
			return nil
		})
		if err != nil || mod == nil {
			t.Fatalf("ModifySubscription(%v): %v", m.ask, err)
		}
		if mod.RevisedPublishingInterval != m.want || mod.RevisedLifetimeCount != m.wantLifetime || mod.RevisedMaxKeepAliveCount != m.keepalive {
			t.Errorf("ModifySubscription(%v/%d/%d) granted %v/%d/%d, want %v/%d/%d", m.ask, m.lifetime, m.keepalive,
				mod.RevisedPublishingInterval, mod.RevisedLifetimeCount, mod.RevisedMaxKeepAliveCount, m.want, m.wantLifetime, m.keepalive)
		}
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

// A subscription id is never issued twice (#432). gopcua numbered them
// len(Subs)+1, so with 1 and 2 live and 1 deleted the next was 2 again: it
// replaced the live entry, and the cancelled subscription's own cleanup,
// which deletes its id when its task returns, then took the new one with
// it, so the client's next CreateMonitoredItems was refused.
func TestSubscriptionIDsAreNeverReused(t *testing.T) {
	rt, _ := newRuntime(t)
	s, c := startServer(t, rt)
	ctx := context.Background()
	subscribe := func() *opcua.Subscription {
		t.Helper()
		sub, err := c.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: 20 * time.Millisecond}, make(chan *opcua.PublishNotificationData, 64))
		if err != nil {
			t.Fatal(err)
		}
		return sub
	}
	a, b := subscribe(), subscribe()
	if err := a.Cancel(ctx); err != nil {
		t.Fatal(err)
	}
	n := subscribe()
	if n.SubscriptionID == a.SubscriptionID || n.SubscriptionID == b.SubscriptionID {
		t.Fatalf("ids a=%d b=%d, then new=%d: an id was issued again", a.SubscriptionID, b.SubscriptionID, n.SubscriptionID)
	}
	// Give a's task time to return and run its cleanup, then use both live
	// subscriptions: neither may have gone with it.
	time.Sleep(200 * time.Millisecond)
	for i, sub := range []*opcua.Subscription{b, n} {
		res, err := sub.Monitor(ctx, ua.TimestampsToReturnBoth,
			opcua.NewMonitoredItemCreateRequestWithDefaults(s.NodeID("Level"), ua.AttributeIDValue, uint32(i+1)))
		if err != nil {
			t.Fatalf("subscription %d: %v", sub.SubscriptionID, err)
		}
		if st := res.Results[0].StatusCode; st != ua.StatusGood {
			t.Fatalf("subscription %d: item %s", sub.SubscriptionID, st)
		}
	}
	if st := s.Status(); st.Subscriptions != 2 || st.MonitoredItems != 2 {
		t.Errorf("status %+v, want 2 subscriptions and 2 items", st)
	}
}

// CloseSession honours deleteSubscriptions (Part 4 §5.6.4, #422): TRUE
// deletes the session's subscriptions and their items before it answers;
// FALSE leaves them to their own lifetime. gopcua ignored the flag, so a
// client that closed its session left its subscriptions running, counted
// on the driver row, until their lifetime ran out.
func TestCloseSessionHonoursDeleteSubscriptions(t *testing.T) {
	for _, del := range []bool{true, false} {
		t.Run(fmt.Sprintf("deleteSubscriptions=%v", del), func(t *testing.T) {
			rt, _ := newRuntime(t)
			s, c := startServer(t, rt)
			ctx := context.Background()
			sub, err := c.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: 20 * time.Millisecond, LifetimeCount: 6, MaxKeepAliveCount: 2}, make(chan *opcua.PublishNotificationData, 64))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := sub.Monitor(ctx, ua.TimestampsToReturnBoth,
				opcua.NewMonitoredItemCreateRequestWithDefaults(s.NodeID("Level"), ua.AttributeIDValue, 1),
				opcua.NewMonitoredItemCreateRequestWithDefaults(s.NodeID("Count"), ua.AttributeIDValue, 2)); err != nil {
				t.Fatal(err)
			}
			if st := s.Status(); st.Subscriptions != 1 || st.MonitoredItems != 2 {
				t.Fatalf("before close %+v", st)
			}
			if err := c.Send(ctx, &ua.CloseSessionRequest{DeleteSubscriptions: del}, func(r ua.Response) error { return nil }); err != nil {
				t.Fatal(err)
			}
			st := s.Status()
			if del {
				if st.Subscriptions != 0 || st.MonitoredItems != 0 {
					t.Fatalf("after CloseSession(deleteSubscriptions=true) %+v, want nothing left", st)
				}
				return
			}
			if st.Subscriptions != 1 || st.MonitoredItems != 2 {
				t.Fatalf("after CloseSession(deleteSubscriptions=false) %+v, want the subscription kept", st)
			}
			// Its lifetime then ends it, once nothing publishes for it. (The
			// inherited publish loop counts the lifetime once per keep-alive
			// period, not per publishing interval: 6 × 2 × 20 ms here.)
			deadline := time.Now().Add(5 * time.Second)
			for st := s.Status(); st.Subscriptions != 0 || st.MonitoredItems != 0; st = s.Status() {
				if time.Now().After(deadline) {
					t.Fatalf("kept subscription never expired: %+v", st)
				}
				time.Sleep(20 * time.Millisecond)
			}
		})
	}
}

// Cancelling a subscription while its items' first values are still owed
// must not stop the server. Ported from gopcua PR #929's end-to-end test,
// which caught the inherited stack deadlocking its one message loop here;
// through nautilus the items on a Server-object node are each owed their
// one value by a goroutine of createMonitoredItems, more of them than the
// subscription's channel holds. The services are sent raw: gopcua's
// client's own subscription bookkeeping (its publish loop pausing and
// resuming as subscriptions come and go) can hang in the client, which is
// not what this test is about.
func TestServerAnswersAfterASubscriptionIsCancelledWithValuesOwed(t *testing.T) {
	rt, _ := newRuntime(t)
	s, c := startServer(t, rt)
	ctx := context.Background()
	node := ua.NewNumericNodeID(0, id.Server_ServerStatus_CurrentTime)
	items := make([]*ua.MonitoredItemCreateRequest, 150)
	for i := range items {
		items[i] = opcua.NewMonitoredItemCreateRequestWithDefaults(node, ua.AttributeIDValue, uint32(i+1))
	}
	send := func(round int, what string, req ua.Request) ua.Response {
		t.Helper()
		sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		var resp ua.Response
		if err := c.Send(sctx, req, func(r ua.Response) error { resp = r; return nil }); err != nil {
			t.Fatalf("round %d: %s: the server stopped answering: %v", round, what, err)
		}
		return resp
	}
	createSub := func(round int) uint32 {
		t.Helper()
		r := send(round, "CreateSubscription", &ua.CreateSubscriptionRequest{RequestedPublishingInterval: 100, RequestedLifetimeCount: 60, RequestedMaxKeepAliveCount: 20, PublishingEnabled: true})
		return r.(*ua.CreateSubscriptionResponse).SubscriptionID
	}
	for round := range 10 {
		subID := createSub(round)
		send(round, "CreateMonitoredItems", &ua.CreateMonitoredItemsRequest{SubscriptionID: subID, TimestampsToReturn: ua.TimestampsToReturnBoth, ItemsToCreate: items})
		send(round, "DeleteSubscriptions", &ua.DeleteSubscriptionsRequest{SubscriptionIDs: []uint32{subID}})
		pctx, pcancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := opcua.GetEndpoints(pctx, s.URL())
		pcancel()
		if err != nil {
			t.Fatalf("round %d: the server stopped answering after a subscription was cancelled: %v", round, err)
		}
	}
	// The deadlock showed when the loop reached the next CreateSubscription.
	createSub(10)
}

// Disabling an item during a burst of changes stops it at once: a value it
// had queued but not yet published is dropped, not sent after the client
// was told Good (Part 4 §5.12.1.3; black-box F-27, which failed once in CI
// on one late notification). The publishing interval is long so a queued
// value sits for up to 600 ms before it would go out; the 100 ms after
// Good is for what was already on the wire.
func TestDisablingDropsWhatTheItemHadQueued(t *testing.T) {
	rt, _ := newRuntime(t)
	s, c := startServer(t, rt)
	ctx := context.Background()
	stop := make(chan struct{})
	defer close(stop)
	go func() { // Count moves every 2 ms
		var n int64
		for {
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
				n++
				rt.Tags().Set("Count", n)
			}
		}
	}()
	notify := make(chan *opcua.PublishNotificationData, 256)
	sub, err := c.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: 600 * time.Millisecond}, notify)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel(ctx)
	resp, err := sub.Monitor(ctx, ua.TimestampsToReturnBoth, opcua.NewMonitoredItemCreateRequestWithDefaults(s.NodeID("Count"), ua.AttributeIDValue, 1))
	if err != nil || resp.Results[0].StatusCode != ua.StatusGood {
		t.Fatalf("monitor: %v %v", err, resp)
	}
	item := resp.Results[0].MonitoredItemID
	for round := range 2 {
		// Wait for a publish, then let the burst queue values for a while.
		if got := count(notify, 900*time.Millisecond); got[1] == 0 {
			t.Fatalf("round %d: the Reporting item heard nothing", round)
		}
		time.Sleep(time.Duration(150+200*round) * time.Millisecond)
		mode, err := sub.SetMonitoringMode(ctx, ua.MonitoringModeDisabled, item)
		if err != nil || mode.Results[0] != ua.StatusGood {
			t.Fatalf("round %d: SetMonitoringMode(Disabled): %v %v", round, err, mode)
		}
		count(notify, 100*time.Millisecond)
		if got := count(notify, 800*time.Millisecond); got[1] != 0 {
			t.Fatalf("round %d: Disabled, yet %d notification(s) in the next 800 ms", round, got[1])
		}
		if _, err := sub.SetMonitoringMode(ctx, ua.MonitoringModeReporting, item); err != nil {
			t.Fatal(err)
		}
	}
}

// F-34 (#433): ModifySubscription changes the rate a live subscription
// publishes at, in both directions, and says so. It answered Good with the
// subscription's own values and changed nothing: a client slowing a busy
// subscription down was told Good and went on hearing it at the old rate.
// The tag moves every few milliseconds throughout, so every publishing
// cycle has a value to send and the count of notifications in a window is
// the number of cycles in it.
func TestModifySubscriptionChangesThePublishingRate(t *testing.T) {
	rt, _ := newRuntime(t)
	s, c := startServer(t, rt)
	ctx := context.Background()
	stop := make(chan struct{})
	moved := make(chan struct{})
	go func() {
		defer close(moved)
		for i := int64(0); ; i++ {
			select {
			case <-stop:
				return
			case <-time.After(3 * time.Millisecond):
				rt.Tags().Set("Count", i)
			}
		}
	}()
	defer func() { close(stop); <-moved }()

	notify := make(chan *opcua.PublishNotificationData, 256)
	sub, err := c.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: 20 * time.Millisecond}, notify)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel(ctx)
	if _, err := sub.Monitor(ctx, ua.TimestampsToReturnBoth,
		opcua.NewMonitoredItemCreateRequestWithDefaults(s.NodeID("Count"), ua.AttributeIDValue, 1),
	); err != nil {
		t.Fatal(err)
	}
	modify := func(ms float64) {
		t.Helper()
		var mod *ua.ModifySubscriptionResponse
		err := c.Send(ctx, &ua.ModifySubscriptionRequest{RequestHeader: &ua.RequestHeader{}, SubscriptionID: sub.SubscriptionID, RequestedPublishingInterval: ms, RequestedLifetimeCount: 300, RequestedMaxKeepAliveCount: 10}, func(v ua.Response) error {
			mod, _ = v.(*ua.ModifySubscriptionResponse)
			return nil
		})
		if err != nil || mod == nil {
			t.Fatalf("ModifySubscription(%v): %v", ms, err)
		}
		if mod.RevisedPublishingInterval != ms || mod.RevisedLifetimeCount != 300 || mod.RevisedMaxKeepAliveCount != 10 {
			t.Fatalf("ModifySubscription(%v) granted %v/%d/%d, want %v/300/10", ms, mod.RevisedPublishingInterval, mod.RevisedLifetimeCount, mod.RevisedMaxKeepAliveCount, ms)
		}
	}
	window := 1500 * time.Millisecond
	count(notify, 200*time.Millisecond) // the initial value, and the subscription settling
	fast := count(notify, window)[1]

	modify(300)                         // 1.5 s at 300 ms is 5 publishing cycles
	count(notify, 300*time.Millisecond) // publishes from the old rate still on their way
	slow := count(notify, window)[1]
	t.Logf("notifications in %v: %d at 20 ms, %d at 300 ms", window, fast, slow)
	if slow > 8 {
		t.Errorf("after ModifySubscription(300): %d notifications in %v, want at most 8 (5 or 6 cycles, and slack); before it, at 20 ms, %d", slow, window, fast)
	}
	if fast < 3*slow || fast < 20 {
		t.Errorf("at 20 ms: %d notifications in %v; at 300 ms: %d; want the first well above the second", fast, window, slow)
	}

	modify(20) // and back: the subscription speeds up again
	count(notify, 300*time.Millisecond)
	if again := count(notify, window)[1]; again < 3*slow || again < 20 {
		t.Errorf("after ModifySubscription(20): %d notifications in %v, want well above the %d at 300 ms", again, window, slow)
	}
}
