// write.go is the Write service: a trusted client writing the tags
// opcua.writable opens (docs/design/opcua-server-writes.md §1–§3).
//
// Who may write. A writer session is one on a SignAndEncrypt channel, and
// a secured channel opens only from a client certificate in the store's
// trust list (pki.go, checked at every OpenSecureChannel, renewals
// included). The trust list is read again on each Write request, so a
// certificate taken out of trusted/ stops writing at once rather than at
// the channel's next renewal. Anonymous on None reads, never writes.
//
// What may be written. A Variable's Value, when the tag or member (or an
// ancestor of it) matches opcua.writable and its root is not an input.
// Scalars only: an array, or an IndexRange, is Bad_WriteNotSupported
// until slice 4.
//
// How it lands. Each item that passes the protocol checks becomes one
// runtime.WriteOp, and the request's ops go through the runtime's front
// door (runtime.Tags.Write) as ONE batch under one store lock: a scan sees
// all of a request's accepted items or none. The front door converts to the
// declared type and refuses what does not fit and what is forced; this file
// only maps the Variant onto what the front door takes (§3.2) and its
// errors onto status codes (§3.3). No notification is sent from here: the
// write moves the tag's generation and the sweep tells subscribers (§3.6).
//
// The DataValue a client sends. A Good (or absent) StatusCode means "not
// specified"; any other is Bad_WriteNotSupported, since the store has no
// status to keep. Source and server timestamps are accepted and ignored:
// the server stamps the change when it sees it, as for every other write.
// Part 4 §5.10.4 lets a server refuse timestamps with Bad_WriteNotSupported,
// but most clients send one by default (asyncua's write_value always does),
// so refusing them would refuse ordinary writes.
package server

import (
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	"github.com/joyautomation/nautilus/internal/writable"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/opcua/internal/uasc"
	opcserver "github.com/joyautomation/nautilus/opcua/internal/uaserver"
	"github.com/joyautomation/nautilus/runtime"
)

// maxNodesPerWrite is the most nodes one Write may name, advertised in the
// Server object's OperationLimits and enforced, like maxNodesPerRead.
const maxNodesPerWrite = 10000

// maxWarned bounds the refusal log-once memory; past it the memory starts
// over, so a client probing many nodes costs a few repeated lines, not an
// unbounded map.
const maxWarned = 4096

// refusalBudget is the most refusal lines one client (by certificate, else
// by host) gets in the log: after it, one line says the rest are only
// counted. A writer's refusals name each node it may know, so a writer
// probing the whole address space would otherwise log a line per node.
const refusalBudget = 64

// maxLoggedNode is how much of a client-supplied node id a log line quotes.
const maxLoggedNode = 96

// writeAccess is the server's write policy and its bookkeeping: what is
// writable, who is a writer, the standby gate, the counters and the
// refusal log. It outlives a Stop/Start, so the counters do too.
type writeAccess struct {
	patterns writable.Patterns
	pki      *PKI // nil without secure endpoints: then nobody is a writer
	log      *slog.Logger

	gate atomic.Pointer[func() bool]

	writes  atomic.Uint64
	refused atomic.Uint64

	mu     sync.Mutex
	warned map[string]struct{}
	lines  map[string]int // refusal lines logged per client key
	budget int            // refusalBudget; tests lower it
}

// client is who sent a request, as the audit names it.
type client struct {
	session    string
	uri        string // the ApplicationURI in the client certificate
	thumbprint string // short: the first 16 hex digits
	remote     string
	writer     bool   // a SignAndEncrypt channel from a trusted, valid certificate
	revoked    bool   // a writer's channel whose certificate no longer is
	why        string // why revoked
}

// channelWriter reports whether sc is a writer's channel: SignAndEncrypt
// under a secure policy, which the server opens only for a certificate in
// the trust list (uasc's checkOpen calls VerifyPeer on every OPN, renewals
// included). Without a PKI no secure endpoint exists and nobody writes.
func (w *writeAccess) channelWriter(sc *uasc.SecureChannel) bool {
	if w == nil || w.pki == nil || sc == nil {
		return false
	}
	p := sc.SecurityPolicyURI()
	return sc.SecurityMode() == ua.MessageSecurityModeSignAndEncrypt && p != "" && p != ua.SecurityPolicyURINone && len(sc.RemoteCertificate()) > 0
}

// who identifies the client of a request on sc. For a writer's channel it
// also checks the certificate again, once per request: still in the trust
// list, and within its validity period now (the channel opened when it
// was). A certificate that fails is revoked: the session writes nothing,
// and reads UserAccessLevel CurrentRead. One that cannot be parsed is no
// writer's.
func (w *writeAccess) who(srv *opcserver.Server, sc *uasc.SecureChannel, hdr *ua.RequestHeader) client {
	c := client{writer: w.channelWriter(sc)}
	if hdr != nil && srv != nil {
		if sess := srv.Session(hdr); sess != nil && sess.ID != nil {
			c.session = sess.ID.String()
		}
	}
	if sc == nil {
		return c
	}
	if a := sc.RemoteAddr(); a != nil {
		c.remote = a.String()
	}
	leaf := leafCert(sc.RemoteCertificate())
	if leaf != nil {
		c.thumbprint = Thumbprint(leaf.Raw)[:16]
		if len(leaf.URIs) > 0 {
			c.uri = leaf.URIs[0].String()
		}
	}
	if c.writer {
		if why := certStanding(leaf, w.pki.trusted, time.Now()); why != "" {
			c.writer, c.revoked, c.why = false, true, why
		}
	}
	return c
}

// leafCert is the first certificate of a DER chain, nil when it does not
// parse.
func leafCert(der []byte) *x509.Certificate {
	certs, err := x509.ParseCertificates(der)
	if err != nil || len(certs) == 0 {
		return nil
	}
	return certs[0]
}

// certStanding is "" when a writer's certificate may still write at now:
// readable, within its validity period, and in the trust list. Otherwise
// it says why not. It fails closed.
func certStanding(leaf *x509.Certificate, trusted func(der []byte) bool, now time.Time) string {
	switch {
	case leaf == nil:
		return "the channel's client certificate cannot be read"
	case now.Before(leaf.NotBefore):
		return "the client certificate is not valid yet"
	case now.After(leaf.NotAfter):
		return "the client certificate has expired"
	case trusted == nil || !trusted(leaf.Raw):
		return "the client certificate is no longer trusted"
	}
	return ""
}

// key names the client for the refusal log: its certificate, else its host.
func (c client) key() string {
	if c.thumbprint != "" {
		return c.thumbprint
	}
	if h, _, err := net.SplitHostPort(c.remote); err == nil {
		return h
	}
	return c.remote
}

// leader reports whether this replica may take writes: true unless a gate
// is installed and says it is a standby.
func (w *writeAccess) leader() bool {
	if g := w.gate.Load(); g != nil && *g != nil {
		return (*g)()
	}
	return true
}

// SetWriteGate installs the leadership gate the Write service asks before
// it lands anything: false on a standby replica, whose store is stale and
// does not scan, so a client's write is refused with Bad_InvalidState
// instead of landing where nothing reads it. `naut run` installs the
// elector's IsLeader (wireRedundancy). Nil removes the gate.
func (s *Server) SetWriteGate(leader func() bool) {
	if leader == nil {
		s.w.gate.Store(nil)
		return
	}
	s.w.gate.Store(&leader)
}

// writableMember reports whether m may be written by a writer: a scalar
// Variable whose address (or an ancestor's) matches opcua.writable, under a
// root that is not an input. It is the node's AccessLevel CurrentWrite bit.
// An array is not, matched or not, until array writes land (slice 4): its
// every write is Bad_WriteNotSupported, and a CurrentWrite would say
// otherwise.
func (ns *namespace) writableMember(m member) bool {
	if ns.w == nil || len(ns.w.patterns) == 0 || m.absent || isObject(m) || m.val.Kind == ir.TypeArray {
		return false
	}
	root, _ := ir.SplitAddress(m.addr)
	if ns.isInput(root) {
		return false
	}
	return ns.w.patterns.Allows(m.addr)
}

// isInput reports whether root names a driver-bound input: the driver
// replaces it before every scan, so a write to it would be lost unread.
func (ns *namespace) isInput(root string) bool {
	key := ir.NameKey(root)
	for _, in := range ns.rt.Inputs() {
		if ir.NameKey(in) == key {
			return true
		}
	}
	return false
}

// accessLevel is a Variable's AccessLevel: CurrentRead, plus CurrentWrite
// when it is writable.
func (ns *namespace) accessLevel(m member) ua.AccessLevelType {
	if ns.writableMember(m) {
		return ua.AccessLevelTypeCurrentRead | ua.AccessLevelTypeCurrentWrite
	}
	return ua.AccessLevelTypeCurrentRead
}

// userAccessLevel is the UserAccessLevel a session reads for nid: the
// node's AccessLevel for a writer, CurrentRead for anyone else. ok is false
// when nid is not one of this namespace's Variables.
func (ns *namespace) userAccessLevel(nid *ua.NodeID, writer bool) (ua.AccessLevelType, bool) {
	m, ok := ns.resolve(nid)
	if !ok || isObject(m) {
		return 0, false
	}
	if writer {
		return ns.accessLevel(m), true
	}
	return ua.AccessLevelTypeCurrentRead, true
}

// writeAttributes is the Write service. See the top of this file.
func writeAttributes(srv *opcserver.Server, ns *namespace, sc *uasc.SecureChannel, r ua.Request) (ua.Response, error) {
	req, ok := r.(*ua.WriteRequest)
	if !ok {
		return nil, ua.StatusBadRequestTypeInvalid
	}
	if len(req.NodesToWrite) == 0 {
		return nil, ua.StatusBadNothingToDo
	}
	if len(req.NodesToWrite) > maxNodesPerWrite {
		return nil, ua.StatusBadTooManyOperations
	}
	w := ns.w
	cl := w.who(srv, sc, req.RequestHeader)
	results := make([]ua.StatusCode, len(req.NodesToWrite))
	details := make([]string, len(req.NodesToWrite))
	var (
		ops   []runtime.WriteOp
		at    []int    // ops[k] is item at[k]
		olds  []member // what each op's node held before
		paths = make([]string, len(req.NodesToWrite))
		addrs = make([]string, len(req.NodesToWrite)) // the resolved address, when there is one
	)
	for i, n := range req.NodesToWrite {
		if n != nil && n.NodeID != nil {
			paths[i] = n.NodeID.String()
		}
		if cl.revoked {
			results[i], details[i] = ua.StatusBadUserAccessDenied, cl.why
			continue
		}
		code, m, val, detail := ns.checkWrite(srv, n, cl)
		addrs[i] = m.addr
		if code != ua.StatusGood {
			results[i], details[i] = code, detail
			continue
		}
		ops = append(ops, runtime.WriteOp{Path: m.addr, Value: val})
		at = append(at, i)
		olds = append(olds, m)
	}
	// The standby gate is asked once, for the whole batch: a request lands
	// whole or not at all when leadership moves while it is answered.
	if len(ops) > 0 && !w.leader() {
		for _, i := range at {
			results[i], details[i] = ua.StatusBadInvalidState, "this replica is a standby"
		}
		ops, at = nil, nil
	}
	if len(ops) > 0 {
		for k, err := range ns.rt.Tags().Write(ops) {
			results[at[k]] = frontDoorStatus(err)
			if err != nil {
				details[at[k]] = err.Error()
			}
		}
	}
	for k, i := range at {
		if results[i] != ua.StatusGood {
			continue
		}
		w.writes.Add(1)
		after := olds[k]
		if m, ok := ns.resolve(req.NodesToWrite[i].NodeID); ok {
			after = m
		}
		w.log.Info("opcua: write", "node", paths[i], "old", shown(olds[k]), "new", shown(after),
			"session", cl.session, "client", cl.uri, "thumbprint", cl.thumbprint, "remote", cl.remote)
	}
	var refused []refusal
	for i, code := range results {
		if code == ua.StatusGood {
			continue
		}
		r := refusal{node: paths[i], code: code, detail: details[i]}
		// A writer is told about the nodes it may know, one line each; every
		// other refusal (a non-writer's, an unknown node id's) is a summary,
		// since its node ids are whatever the client chose to send.
		if cl.writer && addrs[i] != "" && code != ua.StatusBadNodeIDUnknown && code != ua.StatusBadNodeIDInvalid {
			r.node, r.known = ns.nodeID(member{addr: addrs[i]}).String(), true
		}
		refused = append(refused, r)
	}
	w.refuse(cl, refused)
	return &ua.WriteResponse{ResponseHeader: header(req.RequestHeader), Results: results, DiagnosticInfos: []*ua.DiagnosticInfo{}}, nil
}

// checkWrite runs one item's protocol checks, in the order §3.3 implies:
// what the node is, then who is asking, then whether this tag is open,
// then the shape of the DataValue, then the conversion. The replica is
// asked once per request, by writeAttributes, before the batch.
// It returns Good with the member and the value for the front door, or the
// refusal and a line for the log.
func (ns *namespace) checkWrite(srv *opcserver.Server, n *ua.WriteValue, cl client) (ua.StatusCode, member, any, string) {
	switch {
	case n == nil || n.NodeID == nil:
		return ua.StatusBadNodeIDInvalid, member{}, nil, "no node id"
	case n.NodeID.Namespace() != ns.id:
		// gopcua's Server object and its variables: read-only for every
		// session (an anonymous rewrite of NamespaceArray would break
		// every other client's namespace mapping).
		switch {
		case srv.Node(n.NodeID) == nil:
			return ua.StatusBadNodeIDUnknown, member{}, nil, "no such node"
		case n.AttributeID == ua.AttributeIDValue:
			return ua.StatusBadUserAccessDenied, member{}, nil, "the server's own nodes are read-only"
		}
		return ua.StatusBadNotWritable, member{}, nil, "the server's own nodes are read-only"
	case ns.isFolder(n.NodeID):
		return ua.StatusBadNotWritable, member{}, nil, "the project folder is an Object"
	}
	m, ok := ns.resolve(n.NodeID)
	if !ok {
		return ua.StatusBadNodeIDUnknown, member{}, nil, "no such tag or member"
	}
	if n.AttributeID != ua.AttributeIDValue {
		return ua.StatusBadNotWritable, m, nil, "only the Value attribute is writable"
	}
	if isObject(m) {
		return ua.StatusBadNotWritable, m, nil, "an Object has no value; write its members"
	}
	// A session that may not write is told so before anything about the
	// tag: Bad_UserAccessDenied, as every Value write of a tag answered
	// before writes existed. A writer learns whether the tag is open.
	if !cl.writer {
		return ua.StatusBadUserAccessDenied, m, nil, "not a writer session: writes need a SignAndEncrypt channel from a trusted client certificate"
	}
	root, _ := ir.SplitAddress(m.addr)
	if ns.isInput(root) {
		return ua.StatusBadNotWritable, m, nil, "an input: its driver replaces it before every scan"
	}
	if !ns.w.patterns.Allows(m.addr) {
		return ua.StatusBadNotWritable, m, nil, "not matched by opcua.writable"
	}
	if n.IndexRange != "" || m.val.Kind == ir.TypeArray {
		return ua.StatusBadWriteNotSupported, m, nil, "array writes are not supported"
	}
	val, code, detail := fromVariant(m, n.Value)
	return code, m, val, detail
}

// fromVariant converts a written DataValue to the value the front door
// takes for member m, by the node's DataType (§3.2). The front door then
// checks the declared width, enumeration members, NaN, and whole
// milliseconds; this checks what only the Variant shows: its type.
func fromVariant(m member, dv *ua.DataValue) (any, ua.StatusCode, string) {
	if dv == nil || dv.EncodingMask&ua.DataValueValue == 0 || dv.Value == nil {
		return nil, ua.StatusBadTypeMismatch, "no value"
	}
	// A Good status of any subcode (GoodClamped…) is no status to keep; an
	// Uncertain or Bad one would be a quality the store cannot hold.
	if dv.EncodingMask&ua.DataValueStatusCode != 0 && dv.Status&severityMask != 0 {
		return nil, ua.StatusBadWriteNotSupported, "a StatusCode cannot be written"
	}
	v := dv.Value
	if v.Has(ua.VariantArrayValues) {
		return nil, ua.StatusBadWriteNotSupported, "array writes are not supported"
	}
	x := v.Value()
	dt := dataType(m)
	mismatch := func() (any, ua.StatusCode, string) {
		return nil, ua.StatusBadTypeMismatch, fmt.Sprintf("a %s for a %s node", variantName(v), typeName(dt))
	}
	n, isInt, over := variantInt(x)
	switch {
	case dt == id.Boolean:
		if b, ok := x.(bool); ok {
			return b, ua.StatusGood, ""
		}
		return mismatch()
	case dt == id.String:
		if s, ok := x.(string); ok {
			return s, ua.StatusGood, ""
		}
		return mismatch()
	case dt == id.Double:
		switch f := x.(type) {
		case float64:
			return f, ua.StatusGood, ""
		case float32:
			return float64(f), ua.StatusGood, ""
		}
		if isInt {
			// A Double holds every integer up to 2^53 exactly; past it a
			// write would land as a neighbour of what was sent.
			if over || n > 1<<53 || n < -(1<<53) {
				return nil, ua.StatusBadOutOfRange, fmt.Sprintf("%v is beyond ±2^53, which a Double cannot hold exactly", x)
			}
			return float64(n), ua.StatusGood, ""
		}
		return mismatch()
	case dt == id.Duration:
		switch f := x.(type) {
		case float64:
			return f, ua.StatusGood, ""
		case float32:
			return float64(f), ua.StatusGood, ""
		}
		if isInt {
			return intValue(x, n, over), ua.StatusGood, ""
		}
		return mismatch()
	case isIntType(dt):
		// Any integer width: clients like asyncua send Int64 for a plain
		// Python int. The declared width is the front door's to check.
		if isInt {
			return intValue(x, n, over), ua.StatusGood, ""
		}
		return mismatch()
	}
	return mismatch()
}

// intValue is an integer Variant as the front door takes it: an int64, or
// a UInt64 past 2^63-1 as itself, which a ULINT or LWORD holds as its bit
// pattern and the front door refuses as out of range for anything else.
func intValue(x any, n int64, over bool) any {
	if over {
		return x
	}
	return n
}

// variantInt reads an integer Variant value of any width as an int64. over
// is a UInt64 past 2^63-1, which an int64 cannot hold.
func variantInt(x any) (n int64, ok, over bool) {
	switch v := x.(type) {
	case int8:
		return int64(v), true, false
	case int16:
		return int64(v), true, false
	case int32:
		return int64(v), true, false
	case int64:
		return v, true, false
	case uint8:
		return int64(v), true, false
	case uint16:
		return int64(v), true, false
	case uint32:
		return int64(v), true, false
	case uint64:
		if v > math.MaxInt64 {
			return 0, true, true
		}
		return int64(v), true, false
	}
	return 0, false, false
}

// frontDoorStatus maps the runtime front door's verdict on one item onto
// its status code (§3.3).
func frontDoorStatus(err error) ua.StatusCode {
	switch {
	case err == nil:
		return ua.StatusGood
	case errors.Is(err, runtime.ErrForced):
		return ua.StatusBadLocked
	case errors.Is(err, ir.ErrOutOfRange):
		return ua.StatusBadOutOfRange
	case errors.Is(err, ir.ErrTypeMismatch):
		return ua.StatusBadTypeMismatch
	case errors.Is(err, runtime.ErrUndefined):
		return ua.StatusBadNodeIDUnknown
	}
	return ua.StatusBadNotWritable
}

// severityMask is a StatusCode's severity bits: 00 is Good.
const severityMask = 0xC0000000

// refusal is one refused item, for the log.
type refusal struct {
	node   string // the canonical node id when known, else the client's
	code   ua.StatusCode
	detail string
	known  bool // a writer's refusal on a node it may know: logged per node
}

// refuse counts a request's refused items and logs them without letting a
// client flood the log. A writer's refusal on a node it may know is one
// line per client, node and reason. Everything else is a summary per
// client and reason, logged once, naming how many items the request had
// and one node id (cut short): an anonymous client sending thousands of
// junk node ids costs a line, not thousands. Past refusalBudget lines a
// client's refusals are only counted.
func (w *writeAccess) refuse(cl client, rs []refusal) {
	if len(rs) == 0 {
		return
	}
	w.refused.Add(uint64(len(rs)))
	type group struct {
		n     int
		first refusal
	}
	var order []string
	groups := map[string]*group{}
	for _, r := range rs {
		reason := statusName(r.code)
		if r.known {
			w.warnOnce(cl, cl.key()+"|"+r.node+"|"+reason, func() {
				w.log.Warn("opcua: write refused", "node", r.node, "status", reason, "reason", r.detail,
					"session", cl.session, "client", cl.uri, "thumbprint", cl.thumbprint, "remote", cl.remote)
			})
			continue
		}
		g := groups[reason]
		if g == nil {
			g = &group{first: r}
			groups[reason] = g
			order = append(order, reason)
		}
		g.n++
	}
	for _, reason := range order {
		g := groups[reason]
		w.warnOnce(cl, cl.key()+"|"+reason, func() {
			w.log.Warn("opcua: write refused", "node", clip(g.first.node), "items", g.n, "status", reason, "reason", g.first.detail,
				"session", cl.session, "client", cl.uri, "thumbprint", cl.thumbprint, "remote", cl.remote)
		})
	}
}

// warnOnce runs emit unless key was logged before or the client is past
// its budget.
func (w *writeAccess) warnOnce(cl client, key string, emit func()) {
	w.mu.Lock()
	if w.warned == nil || len(w.warned) >= maxWarned || len(w.lines) >= maxWarned {
		w.warned, w.lines = map[string]struct{}{}, map[string]int{}
	}
	if _, seen := w.warned[key]; seen {
		w.mu.Unlock()
		return
	}
	w.warned[key] = struct{}{}
	budget := w.budget
	if budget <= 0 {
		budget = refusalBudget
	}
	n := w.lines[cl.key()] + 1
	w.lines[cl.key()] = n
	w.mu.Unlock()
	switch {
	case n <= budget:
		emit()
	case n == budget+1:
		w.log.Warn("opcua: write refusals from this client are no longer logged, only counted (writesRefused)",
			"client", cl.uri, "thumbprint", cl.thumbprint, "remote", cl.remote)
	}
}

// clip cuts a client-supplied node id for the log.
func clip(s string) string {
	if len(s) > maxLoggedNode {
		return s[:maxLoggedNode] + "…"
	}
	return s
}

// statusName is a status code's name, "BadTypeMismatch".
func statusName(code ua.StatusCode) string {
	if d, ok := ua.StatusCodes[code]; ok {
		return strings.TrimPrefix(d.Name, "Status")
	}
	return fmt.Sprintf("0x%08X", uint32(code))
}

// shown is a member's value as the audit line prints it.
func shown(m member) string {
	if m.absent {
		return "(none)"
	}
	return fmt.Sprint(runtime.Plain(m.val))
}

// variantName and typeName name a Variant's type and a DataType for the log.
func variantName(v *ua.Variant) string {
	if v == nil || v.Value() == nil {
		return "null"
	}
	return strings.TrimPrefix(fmt.Sprintf("%T", v.Value()), "ua.")
}

func typeName(dt uint32) string {
	switch dt {
	case id.Boolean:
		return "Boolean"
	case id.Double:
		return "Double"
	case id.Duration:
		return "Duration"
	case id.String:
		return "String"
	case id.SByte:
		return "SByte"
	case id.Byte:
		return "Byte"
	case id.Int16:
		return "Int16"
	case id.UInt16:
		return "UInt16"
	case id.Int32:
		return "Int32"
	case id.UInt32:
		return "UInt32"
	case id.Int64:
		return "Int64"
	case id.UInt64:
		return "UInt64"
	}
	return fmt.Sprintf("i=%d", dt)
}
