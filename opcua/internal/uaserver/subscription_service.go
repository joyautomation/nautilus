package uaserver

import (
	"context"
	"sync"
	"time"

	"github.com/gopcua/opcua/ua"
	"github.com/joyautomation/nautilus/opcua/internal/uasc"
)

// SubscriptionService implements the Subscription Service Set.
//
// https://reference.opcfoundation.org/Core/Part4/v105/docs/5.13
type SubscriptionService struct {
	srv *Server
	// pub sub stuff
	Mu   sync.Mutex
	Subs map[uint32]*Subscription

	// lastID is the most recently issued subscription ID, guarded by Mu. IDs are never reused:
	// a deleted subscription's task can still call DeleteSubscription with its ID afterwards.
	lastID uint32
}

// get rid of all references to a subscription and all monitored items that are pointed at this subscription.
func (s *SubscriptionService) DeleteSubscription(id uint32) {
	s.Mu.Lock()
	defer s.Mu.Unlock()

	sub, ok := s.Subs[id]
	if ok {
		sub.Mu.Lock()
		if sub.running {
			sub.running = false
			close(sub.shutdown)
		}
		sub.Mu.Unlock()
	}

	delete(s.Subs, id)

	// ask the monitored item service to purge out any items that use this subscription
	s.srv.MonitoredItemService.DeleteSub(id)

}

// DeleteSessionSubscriptions deletes every subscription sess owns, and their
// monitored items, before it returns: CloseSession with deleteSubscriptions
// set. The ids are collected under the lock and deleted after it, since
// DeleteSubscription takes the lock itself.
func (s *SubscriptionService) DeleteSessionSubscriptions(sess *session) {
	if sess == nil || sess.AuthTokenID == nil {
		return
	}
	token := sess.AuthTokenID.String()
	s.Mu.Lock()
	var ids []uint32
	for id, sub := range s.Subs {
		if sub != nil && sub.ownedByToken(token) {
			ids = append(ids, id)
		}
	}
	s.Mu.Unlock()
	for _, id := range ids {
		s.DeleteSubscription(id)
	}
}

// https://reference.opcfoundation.org/Core/Part4/v105/docs/5.13.2
func (s *SubscriptionService) CreateSubscription(sc *uasc.SecureChannel, r ua.Request, reqID uint32) (ua.Response, error) {
	if s.srv.cfg.logger != nil {
		s.srv.cfg.logger.Debug("Handling %T", r)
	}

	req, err := safeReq[*ua.CreateSubscriptionRequest](r)
	if err != nil {
		return nil, err
	}

	s.Mu.Lock()
	defer s.Mu.Unlock()

	// Not len(s.Subs)+1: after a deletion that reuses an ID, possibly one that is still live,
	// and the old subscription's cleanup (DeleteSubscription from its task) then deletes the
	// new one.
	s.lastID++
	if s.lastID == 0 {
		s.lastID = 1
	}
	newsubid := s.lastID

	if s.srv.cfg.logger != nil {
		s.srv.cfg.logger.Info("New Sub %d for %v", newsubid, sc.RemoteAddr())
	}

	sub := NewSubscription()
	sub.srv = s
	sub.Session = s.srv.Session(r.Header())
	if sub.Session == nil {
		return nil, ua.StatusBadSessionIDInvalid // the gate checked; this is defensive
	}
	sub.Channel = sc
	sub.ID = newsubid
	sub.RevisedPublishingInterval = req.RequestedPublishingInterval
	sub.RevisedLifetimeCount = req.RequestedLifetimeCount
	sub.RevisedMaxKeepAliveCount = req.RequestedMaxKeepAliveCount
	sub.publishingEnabled = req.PublishingEnabled

	s.Subs[newsubid] = sub
	sub.running = true
	sub.Start()

	resp := &ua.CreateSubscriptionResponse{
		ResponseHeader: &ua.ResponseHeader{
			Timestamp:          time.Now(),
			RequestHandle:      req.RequestHeader.RequestHandle,
			ServiceResult:      ua.StatusOK,
			ServiceDiagnostics: &ua.DiagnosticInfo{},
			StringTable:        []string{},
			AdditionalHeader:   ua.NewExtensionObject(nil),
		},
		SubscriptionID:            uint32(newsubid),
		RevisedPublishingInterval: req.RequestedPublishingInterval,
		RevisedLifetimeCount:      req.RequestedLifetimeCount,
		RevisedMaxKeepAliveCount:  req.RequestedMaxKeepAliveCount,
	}
	return resp, nil
}

// https://reference.opcfoundation.org/Core/Part4/v105/docs/5.13.5
func (s *SubscriptionService) Publish(sc *uasc.SecureChannel, r ua.Request, reqID uint32) (ua.Response, error) {
	if s.srv.cfg.logger != nil {
		s.srv.cfg.logger.Debug("Raw Publish req")
	}

	req, err := safeReq[*ua.PublishRequest](r)
	if err != nil {
		if s.srv.cfg.logger != nil {
			s.srv.cfg.logger.Error("ERROR: bad PublishRequest Struct")
		}
		return nil, err
	}

	session := s.srv.Session(req.RequestHeader)

	if session == nil {
		response := &ua.PublishResponse{
			ResponseHeader: &ua.ResponseHeader{
				Timestamp:          time.Now(),
				RequestHandle:      req.RequestHeader.RequestHandle,
				ServiceResult:      ua.StatusBadSessionIDInvalid,
				ServiceDiagnostics: &ua.DiagnosticInfo{},
				StringTable:        []string{},
				AdditionalHeader:   ua.NewExtensionObject(nil),
			},
			SubscriptionID:           0,
			MoreNotifications:        false,
			NotificationMessage:      &ua.NotificationMessage{NotificationData: []*ua.ExtensionObject{}},
			AvailableSequenceNumbers: []uint32{}, // an empty array indicates taht we don't support retransmission of messages
			Results:                  []ua.StatusCode{},
			DiagnosticInfos:          []*ua.DiagnosticInfo{},
		}

		return response, nil
	}

	// A subscription moved to another session is news this session is owed first.
	if notice := s.noticeFor(session, req.RequestHeader); notice != nil {
		return notice, nil
	}

	select {
	case session.PublishRequests <- PubReq{Req: req, ID: reqID}:
	default:
		if s.srv.cfg.logger != nil {
			s.srv.cfg.logger.Warn("Too many publish reqs.")
		}
	}

	// per opcua spec, we don't respond now.  When data is available on the subscription,
	// the Subscription will respond in the background.
	return nil, nil
}

// https://reference.opcfoundation.org/Core/Part4/v105/docs/5.13.6
func (s *SubscriptionService) Republish(sc *uasc.SecureChannel, r ua.Request, reqID uint32) (ua.Response, error) {
	if s.srv.cfg.logger != nil {
		s.srv.cfg.logger.Debug("Handling %T", r)
	}

	req, err := safeReq[*ua.RepublishRequest](r)
	if err != nil {
		return nil, err
	}
	return serviceUnsupported(req.RequestHeader), nil
}

// Transfer moves the subscription id to the session that sent hdr, on its channel sc: the
// TransferSubscriptions service for one id (Part 4 §5.14.7). The subscription must exist
// (Bad_SubscriptionIdInvalid), not be the session's already (Bad_NothingToDo), and the
// session must act for the same user as the one that owns it (Bad_UserAccessDenied; see
// mayTransfer). The publish loop takes the change between publishes, as it takes a
// ModifySubscription; the old session, when it is still open, is told (tellTransferred).
// What it returns is the subscription, now the new session's, to resend initial values to.
func (s *SubscriptionService) Transfer(sc *uasc.SecureChannel, hdr *ua.RequestHeader, id uint32) (*Subscription, ua.StatusCode) {
	to := s.srv.Session(hdr)
	if to == nil {
		return nil, ua.StatusBadSessionIDInvalid // the gate checked; this is defensive
	}
	s.Mu.Lock()
	sub := s.Subs[id]
	s.Mu.Unlock()
	if sub == nil {
		return nil, ua.StatusBadSubscriptionIDInvalid
	}
	from, _ := sub.Owner()
	if from == to {
		return nil, ua.StatusBadNothingToDo
	}
	if code := mayTransfer(from, to); code != ua.StatusOK {
		return nil, code
	}
	// The old session hears of the move only while it is open: a closed one (CloseSession
	// with deleteSubscriptions false, the usual way a subscription is handed on) has no one
	// to tell.
	seq, ok := sub.Transfer(to, sc)
	if !ok {
		return nil, ua.StatusBadSubscriptionIDInvalid // its task returned: the subscription is gone
	}
	// A notice the new owner was owed for this subscription (it had it before, lost it, and
	// has taken it back) is not true any more.
	to.dropNotice(id)
	if from != nil && from.AuthTokenID != nil && s.srv.sb.Session(from.AuthTokenID) == from {
		s.tellTransferred(from, id, seq)
	}
	return sub, ua.StatusOK
}

// tellTransferred tells the session from that subscription id has moved to another session:
// a StatusChangeNotification with Good_SubscriptionTransferred (Part 4 §5.14.7), in answer
// to a Publish of from's that is already waiting, or else kept on the session for its next
// one (Publish). Nothing waits for it: a session that never publishes again is never told,
// and the notice goes with the session. It runs on the message loop, as Publish does.
func (s *SubscriptionService) tellTransferred(from *session, id, seq uint32) {
	select {
	case pubreq := <-from.PublishRequests:
		_ = from.channel.SendResponseWithContext(context.Background(), pubreq.ID, transferNotice(pubreq.Req.RequestHeader, id, seq))
	default:
		from.keepNotice(id, seq)
	}
}

// noticeFor answers a Publish of sess with a notice it is owed, if it has one that is still
// true: the subscription is not the session's (it may have taken it back since, or the
// subscription may be gone). Notices that are no longer true are dropped.
func (s *SubscriptionService) noticeFor(sess *session, hdr *ua.RequestHeader) ua.Response {
	for {
		id, seq, ok := sess.takeNotice()
		if !ok {
			return nil
		}
		s.Mu.Lock()
		sub := s.Subs[id]
		s.Mu.Unlock()
		if sub != nil && sub.OwnedBy(sess) {
			continue
		}
		return transferNotice(hdr, id, seq)
	}
}

// transferNotice is the Publish response that tells a session subscription id has moved.
func transferNotice(hdr *ua.RequestHeader, id, seq uint32) *ua.PublishResponse {
	eo := ua.NewExtensionObject(&ua.StatusChangeNotification{Status: ua.StatusGoodSubscriptionTransferred, DiagnosticInfo: &ua.DiagnosticInfo{}})
	eo.UpdateMask()
	return &ua.PublishResponse{
		ResponseHeader:           responseHeader(hdr.RequestHandle, ua.StatusOK),
		SubscriptionID:           id,
		NotificationMessage:      &ua.NotificationMessage{SequenceNumber: seq, PublishTime: time.Now(), NotificationData: []*ua.ExtensionObject{eo}},
		AvailableSequenceNumbers: []uint32{},
		Results:                  []ua.StatusCode{},
		DiagnosticInfos:          []*ua.DiagnosticInfo{},
	}
}

// https://reference.opcfoundation.org/Core/Part4/v105/docs/5.13.8
func (s *SubscriptionService) DeleteSubscriptions(sc *uasc.SecureChannel, r ua.Request, reqID uint32) (ua.Response, error) {
	if s.srv.cfg.logger != nil {
		s.srv.cfg.logger.Debug("Handling %T", r)
	}

	req, err := safeReq[*ua.DeleteSubscriptionsRequest](r)
	if err != nil {
		return nil, err
	}
	session := s.srv.Session(req.Header())
	if session == nil {
		return nil, ua.StatusBadSessionIDInvalid // the gate checked; this is defensive
	}

	s.Mu.Lock()
	defer s.Mu.Unlock()

	results := make([]ua.StatusCode, len(req.SubscriptionIDs))
	for i := range req.SubscriptionIDs {

		subid := req.SubscriptionIDs[i]
		if s.srv.cfg.logger != nil {
			s.srv.cfg.logger.Info("Subscription %d deleted by client", subid)
		}
		sub, ok := s.Subs[subid]
		if !ok {
			results[i] = ua.StatusBadSubscriptionIDInvalid
			continue
		}
		if !sub.OwnedBy(session) {
			results[i] = ua.StatusBadSessionIDInvalid
			continue
		}
		// delete subscription gets the lock so we set them up to run in the background
		// once this function releases its lock
		go s.DeleteSubscription(subid)
		results[i] = ua.StatusOK
	}
	return &ua.DeleteSubscriptionsResponse{
		ResponseHeader: &ua.ResponseHeader{
			Timestamp:          time.Now(),
			RequestHandle:      req.RequestHeader.RequestHandle,
			ServiceResult:      ua.StatusOK,
			ServiceDiagnostics: &ua.DiagnosticInfo{},
			StringTable:        []string{},
			AdditionalHeader:   ua.NewExtensionObject(nil),
		},
		Results:         results,                //                  []StatusCode
		DiagnosticInfos: []*ua.DiagnosticInfo{}, //          []*DiagnosticInfo
	}, nil
}

type PubReq struct {
	// The data of the publish request
	Req *ua.PublishRequest

	// The request ID (from the header) of the publish request.  This has to be used when replying.
	ID uint32
}

// This is the type that with its run() function will work in the bakground fullfilling subscription
// publishes.
//
// MonitoredItems will send updates on the NotifyChannel to let the background task know that
// an event has occured that needs to be published.
type Subscription struct {
	srv *SubscriptionService
	ID  uint32
	// The publishing interval and counts are set by CreateSubscription
	// before Start and, after it, written only by the subscription's task
	// (a ModifySubscription goes through Modify), under Mu. The task reads
	// them without the lock; anything else reads them with Revised.
	RevisedPublishingInterval float64
	RevisedLifetimeCount      uint32
	RevisedMaxKeepAliveCount  uint32
	// Session and Channel are the session that owns the subscription and the channel its
	// publish responses go out on. Like the counts above they are set before Start and then
	// written only by the task (a TransferSubscriptions goes through Transfer), under Mu;
	// anything else reads them with Owner or OwnedBy.
	Session *session
	Channel *uasc.SecureChannel
	// publishingEnabled is SetPublishingMode's flag (CreateSubscription's at first): while it
	// is false the task sends keep-alives only and holds what its items report (Part 4
	// §5.14.4). Owned like the counts.
	publishingEnabled bool

	SequenceID uint32
	//SeqNums                   map[uint32]struct{}
	T *time.Ticker

	NotifyChannel chan *ua.MonitoredItemNotification

	// ops carries a change to the task, which owns the ticker the publishing interval
	// drives and the fields the publish loop reads: ModifySubscription, SetPublishingMode,
	// TransferSubscriptions.
	ops chan taskOp

	// the running flag and shutdown channel are used to signal the background task that it should stop.
	// multiple places can kill the subscription so make sure you check the running flag using the mutex
	// before closing the shutdown channel.
	Mu       sync.Mutex
	running  bool
	shutdown chan struct{}

	// done is closed when the background task returns, so a change waiting for the task
	// gives up instead of waiting forever.
	done chan struct{}

	// publishMu is held from the moment a publish response's notifications
	// are checked against their items' monitoring modes until it is sent.
	publishMu sync.Mutex
}

// Fence returns once any publish response this subscription is composing
// has been sent. A service that changes what an item reports
// (SetMonitoringMode, DeleteMonitoredItems) calls it after the change and
// before it answers, so nothing checked against the old state reaches the
// client after the answer. It waits on at most one send.
func (s *Subscription) Fence() {
	s.publishMu.Lock()
	s.publishMu.Unlock() //nolint:staticcheck // an empty critical section is the point
}

func NewSubscription() *Subscription {
	return &Subscription{
		//SeqNums:       map[uint32]struct{}{},
		NotifyChannel: make(chan *ua.MonitoredItemNotification, 100),
		ops:           make(chan taskOp),
		shutdown:      make(chan struct{}),
		done:          make(chan struct{}),
	}
}

// taskOp is a change on its way to the task: what to do, run on the task's goroutine between
// publishes, and a channel the task closes once it is done.
type taskOp struct {
	do      func()
	applied chan struct{}
}

// change runs do on the task and returns once it has run, or false, having run nothing, when
// the task has returned (the subscription is gone).
func (s *Subscription) change(do func()) bool {
	op := taskOp{do: do, applied: make(chan struct{})}
	select {
	case s.ops <- op:
	case <-s.done:
		return false
	}
	// The task closes applied before it can return, having taken op.
	<-op.applied
	return true
}

// Owner is the session that owns the subscription and the channel it publishes on. Safe from
// any goroutine.
func (s *Subscription) Owner() (*session, *uasc.SecureChannel) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.Session, s.Channel
}

// OwnedBy reports whether sess owns the subscription. Subscription ids are per session
// (Part 4 §5.14), so to any other session the subscription is not there.
func (s *Subscription) OwnedBy(sess *session) bool {
	if sess == nil || sess.AuthTokenID == nil {
		return false
	}
	return s.ownedByToken(sess.AuthTokenID.String())
}

func (s *Subscription) ownedByToken(token string) bool {
	owner, _ := s.Owner()
	return owner != nil && owner.AuthTokenID != nil && owner.AuthTokenID.String() == token
}

// PublishingEnabled reports SetPublishingMode's flag. Safe from any goroutine.
func (s *Subscription) PublishingEnabled() bool {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.publishingEnabled
}

// SetPublishing turns publishing on or off (SetPublishingMode, Part 4 §5.14.4) and returns
// once the publish loop has taken it, or false when the subscription is gone. Disabled, the
// loop goes on counting keep-alives and lifetimes and sends keep-alives, and keeps the latest
// notification of each item for when it is enabled again.
func (s *Subscription) SetPublishing(enabled bool) bool {
	return s.change(func() {
		s.Mu.Lock()
		s.publishingEnabled = enabled
		s.Mu.Unlock()
	})
}

// Transfer gives the subscription to the session to, publishing on its channel ch, and
// returns once the publish loop has taken it (ok false when the subscription is gone). What
// the loop holds for the subscription is published to the new session. seq is the sequence
// number for the old session's notice of the move: the next one, not taken, as a keep-alive
// carries it, so the new session sees no gap.
func (s *Subscription) Transfer(to *session, ch *uasc.SecureChannel) (seq uint32, ok bool) {
	ok = s.change(func() {
		s.Mu.Lock()
		s.Session, s.Channel = to, ch
		s.Mu.Unlock()
		seq = s.SequenceID + 1
		if seq == 0 {
			seq = 1
		}
	})
	return seq, ok
}

// Revised returns the publishing interval (ms) and the lifetime and
// keep-alive counts the subscription has now. Safe from any goroutine.
func (s *Subscription) Revised() (interval float64, lifetime, keepalive uint32) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.RevisedPublishingInterval, s.RevisedLifetimeCount, s.RevisedMaxKeepAliveCount
}

// Modify sets the publishing interval (ms, which must be positive and fit a
// time.Duration) and the lifetime and keep-alive counts of a running
// subscription: ModifySubscription, with the request already revised by the
// caller. The task applies them between publishes and resets its ticker to
// the new interval, so the next publishing cycle runs at it. Modify returns
// once they are in force, or false, having changed nothing, when the task
// has returned (the subscription is gone).
//
// gopcua's Update wrote the three fields from the caller's goroutine while
// the task read them, and never reset the ticker, so a modified
// subscription went on publishing at its old rate (black-box F-34, #433).
func (s *Subscription) Modify(interval float64, lifetime, keepalive uint32) bool {
	return s.change(func() {
		s.Mu.Lock()
		s.RevisedPublishingInterval = interval
		s.RevisedLifetimeCount = lifetime
		s.RevisedMaxKeepAliveCount = keepalive
		s.Mu.Unlock()
		s.T.Reset(publishingPeriod(interval))
	})
}

// publishingPeriod is a publishing interval in milliseconds as a Duration.
func publishingPeriod(ms float64) time.Duration {
	return time.Duration(ms * float64(time.Millisecond))
}

func (s *Subscription) Start() {
	go s.run()

}

func (s *Subscription) keepalive(pubreq PubReq) error {
	eo := make([]*ua.ExtensionObject, 0)

	msg := ua.NotificationMessage{
		SequenceNumber:   s.SequenceID + 1, // not sure why but ua expert wants the next sequence number on keepalives.
		PublishTime:      time.Now(),
		NotificationData: eo,
	}

	response := &ua.PublishResponse{
		ResponseHeader: &ua.ResponseHeader{
			Timestamp:          time.Now(),
			RequestHandle:      pubreq.Req.RequestHeader.RequestHandle,
			ServiceResult:      ua.StatusOK,
			ServiceDiagnostics: &ua.DiagnosticInfo{},
			StringTable:        []string{},
			AdditionalHeader:   ua.NewExtensionObject(nil),
		},
		SubscriptionID:           s.ID,
		MoreNotifications:        false,
		NotificationMessage:      &msg,
		AvailableSequenceNumbers: []uint32{}, // an empty array indicates taht we don't support retransmission of messages
		Results:                  []ua.StatusCode{},
		DiagnosticInfos:          []*ua.DiagnosticInfo{},
	}
	err := s.Channel.SendResponseWithContext(context.Background(), pubreq.ID, response)
	if err != nil {
		return err
	}
	return nil
}

// this function should be run as a go-routine and will handle sending data out
// to the client at the correct rate assuming there are publish requests queued up.
// if the function returns it deletes the subscription
func (s *Subscription) run() {
	// if this go routine dies, we need to delete ourselves.
	defer func() {
		if s.srv.srv.cfg.logger != nil {
			s.srv.srv.cfg.logger.Info("Subscription %d shutting down.", s.ID)
		}
		s.srv.DeleteSubscription(s.ID)
	}()
	// Deferred after the cleanup above, so it runs first: a change that arrives meanwhile is
	// released.
	defer close(s.done)

	keepalive_counter := 0
	lifetime_counter := 0
	// A ModifySubscription resets this ticker (Modify), in either waiting state.
	s.T = time.NewTicker(publishingPeriod(s.RevisedPublishingInterval))
	defer s.T.Stop()

	// This is the master run event loop.  It has effectively 3 states that it can be in.  The first two are designated with
	// the labels L0, and L2.  Everything after the L2 loop is the third state where we send any pending notifications.
	// The states always go L0 -> L2 -> Sending -> L0.  L0 and L2 are both places where we wait so they are done as for loops with
	// breaks to go to the next state.
	// The sending state always runs to completion.
	//
	// L0 waits for our notification interval to expire.  Any notifications that come in
	// while waiting will be stored in the publishQueue.  Once the interval expires, we'll move on to L2 if we've got notifications.
	// In L2 we wait for a publish request.  If we get one, we'll publish the notifications in the publishQueue.  If we don't
	// get a publish request, we'll continue to count intervals without a publish request.
	//
	// In L0 and L2, If we get to the lifetime count without a publish request, we'll kill the subscription.
	// The latest notification of each item, kept across a disabled spell (SetPublishingMode)
	// and emptied once it is published.
	publishQueue := make(map[uint32]*ua.MonitoredItemNotification)
cycle:
	for {
		// we don't need to do anything if we don't have at least one thing to publish so lets get that first

		// Collect notifications until our publication interval is ready
	L0:
		for {
			select {
			case <-s.shutdown:
				return
			case newNotification := <-s.NotifyChannel:
				publishQueue[newNotification.ClientHandle] = newNotification
			case <-s.T.C:
				if len(publishQueue) == 0 || !s.publishingEnabled {
					// nothing to publish (or publishing is disabled, and what there is waits for
					// it), increment the keepalive counter and send a keepalive if it
					// has been enough intervals.
					keepalive_counter++
					if keepalive_counter > int(s.RevisedMaxKeepAliveCount) {
						keepalive_counter = 0
						select {
						case pubreq := <-s.Session.PublishRequests:
							err := s.keepalive(pubreq)
							if err != nil {
								if s.srv.srv.cfg.logger != nil {
									s.srv.srv.cfg.logger.Warn("problem sending keepalive to subscription #%d: %v", s.ID, err)
								}
								return
							}
						default:
							lifetime_counter++
							if lifetime_counter > int(s.RevisedLifetimeCount) {
								if s.srv.srv.cfg.logger != nil {
									s.srv.srv.cfg.logger.Warn("Subscription #%d timed out.", s.ID)
								}
								return
							}
						}
					}
					continue // nothing to publish this interval
				}
				// we have things to publish so we'll break out to do that.
				break L0
			case op := <-s.ops:
				op.do()
				close(op.applied)
			}
		}
		var pubreq PubReq

		// now we need to continue to collect notifications until we've got a publish request
	L2:
		for {
			select {
			case <-s.shutdown:
				return
			case pubreq = <-s.Session.PublishRequests:
				// once we get a publish request, we should move on to publish them back
				break L2
			case newNotification := <-s.NotifyChannel:
				publishQueue[newNotification.ClientHandle] = newNotification
			case op := <-s.ops:
				op.do()
				close(op.applied)
				if !s.publishingEnabled {
					// Disabled while waiting to publish: nothing goes out until it is enabled
					// again, and what was waiting waits with it.
					continue cycle
				}
			case <-s.T.C:
				// we had another tick without a publish request.
				lifetime_counter++
				if lifetime_counter > int(s.RevisedLifetimeCount) {
					if s.srv.srv.cfg.logger != nil {
						s.srv.srv.cfg.logger.Warn("Subscription %d timed out.", s.ID)
					}
					return
				}
			}
		}
		lifetime_counter = 0
		keepalive_counter = 0

		// What is published is what the items are reporting now: a
		// notification queued before its item was set Disabled or Sampling,
		// or deleted, is dropped rather than sent after the client was told
		// the item had stopped (Part 4 §5.12.1.3, §5.12.4). publishMu makes
		// the check and the send one step that Fence can wait for.
		s.publishMu.Lock()
		if mi := s.srv.srv.MonitoredItemService; mi != nil {
			mi.dropUnreported(s.ID, publishQueue)
		}
		if len(publishQueue) == 0 {
			err := s.keepalive(pubreq)
			s.publishMu.Unlock()
			if err != nil {
				if s.srv.srv.cfg.logger != nil {
					s.srv.srv.cfg.logger.Warn("problem sending keepalive to subscription #%d: %v", s.ID, err)
				}
				return
			}
			continue
		}

		s.SequenceID++
		if s.SequenceID == 0 {
			// per the spec, the sequence ID cannot be 0
			s.SequenceID = 1
		}
		if s.srv.srv.cfg.logger != nil {
			s.srv.srv.cfg.logger.Debug("Got publish req on sub #%d.  Sequence %d", s.ID, s.SequenceID)
		}
		// then get all the tags and send them back to the client

		//for x := range pubreq.Req.SubscriptionAcknowledgements {
		//a := pubreq.Req.SubscriptionAcknowledgements[x]
		//delete(s.SeqNums, a.SequenceNumber)
		//}

		final_items := make([]*ua.MonitoredItemNotification, len(publishQueue))
		i := 0
		for k := range publishQueue {
			final_items[i] = publishQueue[k]
			i++
		}

		dcn := ua.DataChangeNotification{
			MonitoredItems:  final_items,
			DiagnosticInfos: []*ua.DiagnosticInfo{},
		}
		eo := make([]*ua.ExtensionObject, 1)
		eo[0] = ua.NewExtensionObject(&dcn)
		eo[0].UpdateMask()

		msg := ua.NotificationMessage{
			SequenceNumber:   s.SequenceID,
			PublishTime:      time.Now(),
			NotificationData: eo,
		}
		//s.SeqNums[s.SequenceID] = struct{}{}

		response := &ua.PublishResponse{
			ResponseHeader: &ua.ResponseHeader{
				Timestamp:          time.Now(),
				RequestHandle:      pubreq.Req.RequestHeader.RequestHandle,
				ServiceResult:      ua.StatusOK,
				ServiceDiagnostics: &ua.DiagnosticInfo{},
				StringTable:        []string{},
				AdditionalHeader:   ua.NewExtensionObject(nil),
			},
			SubscriptionID:           s.ID,
			MoreNotifications:        false,
			NotificationMessage:      &msg,
			AvailableSequenceNumbers: []uint32{}, // an empty array indicates taht we don't support retransmission of messages
			Results:                  []ua.StatusCode{},
			DiagnosticInfos:          []*ua.DiagnosticInfo{},
		}
		err := s.Channel.SendResponseWithContext(context.Background(), pubreq.ID, response)
		s.publishMu.Unlock()
		if err != nil {
			if s.srv.srv.cfg.logger != nil {
				s.srv.srv.cfg.logger.Error("problem sending channel response: %v", err)
				s.srv.srv.cfg.logger.Error("Killing subscription %d", s.ID)
			}
			return
		}
		if s.srv.srv.cfg.logger != nil {
			s.srv.srv.cfg.logger.Debug("Published %d items OK for %d", len(publishQueue), s.ID)
		}
		publishQueue = make(map[uint32]*ua.MonitoredItemNotification)
		// wait till we've got a publish request.
	}
}

//PublishRequest_Encoding_DefaultBinary
