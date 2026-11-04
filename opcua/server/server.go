// Package server is nautilus's OPC UA server: the controller's tag store
// served as an OPC UA address space, so any OPC UA client — a SCADA
// gateway, UAExpert, a historian — can browse, read and subscribe to the
// tags with no configuration on the nautilus side beyond the endpoint.
//
// The server is built on opcua/internal/uaserver for the protocol (secure
// channel, sessions, services) and nothing else: that is gopcua v0.9.1's
// server package, copied in and ours to fix, over gopcua's encoding and
// secure-channel layers. The address space is this package's own NameSpace
// over runtime.Tags (namespace.go), read on demand. Comments here that say
// "gopcua" mean that inherited code.
//
// It is read-only unless Options.Writable opens tags, and then only to a
// writer session: one on a SignAndEncrypt channel from a client
// certificate in the trust list, or, with Options.AnonymousWrites (a lab
// switch), any session on the None endpoint too. A write goes through the
// runtime's one write front door, the same as POST /api/tags (write.go).
//
// Subscriptions are fed by the tag store's own change generations: a sweep
// every Options.Sweep asks the store what moved since the last sweep (one
// integer comparison per tag, nothing allocated at steady state) and
// notifies only the monitored items that cover a changed tag, or a tag
// whose quality changed (a status change is a data change).
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/url"
	"os"
	"reflect"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	"github.com/joyautomation/nautilus/internal/writable"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	opcserver "github.com/joyautomation/nautilus/opcua/internal/uaserver"
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
	// ApplicationURI is the server's ApplicationUri (Part 4 §7.1); empty is
	// ApplicationURI(Name). It must be an absolute URI (CheckApplicationURI).
	ApplicationURI string
	// PKI is the certificate store directory (pki.go). Set, the server
	// also offers the three secure endpoints, Basic256Sha256,
	// Aes128_Sha256_RsaOaep and Aes256_Sha256_RsaPss, each SignAndEncrypt
	// only, with its own certificate from the store (generated on first
	// start), and a secured channel opens only from a trusted client
	// certificate. Empty offers None alone, as before (tests, Go
	// compositions that want no files).
	PKI string
	// Writable opens the tags and members these globs match to Write from
	// a writer session (internal/writable: the rule sparkplug.writable
	// uses). Inputs never are. Empty: every write is refused. A writer
	// needs a secure endpoint, so without PKI nothing is writable, unless
	// AnonymousWrites makes the None endpoint's sessions writers too.
	Writable []string
	// AnonymousWrites makes every session on the None endpoint a writer:
	// an anonymous client with no certificate may write what Writable
	// opens, through the same checks and the same front door as a trusted
	// client. For a lab, where simulators such as PLC-Lab or Factory I/O
	// connect anonymously and certificates are a chore; never for a plant
	// network. Off by default. Writable still decides what is writable, so
	// with Writable empty it opens nothing. Start logs a warning, an error
	// when the endpoint listens beyond loopback.
	AnonymousWrites bool
	// Logger takes the write audit (an Info line per landed write, a Warn
	// once per client, node and reason for a refusal); nil is slog.Default.
	Logger *slog.Logger
}

// serverDiagnosticsLeftOut are the static diagnostic nodes under the Server
// object's ServerDiagnostics: the Summary and its counters, and the
// subscription, sampling-interval and session arrays. Diagnostics are not
// collected (EnabledFlag reads false), so they are left out (nautilus#485).
var serverDiagnosticsLeftOut = []uint32{
	id.Server_ServerDiagnostics_ServerDiagnosticsSummary,
	id.Server_ServerDiagnostics_ServerDiagnosticsSummary_ServerViewCount,
	id.Server_ServerDiagnostics_ServerDiagnosticsSummary_CurrentSessionCount,
	id.Server_ServerDiagnostics_ServerDiagnosticsSummary_CumulatedSessionCount,
	id.Server_ServerDiagnostics_ServerDiagnosticsSummary_SecurityRejectedSessionCount,
	id.Server_ServerDiagnostics_ServerDiagnosticsSummary_RejectedSessionCount,
	id.Server_ServerDiagnostics_ServerDiagnosticsSummary_SessionTimeoutCount,
	id.Server_ServerDiagnostics_ServerDiagnosticsSummary_SessionAbortCount,
	id.Server_ServerDiagnostics_ServerDiagnosticsSummary_PublishingIntervalCount,
	id.Server_ServerDiagnostics_ServerDiagnosticsSummary_CurrentSubscriptionCount,
	id.Server_ServerDiagnostics_ServerDiagnosticsSummary_CumulatedSubscriptionCount,
	id.Server_ServerDiagnostics_ServerDiagnosticsSummary_SecurityRejectedRequestsCount,
	id.Server_ServerDiagnostics_ServerDiagnosticsSummary_RejectedRequestsCount,
	id.Server_ServerDiagnostics_SamplingIntervalDiagnosticsArray,
	id.Server_ServerDiagnostics_SubscriptionDiagnosticsArray,
	id.Server_ServerDiagnostics_SessionsDiagnosticsSummary,
	id.Server_ServerDiagnostics_SessionsDiagnosticsSummary_SessionDiagnosticsArray,
	id.Server_ServerDiagnostics_SessionsDiagnosticsSummary_SessionSecurityDiagnosticsArray,
}

// securePolicies are the policies the secure endpoints offer, each under
// SignAndEncrypt only. Basic128Rsa15 and Basic256 are deprecated (Part 7)
// and never offered; Sign without encryption is not offered either.
var securePolicies = []string{
	ua.SecurityPolicyURIBasic256Sha256,
	ua.SecurityPolicyURIAes128Sha256RsaOaep,
	ua.SecurityPolicyURIAes256Sha256RsaPss,
}

// certWarning is how long before the server certificate's expiry the status
// row starts warning.
const certWarning = 30 * 24 * time.Hour

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
	version string
	cert    CertInfo // the server certificate, when PKI is set

	w *writeAccess // the write policy and audit; outlives Stop/Start
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
	if o.ApplicationURI == "" {
		o.ApplicationURI = ApplicationURI(o.Name)
	} else if err := CheckApplicationURI(o.ApplicationURI); err != nil {
		return nil, err
	}
	if err := writable.Patterns(o.Writable).Validate(); err != nil {
		return nil, fmt.Errorf("opcua.%w", err)
	}
	w := &writeAccess{patterns: writable.Patterns(o.Writable), anonymous: o.AnonymousWrites, log: o.Logger}
	if w.log == nil {
		w.log = slog.Default()
	}
	if o.PKI != "" {
		w.pki = &PKI{Dir: o.PKI}
	}
	return &Server{rt: rt, opts: o, host: host, port: port, w: w}, nil
}

// CheckApplicationURI is `naut check`'s test of opcua.application-uri: an
// absolute URI (a scheme, then something), with no spaces.
func CheckApplicationURI(uri string) error {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme == "" || (u.Opaque == "" && u.Host == "" && u.Path == "") || strings.ContainsAny(uri, " \t\n") {
		return fmt.Errorf("opcua.application-uri: %q must be an absolute URI, such as urn:example:lift-station", uri)
	}
	return nil
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
	build := binaryBuild()
	if s.version != "" {
		build.version = s.version
	}
	opts := []opcserver.Option{
		opcserver.EndPoint(listen, port),
		opcserver.ServerName("Nautilus · " + s.opts.Name),
		opcserver.ApplicationURI(s.opts.ApplicationURI),
		opcserver.ManufacturerName("Joy Automation"),
		opcserver.ProductName("Nautilus"),
		opcserver.ProductURI(ProductURI),
		opcserver.SoftwareVersion(build.version),
		opcserver.BuildNumber(build.number),
		opcserver.BuildDate(build.date),
		opcserver.EnableSecurity("None", ua.MessageSecurityModeNone),
		opcserver.EnableAuthMode(ua.UserTokenTypeAnonymous),
	}
	for _, a := range aliases {
		opts = append(opts, opcserver.EndPoint(a, port))
	}
	if s.opts.PKI != "" {
		sec, err := s.secure(listen, aliases)
		if err != nil {
			s.lastErr = err
			return err
		}
		opts = append(opts, sec...)
	}
	srv := opcserver.New(opts...)
	ns := &namespace{
		rt:      s.rt,
		uri:     "urn:joyautomation:nautilus:" + s.opts.Name,
		title:   s.opts.Name,
		sweep:   s.opts.Sweep,
		stamps:  map[string]stamp{},
		mstamps: map[string]mstamp{},
		w:       s.w,
		enums:   collectEnums(s.rt),
	}
	srv.AddNamespace(ns)
	registerServices(srv, ns)
	// The Read limit advertised is the one readAttributes enforces.
	if n := srv.Node(ua.NewNumericNodeID(0, id.Server_ServerCapabilities_OperationLimits_MaxNodesPerRead)); n != nil {
		n.SetAttribute(ua.AttributeIDValue, opcserver.DataValueFromValue(uint32(maxNodesPerRead)))
	}
	// And the Write limit writeAttributes enforces.
	if n := srv.Node(ua.NewNumericNodeID(0, id.Server_ServerCapabilities_OperationLimits_MaxNodesPerWrite)); n != nil {
		n.SetAttribute(ua.AttributeIDValue, opcserver.DataValueFromValue(uint32(maxNodesPerWrite)))
	}
	// And the Browse, TranslateBrowsePathsToNodeIds and Call limits their
	// services enforce (browse.go, method.go).
	for limit, n := range map[uint32]uint32{
		id.Server_ServerCapabilities_OperationLimits_MaxNodesPerBrowse:                        maxNodesPerBrowse,
		id.Server_ServerCapabilities_OperationLimits_MaxNodesPerTranslateBrowsePathsToNodeIDs: maxNodesPerTranslate,
		id.Server_ServerCapabilities_OperationLimits_MaxNodesPerMethodCall:                    maxNodesPerMethodCall,
	} {
		if node := srv.Node(ua.NewNumericNodeID(0, limit)); node != nil {
			node.SetAttribute(ua.AttributeIDValue, opcserver.DataValueFromValue(n))
		}
	}
	// The fastest a value is sampled is the change sweep, the same
	// MinimumSamplingInterval every tag advertises.
	if n := srv.Node(ua.NewNumericNodeID(0, id.Server_ServerCapabilities_MinSupportedSampleRate)); n != nil {
		n.SetAttribute(ua.AttributeIDValue, opcserver.DataValueFromValue(float64(s.opts.Sweep)/float64(time.Millisecond)))
	}
	// Diagnostics are not collected, and ServerDiagnostics says so: its
	// mandatory EnabledFlag reads false rather than failing (nautilus#485).
	// It stays read-only, as Part 5 §6.3.3 asks when collection is not
	// supported (every ns=0 node is; see checkWrite and accessAttribute).
	if n := srv.Node(ua.NewNumericNodeID(0, id.Server_ServerDiagnostics_EnabledFlag)); n != nil {
		n.SetAttribute(ua.AttributeIDValue, opcserver.DataValueFromValue(false))
	}
	if root, err := srv.Namespace(0); err == nil {
		if nns, ok := root.(*opcserver.NodeNameSpace); ok {
			// MaxArrayLength and MaxStringLength are optional, and the server
			// sets no limit of its own (the message size bounds both). Part 5
			// says a server that does not impose such a limit leaves the
			// property out, so it is left out rather than served without a
			// value (nautilus#485).
			for _, nid := range []uint32{id.Server_ServerCapabilities_MaxArrayLength, id.Server_ServerCapabilities_MaxStringLength} {
				nns.RemoveNode(ua.NewNumericNodeID(0, nid))
			}
			// With diagnostics off, Part 5 §6.3.3 lets a server answer
			// Bad_NodeIdUnknown for every static diagnostic node except
			// EnabledFlag. Leave them out rather than serve the Summary's
			// counters and the arrays with no value (nautilus#485).
			for _, nid := range serverDiagnosticsLeftOut {
				nns.RemoveNode(ua.NewNumericNodeID(0, nid))
			}
		}
	}
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
	// Each enumeration the project declares is a DataType under ns=0's
	// Enumeration, where a client looks for the member names (#425).
	if enumeration := root.Node(ua.NewNumericNodeID(0, id.Enumeration)); enumeration != nil {
		ns.hangEnums(enumeration)
	}

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
	if s.opts.AnonymousWrites {
		s.warnAnonymousWrites(listen)
	}
	s.up, s.since, s.lastErr = true, time.Now(), nil
	s.done = make(chan struct{})
	go s.sweep(cctx, s.done)
	return nil
}

// warnAnonymousWrites says, at every start, that anonymous clients may
// write: a warning when the server listens on loopback, where only this
// machine's clients reach it, and an error when it listens beyond it,
// where anyone who can reach the port may write. With no writable list it
// says the switch opens nothing.
func (s *Server) warnAnonymousWrites(listen string) {
	lg := s.w.log
	if len(s.opts.Writable) == 0 {
		lg.Warn("opcua: anonymous-writes is on, but opcua.writable opens nothing, so nothing is writable", "endpoint", s.url)
		return
	}
	if loopbackHost(listen) {
		lg.Warn("opcua: anonymous-writes is on: any client on this machine may write the tags opcua.writable opens, anonymously on the None endpoint, with no certificate",
			"endpoint", s.url, "writable", strings.Join(s.opts.Writable, ", "))
		return
	}
	lg.Error("opcua: anonymous-writes is on and the server listens beyond loopback: ANY client that can reach this port may write the tags opcua.writable opens, with no certificate to say who it is (only its address is logged). Use it on a lab network only, never on a plant network",
		"endpoint", s.url, "writable", strings.Join(s.opts.Writable, ", "))
}

// loopbackHost reports whether a listen host (an address in URL form, as
// endpointHosts returns it) is a loopback address. A name that did not
// resolve is not known to be loopback.
func loopbackHost(host string) bool {
	ip := net.ParseIP(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"))
	return ip != nil && ip.IsLoopback()
}

// SetSoftwareVersion sets the version the server reports as
// ServerStatus.BuildInfo.SoftwareVersion: the CLI passes its own, the one
// `naut version` prints. Unset, it is the module version in the binary's
// build info, or "dev". It takes effect at the next Start.
func (s *Server) SetSoftwareVersion(v string) {
	s.mu.Lock()
	s.version = v
	s.mu.Unlock()
}

// ProductURI names nautilus as a product: ApplicationDescription.ProductUri
// and ServerStatus.BuildInfo.ProductUri.
const ProductURI = "urn:joyautomation:nautilus"

// ApplicationURI is the default ApplicationUri of the server for project
// name: urn:joyautomation:nautilus:<project>, the project name
// percent-encoded so any name makes a valid URI ("Lift Station" is
// Lift%20Station). It names the controller, not the machine it runs on:
// it is the same after a restart, on a new host, and on every replica of a
// cluster Deployment, whose pods have generated host names but one
// Service address and (with secure endpoints) one shared certificate, so
// a client that keys its connection on it (a historian, a SCADA gateway)
// finds the same server whichever replica answers. Two separate
// controllers running a project of the same name on one network should
// each set opcua.application-uri.
func ApplicationURI(name string) string {
	return "urn:joyautomation:nautilus:" + url.PathEscape(name)
}

// build is what ServerStatus.BuildInfo says about the binary.
type build struct {
	version string    // SoftwareVersion
	number  string    // BuildNumber: the commit, when the build recorded one
	date    time.Time // BuildDate
}

// binaryBuild reads the running binary's build info: the module version
// (or "dev"), the VCS revision as the build number, and the commit time as
// the build date, falling back to the executable's modification time.
func binaryBuild() build {
	b := build{version: "dev"}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := strings.TrimPrefix(bi.Main.Version, "v"); v != "" && v != "(devel)" {
			b.version = v
		}
		for _, kv := range bi.Settings {
			switch kv.Key {
			case "vcs.revision":
				b.number = kv.Value
			case "vcs.time":
				if t, err := time.Parse(time.RFC3339, kv.Value); err == nil {
					b.date = t
				}
			}
		}
	}
	if b.date.IsZero() {
		if exe, err := os.Executable(); err == nil {
			if fi, err := os.Stat(exe); err == nil {
				b.date = fi.ModTime()
			}
		}
	}
	return b
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
	// The server certificate, when secure endpoints are offered: its
	// thumbprint, its expiry, and a warning from 30 days before it.
	CertThumbprint string `json:"certThumbprint,omitempty"`
	CertExpiresMs  int64  `json:"certExpiresMs,omitempty"`
	CertWarning    string `json:"certWarning,omitempty"`
	// Writable is opcua.writable; empty is a read-only server.
	Writable []string `json:"writable,omitempty"`
	// AnonymousWrites is opcua.anonymous-writes: anonymous sessions on
	// the None endpoint may write what Writable opens.
	AnonymousWrites bool `json:"anonymousWrites,omitempty"`
	// Writes counts the items a client wrote that landed, WritesRefused
	// the items refused, since the process started.
	Writes        uint64 `json:"writes"`
	WritesRefused uint64 `json:"writesRefused"`
	// Standby is true while the write gate (SetWriteGate) says this replica
	// is not the leader: every write is refused with Bad_InvalidState.
	Standby bool `json:"standby,omitempty"`
}

// secure loads (or on first start makes) the server's certificate from the
// store and returns the options that offer the secure endpoints with it and
// hold every secured channel to the store's trust list.
func (s *Server) secure(listen string, aliases []string) ([]opcserver.Option, error) {
	pki := PKI{Dir: s.opts.PKI}
	hosts := append([]string{listen}, aliases...)
	der, key, created, stale, err := pki.Own(s.opts.ApplicationURI, s.opts.Name, hosts)
	if err != nil {
		return nil, err
	}
	info, err := describeCert(der, pki.ownCert())
	if err != nil {
		return nil, err
	}
	switch {
	case created:
		slog.Info("opcua: generated the server certificate", "file", pki.ownCert(), "thumbprint", info.Thumbprint, "applicationUri", info.ApplicationURI)
	case len(stale) > 0:
		slog.Warn("opcua: the server certificate does not name every host it is reached by; clients that check host names may refuse it. Empty own/ to generate a new one (every client must trust it again)", "file", pki.ownCert(), "missing", strings.Join(stale, ", "))
	}
	appURI := s.opts.ApplicationURI
	if info.ApplicationURI != "" && info.ApplicationURI != appURI {
		// Part 4 §7.1: the ApplicationUri announced is the certificate's.
		slog.Warn("opcua: the server certificate names another ApplicationURI; announcing the certificate's", "certificate", info.ApplicationURI, "configured", appURI, "file", pki.ownCert())
		appURI = info.ApplicationURI
	}
	s.cert = info
	opts := []opcserver.Option{
		opcserver.ApplicationURI(appURI),
		opcserver.Certificate(der),
		opcserver.PrivateKey(key),
		opcserver.VerifyPeer(pki.Verify),
	}
	for _, p := range securePolicies {
		opts = append(opts, opcserver.EnableSecurity(p, ua.MessageSecurityModeSignAndEncrypt))
	}
	return opts, nil
}

// Status reports whether the server is listening, where, and what the
// clients are watching.
func (s *Server) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{Endpoint: s.url, Listening: s.up, Tags: len(s.rt.Tags().AppendNames(nil)),
		Writable: s.opts.Writable, AnonymousWrites: s.opts.AnonymousWrites, Writes: s.w.writes.Load(), WritesRefused: s.w.refused.Load(), Standby: !s.w.leader()}
	if st.Endpoint == "" {
		st.Endpoint = "opc.tcp://" + net.JoinHostPort(s.host, strconv.Itoa(s.port))
	}
	if s.up {
		st.SinceMs = s.since.UnixMilli()
	}
	if s.lastErr != nil {
		st.LastError = s.lastErr.Error()
	}
	if s.cert.Thumbprint != "" {
		st.CertThumbprint = s.cert.Thumbprint
		st.CertExpiresMs = s.cert.NotAfter.UnixMilli()
		if left := time.Until(s.cert.NotAfter); left < certWarning {
			if left <= 0 {
				st.CertWarning = "the OPC UA server certificate has expired; clients will refuse it (empty pki/own to generate a new one, then trust it on each client)"
			} else {
				st.CertWarning = fmt.Sprintf("the OPC UA server certificate expires in %d days", int(left.Hours()/24)+1)
			}
		}
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
// a member of it — and whose reading actually differs from the one last
// sent, since the store's generation is per tag and a member of a UDT
// moves its idle siblings' generation without moving their values. A
// monitored tag whose quality changed since the last tick is visited the
// same way, since its status is part of what a subscriber is told (#419).
//
// The notifications are this package's. gopcua's ChangeNotification, now
// deleted from uaserver, sent on a subscription's channel while holding the
// monitored item lock, and blocked when the channel was full — which, once the
// subscription's goroutine has gone (its deletion wants the same lock),
// is a deadlock that takes the sweep, every Status call and every new
// subscription with it. Here the send never blocks: a full channel marks
// the node pending and the next tick tries again, so a slow subscriber
// delays its own notifications and nothing else. An item's first value is
// the sweep's too (monitoring.go books items without sending), as is the
// value an item hears when it is set back to Reporting.
func (s *Server) sweep(ctx context.Context, done chan struct{}) {
	defer close(done)
	tags := s.rt.Tags()
	gen := tags.Generation()
	var buf []runtime.Change
	changed := map[string]struct{}{}
	pending := map[string]struct{}{} // node id strings whose send was refused
	last := map[string]reading{}     // what each monitored node was last told
	live := map[string]struct{}{}    // the nodes monitored this tick, to prune last
	items := map[*opcserver.MonitoredItem]*itemState{}
	liveItems := map[*opcserver.MonitoredItem]struct{}{}
	qual := map[string]nio.Quality{}     // the store's non-Good qualities this tick, by name key
	lastQual := map[string]nio.Quality{} // each monitored root's quality as last seen
	liveRoots := map[string]struct{}{}   // the roots monitored this tick, to prune lastQual
	requal := map[string]struct{}{}      // roots whose quality moved this tick
	t := time.NewTicker(s.opts.Sweep)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		buf, gen = tags.ChangedSince(gen, buf[:0])
		// A quiet tick still walks the monitored nodes: a node new under
		// monitoring is recorded then, so the first change after it is
		// compared with what gopcua's initial notification told the client.
		now := time.Now()
		clear(changed)
		for _, c := range buf {
			s.ns.stampAt(c.Name, c.Gen, now)
			changed[ir.NameKey(c.Name)] = struct{}{}
		}
		clear(live)
		clear(liveItems)
		clear(liveRoots)
		clear(requal)
		mons := s.monitored()
		// A quality change writes nothing to the store, so no generation
		// moves with it; the default DataChangeFilter trigger is
		// StatusValue (Part 4 §7.17.2), so it is a data change all the
		// same (#419). Each monitored root's quality is compared with the
		// one seen last tick. Quality reports only what is not Good, so a
		// healthy plant costs one empty map here.
		clear(qual)
		if len(mons) > 0 {
			for name, q := range s.rt.Quality() {
				qual[ir.NameKey(name)] = q
			}
		}
		for _, mon := range mons {
			root, _ := ir.SplitAddress(mon.nid.StringID())
			rk := ir.NameKey(root)
			liveRoots[rk] = struct{}{}
			if prev, seen := lastQual[rk]; seen && prev != qual[rk] {
				requal[rk] = struct{}{}
			}
			lastQual[rk] = qual[rk]
		}
		for k := range lastQual {
			if _, ok := liveRoots[k]; !ok {
				delete(lastQual, k)
			}
		}
		for _, mon := range mons {
			live[mon.key] = struct{}{}
			owed := false // an item new under monitoring, back to Reporting or asked to resend is owed the current value
			for _, w := range mon.items {
				liveItems[w.item] = struct{}{}
				if st := items[w.item]; w.watching() && (st == nil || st.mode != w.mode || st.resend != w.resend || st.req != w.req) {
					owed = true
				}
			}
			root, _ := ir.SplitAddress(mon.nid.StringID())
			rk := ir.NameKey(root)
			_, moved := changed[rk]
			_, requalified := requal[rk]
			_, retry := pending[mon.key]
			if !moved && !requalified && !retry && !owed {
				continue
			}
			if s.notify(mon, last, retry, items, now) {
				delete(pending, mon.key)
			} else {
				pending[mon.key] = struct{}{}
			}
		}
		for k := range last {
			if _, ok := live[k]; !ok {
				delete(last, k)
			}
		}
		for it := range items {
			if _, ok := liveItems[it]; !ok {
				delete(items, it)
			}
		}
	}
}

// watching reports whether item is a live monitored item with a request
// behind it; gopcua's maps can hold less. req is the item's request as
// read under the lock (ModifyMonitoredItems replaces it).
func watching(item *opcserver.MonitoredItem, req *ua.MonitoredItemCreateRequest) bool {
	return item != nil && item.Sub != nil && req != nil && req.ItemToMonitor != nil && req.RequestedParameters != nil
}

// current reads the node once, for the attribute its first live item
// watches, and reduces it to what the node would be told.
func (s *Server) current(mon monitoredNode) (*ua.DataValue, reading, bool) {
	for _, w := range mon.items {
		if !w.watching() {
			continue
		}
		dv := s.ns.Attribute(mon.nid, w.req.ItemToMonitor.AttributeID)
		return dv, readingOf(dv), true
	}
	return nil, reading{}, false
}

// reading is what a monitored node was last told: enough to know whether
// the next sweep has anything new to say about it.
type reading struct {
	value  any
	status ua.StatusCode
}

// monitoredNode is one of our nodes some subscription is watching, with
// the items watching it.
type monitoredNode struct {
	key   string // the node id as gopcua keys it
	nid   *ua.NodeID
	items []watched
}

// watched is a monitored item with its mode, request and resend count as
// they were when the lock was held: SetMonitoringMode, ModifyMonitoredItems
// and a resend write them under gopcua's lock, and the sweep reads them
// there.
type watched struct {
	item   *opcserver.MonitoredItem
	mode   ua.MonitoringMode
	req    *ua.MonitoredItemCreateRequest
	resend uint32
}

func (w watched) watching() bool { return watching(w.item, w.req) }

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
		mon := monitoredNode{key: k, nid: nid, items: make([]watched, 0, len(items))}
		for _, item := range items {
			if item != nil {
				mon.items = append(mon.items, watched{item: item, mode: item.Mode, req: item.Req, resend: item.Resend})
			}
		}
		out = append(out, mon)
	}
	mi.Mu.Unlock()
	return out
}

// itemState is one monitored item's sampling: the interval it was granted
// (CreateMonitoredItems revised it to a multiple of the sweep), when it was
// last sent a value, whether its node has changed since, and the mode,
// request and resend count it was last seen with.
type itemState struct {
	every  time.Duration
	sent   time.Time
	dirty  bool
	mode   ua.MonitoringMode
	req    *ua.MonitoredItemCreateRequest // the interval is req's
	resend uint32
	last   reading // what the item was last told, for one with an IndexRange
}

// notify reads the node once and offers the reading to every item watching
// it that is due. A reading equal to what the node was last told says
// nothing new, but an item new under monitoring, or just set back to
// Reporting, is owed it anyway. An item whose sampling interval has not
// elapsed since its last value is marked dirty and left for a later tick,
// which is what sampling at that interval means; a Disabled or Sampling
// item is marked and never sent (Part 4 §5.12.4). An item with an
// IndexRange hears its slice, and only when the slice has changed: a move
// elsewhere in the array is nothing to it. It reports false when any item
// is still owed a value — a full channel, or an interval not yet up — so
// the caller comes back to this node next tick.
func (s *Server) notify(mon monitoredNode, last map[string]reading, retry bool, items map[*opcserver.MonitoredItem]*itemState, now time.Time) bool {
	dv, cur, ok := s.current(mon)
	if !ok {
		return true
	}
	prev, seen := last[mon.key]
	changed := !seen || !prev.equal(cur)
	last[mon.key] = cur
	done := true
	for _, w := range mon.items {
		item := w.item
		if !w.watching() {
			continue
		}
		st := items[item]
		if st == nil {
			st = &itemState{every: s.sampling(item, w.req), dirty: true, mode: w.mode, req: w.req, resend: w.resend}
			items[item] = st
		}
		if changed {
			st.dirty = true
		}
		if w.req != st.req {
			// ModifyMonitoredItems: the item samples at its new interval from
			// now; a value it is owed waits no longer than that.
			st.every, st.req = s.sampling(item, w.req), w.req
		}
		resumed := false
		if w.mode != st.mode {
			if w.mode == ua.MonitoringModeReporting {
				st.dirty, resumed = true, true
			}
			st.mode = w.mode
		}
		if w.resend != st.resend {
			// Owed the current value, changed or not (a transfer with
			// sendInitialValues, ResendData, a new client handle).
			st.dirty, resumed, st.resend = true, true, w.resend
		}
		if !st.dirty || st.mode != ua.MonitoringModeReporting {
			continue
		}
		if !st.sent.IsZero() && now.Sub(st.sent) < st.every-s.opts.Sweep/2 {
			done = false // not due yet; the next value it sees is the one current then
			continue
		}
		told, mine := dv, cur
		if rng := w.req.ItemToMonitor.IndexRange; rng != "" {
			told = indexRange(dv, rng)
			mine = readingOf(told)
			if !resumed && !st.sent.IsZero() && st.last.equal(mine) {
				st.dirty = false // the array moved, the slice did not
				continue
			}
		}
		n := &ua.MonitoredItemNotification{ClientHandle: w.req.RequestedParameters.ClientHandle, Value: wireValue(told)}
		select {
		case item.Sub.NotifyChannel <- n:
			st.sent, st.dirty, st.last = now, false, mine
		default:
			done = false
		}
	}
	return done
}

// readingOf reduces a value to what a node or an item is told of it.
func readingOf(dv *ua.DataValue) reading {
	r := reading{status: dv.Status}
	if dv.Value != nil {
		r.value = dv.Value.Value()
	}
	return r
}

// equal reports whether two readings say the same thing.
func (r reading) equal(o reading) bool {
	return r.status == o.status && reflect.DeepEqual(r.value, o.value)
}

// sampling is the interval an item was granted, the same arithmetic
// CreateMonitoredItems and ModifyMonitoredItems answered with.
func (s *Server) sampling(item *opcserver.MonitoredItem, req *ua.MonitoredItemCreateRequest) time.Duration {
	publishing, _, _ := item.Sub.Revised()
	ms := reviseSampling(req.RequestedParameters.SamplingInterval, publishing, s.opts.Sweep)
	return time.Duration(ms * float64(time.Millisecond))
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
