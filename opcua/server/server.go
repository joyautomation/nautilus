// Package server is nautilus's OPC UA server: the controller's tag store
// served as an OPC UA address space, so any OPC UA client — a SCADA
// gateway, UAExpert, a historian — can browse, read and subscribe to the
// tags with no configuration on the nautilus side beyond the endpoint.
//
// The server is built on gopcua's server package for the protocol (secure
// channel, sessions, services) and nothing else: the address space is this
// package's own NameSpace over runtime.Tags (namespace.go), read on demand.
//
// It is read-only. Writes are refused with Bad_UserAccessDenied, because
// the only thing a client can be today is anonymous; writes on a secure
// channel from a trusted client certificate, routed through the same path
// as POST /api/tags, are the planned follow-up.
//
// Subscriptions are fed by the tag store's own change generations: a sweep
// every Options.Sweep asks the store what moved since the last sweep (one
// integer comparison per tag, nothing allocated at steady state) and
// notifies only the monitored items that cover a changed tag.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gopcua/opcua/id"
	opcserver "github.com/gopcua/opcua/server"
	"github.com/gopcua/opcua/ua"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// DefaultEndpoint is where the server listens when the manifest names no
// endpoint: the loopback interface, so exposing a controller to the network
// is a decision an author writes down.
const DefaultEndpoint = "opc.tcp://localhost:4840"

// DefaultSweep is how often the change sweep runs for subscriptions.
const DefaultSweep = 100 * time.Millisecond

// Options configures a Server.
type Options struct {
	// Endpoint is the listen address, "opc.tcp://host:port" or "host:port";
	// empty is DefaultEndpoint. Port 0 picks a free port (tests).
	Endpoint string
	// Name is the project: the folder under Objects, the server's
	// application name, and the tail of its namespace URI.
	Name string
	// Sweep is the interval of the change sweep that feeds subscriptions;
	// zero is DefaultSweep. It bounds how late a subscriber hears of a
	// change, and is the MinimumSamplingInterval the server advertises.
	Sweep time.Duration
}

// Server serves one runtime's tag store over OPC UA.
type Server struct {
	rt   *runtime.Runtime
	opts Options
	host string
	port int

	mu      sync.Mutex
	srv     *opcserver.Server
	ns      *namespace
	cancel  context.CancelFunc
	done    chan struct{}
	up      bool
	since   time.Time
	lastErr error
	url     string
}

// New prepares a server. It validates the endpoint and dials nothing — the
// listen is Start's — so a project loads (and `naut check` passes) with no
// port in sight.
func New(rt *runtime.Runtime, o Options) (*Server, error) {
	if rt == nil {
		return nil, errors.New("opcua: no runtime")
	}
	if o.Endpoint == "" {
		o.Endpoint = DefaultEndpoint
	}
	if o.Sweep <= 0 {
		o.Sweep = DefaultSweep
	}
	if o.Name == "" {
		o.Name = "nautilus"
	}
	host, port, err := ParseEndpoint(o.Endpoint)
	if err != nil {
		return nil, err
	}
	return &Server{rt: rt, opts: o, host: host, port: port}, nil
}

// ParseEndpoint splits an "opc.tcp://host:port" (or bare "host:port")
// endpoint into its parts, with the errors `naut check` reports.
func ParseEndpoint(ep string) (host string, port int, err error) {
	addr := ep
	if strings.Contains(addr, "://") {
		if !strings.HasPrefix(addr, "opc.tcp://") {
			return "", 0, fmt.Errorf("opcua.endpoint: %q must be opc.tcp://host:port", ep)
		}
		addr = strings.TrimPrefix(addr, "opc.tcp://")
	}
	if strings.ContainsAny(addr, "/?#") {
		return "", 0, fmt.Errorf("opcua.endpoint: %q must be opc.tcp://host:port, with no path", ep)
	}
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, fmt.Errorf("opcua.endpoint: %q must be opc.tcp://host:port", ep)
	}
	if host == "" {
		return "", 0, fmt.Errorf("opcua.endpoint: %q names no host (use 0.0.0.0 to listen on every interface)", ep)
	}
	port, err = strconv.Atoi(portText)
	if err != nil || port < 0 || port > 65535 {
		return "", 0, fmt.Errorf("opcua.endpoint: %q has no valid port", ep)
	}
	return host, port, nil
}

// Start listens and serves until Stop (or ctx's cancellation). A failed
// listen — the port is taken — is returned, and the server is not up.
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.up {
		return nil
	}
	port := s.port
	listen, aliases := endpointHosts(s.host)
	if port == 0 {
		p, err := freePort(listen)
		if err != nil {
			s.lastErr = err
			return err
		}
		port = p
	}
	// gopcua announces its node-set import and its listener on the
	// standard logger, unconditionally; those lines are not ours to show
	// an operator (the run banner says where we listen). Everything else
	// written to the logger in this window passes through untouched.
	restore := quietGopcua()
	defer restore()
	// gopcua listens on the first endpoint and advertises them all, and a
	// client finds an endpoint only by its exact URL: the listener's own,
	// then one per name it answers to (see endpointHosts).
	opts := []opcserver.Option{
		opcserver.EndPoint(listen, port),
		opcserver.ServerName("Nautilus · " + s.opts.Name),
		opcserver.ManufacturerName("Joy Automation"),
		opcserver.ProductName("Nautilus"),
		opcserver.EnableSecurity("None", ua.MessageSecurityModeNone),
		opcserver.EnableAuthMode(ua.UserTokenTypeAnonymous),
	}
	for _, a := range aliases {
		opts = append(opts, opcserver.EndPoint(a, port))
	}
	srv := opcserver.New(opts...)
	ns := &namespace{
		rt:     s.rt,
		uri:    "urn:joyautomation:nautilus:" + s.opts.Name,
		title:  s.opts.Name,
		sweep:  s.opts.Sweep,
		stamps: map[string]stamp{},
	}
	srv.AddNamespace(ns)
	registerServices(srv, ns)
	// Hang the project folder off ns=0's Objects, where every client
	// starts browsing.
	root, err := srv.Namespace(0)
	if err != nil {
		return err
	}
	objects := root.Node(ua.NewNumericNodeID(0, id.ObjectsFolder))
	if objects == nil {
		return errors.New("opcua: gopcua has no Objects folder")
	}
	objects.AddRef(ns.folderNode(), opcserver.RefTypeIDOrganizes, true)

	cctx, cancel := context.WithCancel(ctx)
	if err := srv.Start(cctx); err != nil {
		cancel()
		s.lastErr = err
		return fmt.Errorf("opcua: listen %s: %w", net.JoinHostPort(s.host, strconv.Itoa(port)), err)
	}
	// Every value the store holds now was first seen now, as far as any
	// OPC UA client can tell: stamp them before the first read, or the
	// first client to read a tag would date every initial value to its
	// own connect time.
	ns.seed(time.Now())
	s.srv, s.ns, s.cancel = srv, ns, cancel
	s.url = endpointURL(listen, port)
	s.up, s.since, s.lastErr = true, time.Now(), nil
	s.done = make(chan struct{})
	go s.sweep(cctx, s.done)
	return nil
}

// Stop closes every session and the listener, and stops the sweep.
func (s *Server) Stop() {
	s.mu.Lock()
	if !s.up {
		s.mu.Unlock()
		return
	}
	s.cancel()
	s.srv.Close()
	s.up = false
	done := s.done
	s.mu.Unlock()
	<-done
}

// URL is the endpoint the server listens on, as a client dials it: the
// address the configured host resolved to (127.0.0.1 for localhost) and
// the port a port of 0 resolved to. Empty before Start.
func (s *Server) URL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.url
}

// NamespaceIndex is the namespace index tag nodes live in: 1, the entry
// after ns=0's http://opcfoundation.org/UA/ in the NamespaceArray (gopcua
// has no other built-in namespace). 0 before Start.
func (s *Server) NamespaceIndex() uint16 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ns == nil {
		return 0
	}
	return s.ns.id
}

// NodeID is the node id of a tag or member address, for a client that
// would rather not browse: "Level", "P101.Drive.Speed", "Levels[2]".
func (s *Server) NodeID(addr string) *ua.NodeID {
	return ua.NewStringNodeID(s.NamespaceIndex(), addr)
}

// Status is the server's health for the driver-status card.
type Status struct {
	Endpoint       string `json:"endpoint"`
	Listening      bool   `json:"listening"`
	SinceMs        int64  `json:"sinceMs"`
	LastError      string `json:"lastError,omitempty"`
	Tags           int    `json:"tags"`
	Subscriptions  int    `json:"subscriptions"`
	MonitoredItems int    `json:"monitoredItems"`
}

// Status reports whether the server is listening, where, and what the
// clients are watching.
func (s *Server) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{Endpoint: s.url, Listening: s.up, Tags: len(s.rt.Tags().AppendNames(nil))}
	if st.Endpoint == "" {
		st.Endpoint = "opc.tcp://" + net.JoinHostPort(s.host, strconv.Itoa(s.port))
	}
	if s.up {
		st.SinceMs = s.since.UnixMilli()
	}
	if s.lastErr != nil {
		st.LastError = s.lastErr.Error()
	}
	if s.srv != nil {
		if sub := s.srv.SubscriptionService; sub != nil {
			sub.Mu.Lock()
			st.Subscriptions = len(sub.Subs)
			sub.Mu.Unlock()
		}
		if mi := s.srv.MonitoredItemService; mi != nil {
			mi.Mu.Lock()
			st.MonitoredItems = len(mi.Items)
			mi.Mu.Unlock()
		}
	}
	return st
}

// sweep feeds subscriptions. Each tick asks the store what changed since
// the last — a generation compare per tag, with the changed names appended
// to a reused buffer — stamps those changes with the time (SourceTimestamp
// for the readings that follow), and, when anything is monitored, notifies
// the monitored items whose node covers a changed tag: the tag itself, or
// a member of it.
//
// The notifications are this package's, not gopcua's ChangeNotification:
// that one sends on a subscription's channel while holding the monitored
// item lock, and blocks when the channel is full — which, once the
// subscription's goroutine has gone (its deletion wants the same lock),
// is a deadlock that takes the sweep, every Status call and every new
// subscription with it. Here the send never blocks: a full channel marks
// the node pending and the next tick tries again, so a slow subscriber
// delays its own notifications and nothing else.
func (s *Server) sweep(ctx context.Context, done chan struct{}) {
	defer close(done)
	tags := s.rt.Tags()
	gen := tags.Generation()
	var buf []runtime.Change
	changed := map[string]struct{}{}
	pending := map[string]struct{}{} // node id strings whose send was refused
	t := time.NewTicker(s.opts.Sweep)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		buf, gen = tags.ChangedSince(gen, buf[:0])
		if len(buf) == 0 && len(pending) == 0 {
			continue
		}
		now := time.Now()
		clear(changed)
		for _, c := range buf {
			s.ns.stampAt(c.Name, c.Gen, now)
			changed[ir.NameKey(c.Name)] = struct{}{}
		}
		for _, mon := range s.monitored() {
			root, _ := ir.SplitAddress(mon.nid.StringID())
			_, moved := changed[ir.NameKey(root)]
			_, retry := pending[mon.key]
			if !moved && !retry {
				continue
			}
			if s.notify(mon) {
				delete(pending, mon.key)
			} else {
				pending[mon.key] = struct{}{}
			}
		}
	}
}

// monitoredNode is one of our nodes some subscription is watching, with
// the items watching it.
type monitoredNode struct {
	key   string // the node id as gopcua keys it
	nid   *ua.NodeID
	items []*opcserver.MonitoredItem
}

// monitored lists the nodes of ours under monitoring right now. gopcua's
// maps are read under its lock and copied out, so no notification is ever
// sent with the lock held.
func (s *Server) monitored() []monitoredNode {
	mi := s.srv.MonitoredItemService
	if mi == nil {
		return nil
	}
	mi.Mu.Lock()
	out := make([]monitoredNode, 0, len(mi.Nodes))
	for k, items := range mi.Nodes {
		nid, err := ua.ParseNodeID(k)
		if err != nil || nid.Namespace() != s.ns.id || nid.Type() != ua.NodeIDTypeString {
			continue
		}
		out = append(out, monitoredNode{key: k, nid: nid, items: append([]*opcserver.MonitoredItem(nil), items...)})
	}
	mi.Mu.Unlock()
	return out
}

// notify reads the node once and offers the reading to every item watching
// it. It reports false when any subscription's channel was full, so the
// caller tries that node again next tick rather than dropping the change.
func (s *Server) notify(mon monitoredNode) bool {
	ok := true
	var dv *ua.DataValue
	for _, item := range mon.items {
		if item == nil || item.Sub == nil || item.Req == nil || item.Req.ItemToMonitor == nil || item.Req.RequestedParameters == nil {
			continue
		}
		if dv == nil {
			dv = s.ns.Attribute(mon.nid, item.Req.ItemToMonitor.AttributeID)
		}
		n := &ua.MonitoredItemNotification{ClientHandle: item.Req.RequestedParameters.ClientHandle, Value: dv}
		select {
		case item.Sub.NotifyChannel <- n:
		default:
			ok = false
		}
	}
	return ok
}

// quietGopcua filters gopcua's startup chatter out of the standard logger
// until the returned function restores the previous writer.
func quietGopcua() (restore func()) {
	prev := log.Writer()
	log.SetOutput(&lineFilter{w: prev, drop: func(line string) bool {
		return strings.HasPrefix(line, "New Node Set:") || strings.HasPrefix(line, "doing ") || strings.HasPrefix(line, "Started listening on ")
	}})
	return func() { log.SetOutput(prev) }
}

// lineFilter is an io.Writer that drops the lines drop says to and passes
// the rest through. The standard logger writes one line per call, with
// its prefix and timestamp first, so the message is matched after them.
type lineFilter struct {
	w    io.Writer
	drop func(line string) bool
}

func (f *lineFilter) Write(p []byte) (int, error) {
	line := strings.TrimRight(string(p), "\n")
	// Strip the standard logger's "2006/01/02 15:04:05 " stamp when present.
	msg := line
	if len(msg) > 20 && msg[4] == '/' && msg[7] == '/' && msg[10] == ' ' && msg[13] == ':' && msg[16] == ':' && msg[19] == ' ' {
		msg = msg[20:]
	}
	if f.drop(msg) {
		return len(p), nil
	}
	return f.w.Write(p)
}

// freePort asks the OS for a port nobody is listening on.
func freePort(host string) (int, error) {
	l, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
