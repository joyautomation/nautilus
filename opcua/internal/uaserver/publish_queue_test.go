package uaserver

import (
	"context"
	"testing"
	"time"

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
	require.NoError(t, sc.SendRequest(ctx, &ua.CreateSubscriptionRequest{RequestedPublishingInterval: float64(time.Hour / time.Millisecond), RequestedLifetimeCount: 3, RequestedMaxKeepAliveCount: 1, PublishingEnabled: true}, tok, func(ua.Response) error { return nil }))

	type answer struct {
		handle uint32 // the request's handle
		resp   ua.Response
		err    error
	}
	publish := func() <-chan answer {
		done := make(chan answer, 1)
		go func() {
			req := &ua.PublishRequest{SubscriptionAcknowledgements: []*ua.SubscriptionAcknowledgement{}}
			var resp ua.Response
			err := sc.SendRequestWithTimeout(ctx, req, tok, time.Minute, func(r ua.Response) error { resp = r; return nil })
			done <- answer{handle: req.Header().RequestHandle, resp: resp, err: err}
		}()
		return done
	}
	queued := func(n int) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for len(sess.PublishRequests) != n {
			if time.Now().After(deadline) {
				t.Fatalf("%d publish requests queued, want %d", len(sess.PublishRequests), n)
			}
			time.Sleep(time.Millisecond)
		}
	}

	answers := make([]<-chan answer, 0, maxPublishRequests+1)
	for i := range maxPublishRequests {
		answers = append(answers, publish())
		queued(i + 1)
	}
	answers = append(answers, publish()) // one past the limit

	var oldest answer
	select {
	case oldest = <-answers[0]:
	case <-time.After(10 * time.Second):
		t.Fatalf("with %d publish requests outstanding, none was answered", maxPublishRequests+1)
	}
	require.Equal(t, ua.StatusBadTooManyPublishRequests, oldest.err)
	require.NotNil(t, oldest.resp, "the oldest request's answer")
	require.Equal(t, oldest.handle, oldest.resp.Header().RequestHandle, "the answer does not carry the request's handle")

	// The new request took the oldest's place: the queue is full again, with every request but
	// the oldest, in the order they were sent. Draining it here leaves them unanswered; the
	// test is ending.
	queued(maxPublishRequests)
	prev := oldest.handle
	for range maxPublishRequests {
		h := (<-sess.PublishRequests).Req.RequestHeader.RequestHandle
		require.Greater(t, h, prev, "queued requests out of order, or the oldest still queued")
		prev = h
	}
	for _, a := range answers[1:] {
		select {
		case a := <-a:
			t.Fatalf("request %d was answered: %v", a.handle, a.err)
		default:
		}
	}
}
