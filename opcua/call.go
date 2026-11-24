// call.go is a method call on the driver's own session (#705: ISDU over
// OPC UA is a pair of methods on a port's device object). The driver reads,
// writes and subscribes through its session loop; a call is one more
// request on the same session, so it is refused, not queued, when there is
// none, and an answer from a session that ended mid-call is an error.
//
// Call itself does not look at the write gate (SetSendGate): a read may
// go from a standby. A call that writes to a device (a WriteISDU) goes
// through CallFenced, which does.
package opcua

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gopcua/opcua/ua"

	"github.com/joyautomation/nautilus/internal/fence"
)

// ErrCallUnknown wraps the error of a call that was SENT and got no answer
// (a timeout, a connection lost under it): the server may have run it. An
// error that does not wrap it is certain: the call was never sent (no
// session, an id that does not resolve), or the server answered it with a
// failure of its own.
var ErrCallUnknown = errors.New("the call was sent and no answer came back")

// NodeRef addresses a node as a binding does: a node id and, optionally,
// the namespace URI that decides the node id's index on the live server
// (an index the server renumbered since the project was written is
// followed by its URI).
type NodeRef struct {
	NodeID    string
	Namespace string
}

func (d *Driver) liveSession() *Session {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.live
}

func (d *Driver) setLive(s *Session) {
	d.mu.Lock()
	d.live = s
	d.mu.Unlock()
}

// resolve parses a reference against a session's namespace table.
func (r NodeRef) resolve(s *Session) (*ua.NodeID, error) {
	n, err := ua.ParseNodeID(r.NodeID)
	if err != nil {
		return nil, fmt.Errorf("node id %q: %w", r.NodeID, err)
	}
	if r.Namespace == "" {
		return n, nil
	}
	for i, uri := range s.Client.Namespaces() {
		if uri == r.Namespace {
			return withNamespace(n, uint16(i)), nil
		}
	}
	return nil, fmt.Errorf("namespace %s is not on the server", r.Namespace)
}

// Call calls method on object with the input arguments and returns the
// output arguments. A bad status of the call, or of an argument, is an
// error naming it. It needs a connected session: with none it fails at
// once, saying so.
func (d *Driver) Call(ctx context.Context, object, method NodeRef, args ...*ua.Variant) ([]*ua.Variant, error) {
	sess := d.liveSession()
	if sess == nil {
		return nil, errors.New("opcua-client: not connected")
	}
	obj, err := object.resolve(sess)
	if err != nil {
		return nil, fmt.Errorf("object: %w", err)
	}
	meth, err := method.resolve(sess)
	if err != nil {
		return nil, fmt.Errorf("method: %w", err)
	}
	rctx, cancel := context.WithTimeout(ctx, d.conn.Timeout)
	defer cancel()
	res, err := sess.Client.Call(rctx, &ua.CallMethodRequest{ObjectID: obj, MethodID: meth, InputArguments: args})
	if err != nil {
		var answered ua.StatusCode
		if errors.As(err, &answered) {
			return nil, fmt.Errorf("call %s: %w", method.NodeID, err) // the server answered, with a failure
		}
		return nil, fmt.Errorf("call %s: %w: %w", method.NodeID, ErrCallUnknown, err)
	}
	if !good(res.StatusCode) {
		return nil, fmt.Errorf("call %s: %w", method.NodeID, res.StatusCode)
	}
	for i, st := range res.InputArgumentResults {
		if !good(st) {
			return nil, fmt.Errorf("call %s: argument %d: %w", method.NodeID, i+1, st)
		}
	}
	return res.OutputArguments, nil
}

// CallFenced is Call for a method that writes to a device (an IO-Link
// parameter's WriteISDU, #710 d1c): it goes only while this controller's
// write gate is open, on a pair's leader, and runs to the gate's send
// budget like every other fenced send (design §3.4). It is counted for a
// switchover's drain (fence.Enter before the gate read), so a call under
// way when the gate shuts ends, or is cut, before the RESIGN leaves.
// Outside a pair it is Call.
func (d *Driver) CallFenced(ctx context.Context, object, method NodeRef, args ...*ua.Variant) ([]*ua.Variant, error) {
	if mut("isdu_write_unfenced") {
		return d.Call(ctx, object, method, args...)
	}
	deadline, _, ok, done := fence.Begin(d.look, time.Now())
	defer done()
	if !ok || !fence.Started(deadline, time.Now()) {
		return nil, fmt.Errorf("call %s: %w", method.NodeID, fence.ErrGateClosed)
	}
	fctx, fcancel := fence.Context(ctx, deadline)
	defer fcancel()
	fence.BeforeSend()
	return d.Call(fctx, object, method, args...)
}
