package uaserver

import (
	"context"
	"testing"
	"time"

	cuasc "github.com/gopcua/opcua/uasc"

	"github.com/gopcua/opcua/ua"
	"github.com/joyautomation/nautilus/opcua/internal/require"
)

// A session that keeps more Publish requests outstanding than the server queues has its oldest
// de-queued and answered with Bad_TooManyPublishRequests, carrying that request's handle, and
// the new one queued (Part 4 §5.14.5.1). The request past the limit was dropped with no answer,
// so the client waited on it until its own timeout. The requests go out raw, one at a time, each
// sent once the one before it is queued, so which is the oldest is known.
func TestPublishPastTheQueueAnswersTheOldestWithTooManyPublishRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	endpoint, srv := startTestServer(t)

	sc := channel(t, ctx, endpoint)
	tok := rawCreateSession(t, ctx, sc, endpoint)
	require.NoError(t, rawActivate(ctx, sc, tok))
	sess := srv.sb.Session(tok)
	require.NotNil(t, sess)

	// A subscription, so the session has one to publish for, whose publishing interval is
	// far longer than the test: its task never takes a request from the queue.
	rawIdleSubscription(t, ctx, sc, tok)

	answers := make([]<-chan publishAnswer, 0, maxPublishRequests+1)
	for i := range maxPublishRequests {
		answers = append(answers, sendPublish(ctx, sc, tok))
		waitQueued(t, sess, i+1)
	}
	answers = append(answers, sendPublish(ctx, sc, tok)) // one past the limit

	oldest := answerOf(t, answers[0])
	require.Equal(t, ua.StatusBadTooManyPublishRequests, oldest.err)
	require.NotNil(t, oldest.resp, "the oldest request's answer")
	require.Equal(t, oldest.handle, oldest.resp.Header().RequestHandle, "the answer does not carry the request's handle")

	// The new request took the oldest's place: the queue is full again, with every request but
	// the oldest, in the order they were sent. Draining it here leaves them unanswered; the
	// test is ending.
	waitQueued(t, sess, maxPublishRequests)
	prev := oldest.handle
	for range maxPublishRequests {
		h := takeQueued(t, sess).Req.RequestHeader.RequestHandle
		require.Greater(t, h, prev, "queued requests out of order, or the oldest still queued")
		prev = h
	}
	notAnswered(t, answers[1:]...)
}

// A Publish from a session that owns no subscription is answered at once with
// Bad_NoSubscription and its own handle (Part 4 §5.14.5.3, Table 90: "There is no Subscription
// available for this session"). It was queued, and nothing would ever take it off the queue, so
// the client waited on it until its own timeout. Once the session has a subscription, a Publish
// is queued for it.
func TestPublishWithNoSubscriptionIsAnsweredWithNoSubscription(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	endpoint, srv := startTestServer(t)

	sc := channel(t, ctx, endpoint)
	tok := rawCreateSession(t, ctx, sc, endpoint)
	require.NoError(t, rawActivate(ctx, sc, tok))
	sess := srv.sb.Session(tok)
	require.NotNil(t, sess)

	a := answerOf(t, sendPublish(ctx, sc, tok))
	require.Equal(t, ua.StatusBadNoSubscription, a.err)
	require.NotNil(t, a.resp, "the answer")
	require.Equal(t, a.handle, a.resp.Header().RequestHandle, "the answer does not carry the request's handle")
	require.Equal(t, 0, queuedPublishes(sess), "the request was queued as well")

	// The message loop handles the Publish after CreateSubscription has counted the
	// subscription, so it is queued.
	rawIdleSubscription(t, ctx, sc, tok)
	b := sendPublish(ctx, sc, tok)
	waitQueued(t, sess, 1)
	notAnswered(t, b)
}

// "If this is the last Subscription for the Session, then all Publish requests still queued for
// that Session are de-queued and shall be returned with Bad_NoSubscription" (Part 4 §5.14.8.1).
// Deleting one that is not the last leaves them queued. They were left on the queue, with
// nothing to take them off it.
func TestDeletingTheLastSubscriptionAnswersQueuedPublishesWithNoSubscription(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	endpoint, srv := startTestServer(t)

	sc := channel(t, ctx, endpoint)
	tok := rawCreateSession(t, ctx, sc, endpoint)
	require.NoError(t, rawActivate(ctx, sc, tok))
	sess := srv.sb.Session(tok)
	require.NotNil(t, sess)

	first, second := rawIdleSubscription(t, ctx, sc, tok), rawIdleSubscription(t, ctx, sc, tok)
	var answers []<-chan publishAnswer
	for i := range 3 {
		answers = append(answers, sendPublish(ctx, sc, tok))
		waitQueued(t, sess, i+1)
	}

	rawDeleteSubscription(t, ctx, sc, tok, first)
	require.Equal(t, 3, queuedPublishes(sess), "deleting a subscription that was not the last de-queued requests")
	notAnswered(t, answers...)

	rawDeleteSubscription(t, ctx, sc, tok, second)
	for _, ch := range answers {
		a := answerOf(t, ch)
		require.Equal(t, ua.StatusBadNoSubscription, a.err)
		require.NotNil(t, a.resp, "the answer")
		require.Equal(t, a.handle, a.resp.Header().RequestHandle, "the answer does not carry the request's handle")
	}
	require.Equal(t, 0, queuedPublishes(sess), "requests still queued")

	a := answerOf(t, sendPublish(ctx, sc, tok))
	require.Equal(t, ua.StatusBadNoSubscription, a.err, "a Publish after the last subscription was deleted")
}

// A session whose last subscription is transferred away is told of it in answer to one queued
// Publish (Part 4 §5.14.7); the requests it still has queued after that have nothing left to
// answer them, and are answered with Bad_NoSubscription, as when the last one is deleted
// (§5.14.8.1) and as open62541 answers them. Transfer is called directly, with both sessions
// marked as one signed client application: this package has no TransferSubscriptions handler
// and its test server signs nothing.
func TestTransferringTheLastSubscriptionAwayAnswersTheRestWithNoSubscription(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	endpoint, srv := startTestServer(t)

	sc := channel(t, ctx, endpoint)
	fromTok := rawCreateSession(t, ctx, sc, endpoint)
	require.NoError(t, rawActivate(ctx, sc, fromTok))
	toTok := rawCreateSession(t, ctx, sc, endpoint)
	require.NoError(t, rawActivate(ctx, sc, toTok))
	from, to := srv.sb.Session(fromTok), srv.sb.Session(toTok)
	require.NotNil(t, from)
	require.NotNil(t, to)
	// isActivated takes the session's lock, which ActivateSession released after writing the
	// identity: what the message loop wrote is seen here.
	require.True(t, from.isActivated() && to.isActivated(), "a session is not activated")
	for _, s := range []*session{from, to} {
		s.secured, s.applicationURI = true, "urn:example:synthetic-client"
	}

	subID := rawIdleSubscription(t, ctx, sc, fromTok)
	var answers []<-chan publishAnswer
	for i := range 3 {
		answers = append(answers, sendPublish(ctx, sc, fromTok))
		waitQueued(t, from, i+1)
	}

	_, code := srv.SubscriptionService.Transfer(to.channel, &ua.RequestHeader{AuthenticationToken: toTok}, subID)
	require.Equal(t, ua.StatusOK, code)

	// The oldest carries the notice; the other two, Bad_NoSubscription.
	notice := answerOf(t, answers[0])
	require.NoError(t, notice.err)
	pr, ok := notice.resp.(*ua.PublishResponse)
	require.True(t, ok, "the notice is a %T", notice.resp)
	require.Equal(t, subID, pr.SubscriptionID)
	require.Equal(t, 1, len(pr.NotificationMessage.NotificationData))
	st, ok := pr.NotificationMessage.NotificationData[0].Value.(*ua.StatusChangeNotification)
	require.True(t, ok, "the notice carries a %T", pr.NotificationMessage.NotificationData[0].Value)
	require.Equal(t, ua.StatusGoodSubscriptionTransferred, st.Status)
	for _, ch := range answers[1:] {
		a := answerOf(t, ch)
		require.Equal(t, ua.StatusBadNoSubscription, a.err)
		require.Equal(t, a.handle, a.resp.Header().RequestHandle, "the answer does not carry the request's handle")
	}
	require.Equal(t, 0, queuedPublishes(from), "requests still queued on the old session")

	// The new owner's Publish is queued for it.
	b := sendPublish(ctx, sc, toTok)
	waitQueued(t, to, 1)
	notAnswered(t, b)
}

// "But a Server shall accept more queued Publish requests than created Subscriptions" (Part 4
// §5.14.5.1). A session with maxPublishRequests subscriptions or more may queue one more
// request than it has subscriptions, as the OPC Foundation .NET stack allows; past that the
// oldest is still de-queued with Bad_TooManyPublishRequests. The queue held maxPublishRequests,
// so the session's next request de-queued one that a subscription may have been waiting on.
func TestPublishQueueGrowsWithTheSessionsSubscriptions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	endpoint, srv := startTestServer(t)

	sc := channel(t, ctx, endpoint)
	tok := rawCreateSession(t, ctx, sc, endpoint)
	require.NoError(t, rawActivate(ctx, sc, tok))
	sess := srv.sb.Session(tok)
	require.NotNil(t, sess)

	const subs = maxPublishRequests + 1
	for range subs {
		rawIdleSubscription(t, ctx, sc, tok)
	}
	limit := subs + 1
	answers := make([]<-chan publishAnswer, 0, limit+1)
	for i := range limit {
		answers = append(answers, sendPublish(ctx, sc, tok))
		waitQueued(t, sess, i+1)
	}
	notAnswered(t, answers...)

	answers = append(answers, sendPublish(ctx, sc, tok)) // one past the limit
	oldest := answerOf(t, answers[0])
	require.Equal(t, ua.StatusBadTooManyPublishRequests, oldest.err)
	require.Equal(t, oldest.handle, oldest.resp.Header().RequestHandle, "the answer does not carry the request's handle")
	waitQueued(t, sess, limit)
	notAnswered(t, answers[1:]...)
}

// publishAnswer is what came back for a raw Publish request.
type publishAnswer struct {
	handle uint32 // the request's handle
	resp   ua.Response
	err    error
}

// sendPublish sends one raw Publish on sc for the session tok names and returns where its
// answer arrives.
func sendPublish(ctx context.Context, sc *cuasc.SecureChannel, tok *ua.NodeID) <-chan publishAnswer {
	done := make(chan publishAnswer, 1)
	go func() {
		req := &ua.PublishRequest{SubscriptionAcknowledgements: []*ua.SubscriptionAcknowledgement{}}
		var resp ua.Response
		err := sc.SendRequestWithTimeout(ctx, req, tok, time.Minute, func(r ua.Response) error { resp = r; return nil })
		done <- publishAnswer{handle: req.Header().RequestHandle, resp: resp, err: err}
	}()
	return done
}

// answerOf waits for a Publish's answer.
func answerOf(t *testing.T, ch <-chan publishAnswer) publishAnswer {
	t.Helper()
	select {
	case a := <-ch:
		return a
	case <-time.After(10 * time.Second):
		t.Fatal("a publish request was not answered")
		return publishAnswer{}
	}
}

// notAnswered fails the test if any of the Publishes has been answered.
func notAnswered(t *testing.T, chs ...<-chan publishAnswer) {
	t.Helper()
	for _, ch := range chs {
		select {
		case a := <-ch:
			t.Fatalf("request %d was answered: %v", a.handle, a.err)
		default:
		}
	}
}

// waitQueued waits until sess has n Publish requests queued. A request is queued on the message
// loop, after the client has sent it, so this polls.
func waitQueued(t *testing.T, sess *session, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for queuedPublishes(sess) != n {
		if time.Now().After(deadline) {
			t.Fatalf("%d publish requests queued, want %d", queuedPublishes(sess), n)
		}
		time.Sleep(time.Millisecond)
	}
}

// queuedPublishes is how many Publish requests sess has queued.
func queuedPublishes(sess *session) int {
	return sess.queuedPublishes()
}

// takeQueued de-queues sess's oldest Publish request.
func takeQueued(t *testing.T, sess *session) PubReq {
	t.Helper()
	pr, ok := sess.takePublish()
	require.True(t, ok, "no publish request queued")
	return pr
}

// rawIdleSubscription creates a subscription with a raw request and returns its id. Its
// publishing interval is far longer than any test, so its task never takes a Publish request
// from the queue.
func rawIdleSubscription(t *testing.T, ctx context.Context, sc *cuasc.SecureChannel, tok *ua.NodeID) uint32 {
	t.Helper()
	var id uint32
	require.NoError(t, sc.SendRequest(ctx, &ua.CreateSubscriptionRequest{RequestedPublishingInterval: float64(time.Hour / time.Millisecond), RequestedLifetimeCount: 3, RequestedMaxKeepAliveCount: 1, PublishingEnabled: true}, tok, func(r ua.Response) error {
		id = r.(*ua.CreateSubscriptionResponse).SubscriptionID
		return nil
	}))
	return id
}

// rawDeleteSubscription deletes subscription id with a raw request.
func rawDeleteSubscription(t *testing.T, ctx context.Context, sc *cuasc.SecureChannel, tok *ua.NodeID, id uint32) {
	t.Helper()
	var results []ua.StatusCode
	require.NoError(t, sc.SendRequest(ctx, &ua.DeleteSubscriptionsRequest{SubscriptionIDs: []uint32{id}}, tok, func(r ua.Response) error {
		results = r.(*ua.DeleteSubscriptionsResponse).Results
		return nil
	}))
	require.Equal(t, []ua.StatusCode{ua.StatusOK}, results)
}
