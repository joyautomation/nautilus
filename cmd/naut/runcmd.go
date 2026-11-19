// `naut run` + `naut build`: the no-Go product surface. A manifest
// project (nautilus.yaml + IEC files) runs directly under the CLI binary,
// and `build` emits a deployable single binary by appending the project to
// a copy of this executable — no Go toolchain anywhere. Devs who outgrow
// the manifest drop to the Go library API (`naut new` without --no-go);
// that tier is unchanged.

package main

import (
	"archive/zip"
	"bytes"
	"context"
	"debug/elf"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/auth"
	"github.com/joyautomation/nautilus/devprofile"
	"github.com/joyautomation/nautilus/internal/lsp"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/internal/vcs"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/leader"
	"github.com/joyautomation/nautilus/retain"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/server"
	"github.com/joyautomation/nautilus/virtdev"
)

// electorFor builds the elector for a manifest with a `redundancy:` section
// and wires the project to it (wireRedundancy); nil, and nothing wired, for
// a manifest without one. A section with peers: makes this controller a
// member of a redundant pair (pairrun.go): the member comes back too, its
// peer listener already serving, for the caller to Close after the
// election's last Release. dir is the project's checkout ("" for a built
// controller).
func electorFor(proj *project.Project, addr, dir string) (leader.Election, *pairMember, error) {
	return electorWith(proj, addr, dir, osPairDeps())
}

// electorWith is electorFor with a pair member's environment and listen
// call given (tests).
func electorWith(proj *project.Project, addr, dir string, deps pairDeps) (leader.Election, *pairMember, error) {
	if node := deps.getenv("NAUTILUS_NODE"); node != "" && !proj.Redundancy.Pair() {
		// Set for a pair, but the manifest names none: each box would run
		// as its own leader, and both would drive the I/O.
		return nil, nil, fmt.Errorf("NAUTILUS_NODE=%s is set, but this manifest's redundancy: names no pair (peers: and witness:), so every box would run as its own leader against the same I/O: add the pair's section, or unset NAUTILUS_NODE", node)
	}
	if proj.Redundancy == nil {
		return nil, nil, nil
	}
	if proj.Redundancy.Pair() {
		m, err := newPairMember(proj.Redundancy, addr, dir, deps)
		if err != nil {
			return nil, nil, err
		}
		// /api/cluster and the dashboard say it until state streaming.
		m.q.Warn(project.PairRetainNotReplicated)
		wireRedundancy(proj, m.q)
		return m.q, m, nil
	}
	el, err := leader.New(proj.LeaseName(), os.Getenv("POD_NAME"), selfAddr(addr))
	if err != nil {
		return nil, nil, err
	}
	wireRedundancy(proj, el)
	return el, nil, nil
}

// firstDecisionTimeout bounds how long runProject waits for the elector's
// first decision (one Lease GET, and a write when it claims). An API server
// slower than that starts the drivers anyway; they then see the gate closed
// and behave as a standby's do (no start-up baseline, #606).
const firstDecisionTimeout = 5 * time.Second

// decider is the part of leader.Elector startup waits on.
type decider interface{ Decided() <-chan struct{} }

// awaitDecision waits until el has made its first decision (leader or
// standby), until timeout, or until ctx ends (a stop during startup). It
// reports whether the decision came.
func awaitDecision(ctx context.Context, el decider, timeout time.Duration) bool {
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-el.Decided():
		return true
	case <-ctx.Done():
		return false
	case <-t.C:
		fmt.Fprintln(os.Stderr, "naut run: redundancy: no leadership decision after", timeout, "- starting as a standby")
		return false
	}
}

// startDriverWhenDecided starts drv, when it has a loop of its own to
// start, once dec (nil without redundancy) has made its first decision or
// timeout has passed: a replica that leads from its first scan is then a
// plain start and keeps its drivers' start-up baseline, and only one that
// starts as a standby sees its write gate closed and drops it (#606). A
// stop during the wait starts nothing. It returns the teardown runProject
// defers, nil when there is none.
//
// A driver that also has Stop() owns its own teardown, so it gets a
// context Ctrl+C does NOT cancel and is stopped by the returned func
// instead. The ordering matters: the Sparkplug host must publish its
// STATE death certificate on a live connection before the DISCONNECT, and
// cancelling its context first would tear the session down out from
// under it. Stop publishes synchronously and waits, so by the time it
// returns the certificate is on the wire.
func startDriverWhenDecided(ctx context.Context, dec decider, timeout time.Duration, drv any) (stop func()) {
	if dec != nil && !awaitDecision(ctx, dec, timeout) && ctx.Err() != nil {
		return nil
	}
	s, ok := drv.(interface{ Start(context.Context) })
	if !ok {
		return nil // the loopback memory driver has nothing to start
	}
	if st, ok := drv.(interface{ Stop() }); ok {
		dctx, dcancel := context.WithCancel(context.Background())
		s.Start(dctx)
		return func() {
			st.Stop()
			dcancel() // belt and braces: Stop cancels its own child
		}
	}
	s.Start(ctx)
	return nil
}

// wireRedundancy answers a loaded project to its elector: the runtime scans
// only on the leader, and the driver (or every child of a Multi) gets the
// elector as its write gate. runProject calls it only for a manifest with a
// `redundancy:` section; without one nothing is gated and every driver is
// its own leader.
//
// The gate governs what a driver does on its OWN, outside the scan:
//   - a driver whose outputs carry device-side watchdogs (the modbus
//     Rewrite keep-alives: SC10 safety word, i550 control word) must stop
//     asserting them the moment this replica is not the leader — two
//     replicas must never both write a control word, and a stopped leader's
//     silence is what trips the device's own fail-safe instead of masking
//     the fault;
//   - a Sparkplug host must not declare itself the primary host (STATE
//     online) or send NCMD/DCMD from a standby, or two hosts fight over
//     every edge node in the group (#325).
//
// The OPC UA server gets the same gate for its Write service: a standby's
// store is stale and the scan does not run there, so a client's write is
// refused (Bad_InvalidState) rather than landing where nothing reads it.
// The server is built after the runtime, so the gate rides on the project
// and OPCUAServer installs it.
func wireRedundancy(proj *project.Project, el runtime.Coordinator) {
	proj.Runtime.Coordinator = el
	if g, ok := proj.Runtime.Driver.(interface{ SetWriteGate(func() bool) }); ok {
		g.SetWriteGate(el.IsLeader)
	}
	proj.WriteGate = el.IsLeader
}

// localGateLease is how long a project without `redundancy:` keeps its
// write gate open after the last check that found its scans live (#625):
// the lease a redundant replica holds, so a hung scan closes the gate on
// the same timing (about 5 to 8 s into the hang).
const localGateLease = 4 * time.Second

// gateOnLiveScans makes the write gate need live scans and returns the
// elector to run for it. With redundancy that is el itself: the lease
// needs live scans (#621), and el already gates the runtime and the
// drivers (wireRedundancy). Without, it is a local gate (#625) on the
// drivers only, and only those whose gate is just a gate
// (io.SetPlainGate): open while rt's scans are live, closed lease after the
// last check that found them live, so a driver's own loop (a Modbus
// rewrite: keep-alive) stops asserting outputs for a hung scan and the
// device's watchdog acts. The runtime stays uncoordinated (no takeover at
// start, forces kept) and the gate is open from the start, so a healthy
// project's drivers see what they saw without it.
func gateOnLiveScans(proj *project.Project, el leader.Election, rt *runtime.Runtime, lease time.Duration) leader.Election {
	gate := el
	if gate == nil {
		gate = leader.NewLocal(proj.Name, lease)
		if proj.Runtime.Driver != nil {
			// Only drivers whose gate is just a gate (io.PlainGated), each
			// told it is plain: optommp lets the unit's watchdog act on a
			// hung scan (#865); a Sparkplug host holds its commands and
			// stays the primary host (#943).
			nio.SetPlainGate(proj.Runtime.Driver, gate.IsLeader)
		}
	}
	gate.SetHealth(rt.Liveness)
	return gate
}

// replicaReady is runProject's READY=1 rule: scanned, or as a replica
// decided to stand by, or as a pair member up for firstDecisionTimeout.
func replicaReady(scanned bool, el leader.Election, pair bool, up time.Duration) bool {
	if scanned {
		return true
	}
	if el == nil {
		return false
	}
	select {
	case <-el.Decided():
		return !el.IsLeader()
	default:
		return pair && up >= firstDecisionTimeout
	}
}

// runProject hosts a loaded project: scan loop + tag API, Ctrl+C to stop.
// dir is the project's on-disk path when running from a checkout ("" for a
// built binary's embedded project) — it's where live git history capture
// looks; a built binary carries its history in the archive instead.
func runProject(fsys fs.FS, manifest, label, dir string) int {
	useConsoleLog()
	proj, err := project.Load(fsys, manifest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut run:", err)
		return 1
	}

	addr := proj.Addr
	if env := os.Getenv("NAUTILUS_ADDR"); env != "" {
		addr = env
	}

	// The tag API's port is bound first, before the elector, the retained
	// state, a driver or a protocol server touches anything: it is what
	// keeps a second instance of this controller (one started by hand next
	// to the service, #968) from connecting to the same I/O. One that finds
	// it held refuses to start, rather than running its I/O without an API.
	apiLn, addr, err := bindAPI(addr, dir, proj.Redundancy != nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut run:", err)
		return 1
	}
	apiUp := apiLn != nil
	defer func() {
		if apiLn != nil {
			apiLn.Close() // an early return; an orderly stop closes it first (below)
		}
	}()

	// Redundancy and retained state wire in before the runtime compiles,
	// so the first scan already answers to the elector and sees what the
	// last leader saved. Outside a cluster the elector is standalone
	// (always leader) and the retain store is the JSON file — the same
	// manifest runs on a bench and as a replica set.
	el, pair, err := electorFor(proj, addr, dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut run: redundancy:", err)
		return 1
	}
	if pair != nil {
		// Registered first, so it runs last: the peer listener answers
		// votes until the election below has stopped and released.
		defer pair.Close()
		// Until state streaming (#710 step 4) the opt-in is required
		// (Load refuses a pair without it), and said out loud.
		fmt.Fprintln(os.Stderr, "naut run: redundancy: WARNING: experimental pair: "+project.PairRetainNotReplicated)
		for _, h := range pair.untrustedAPIs(append(server.EnvAllowedHosts(), proj.Server.AllowedHosts...)) {
			fmt.Fprintf(os.Stderr, "naut run: redundancy: the pair advertises a tag API at %s, which NAUTILUS_ALLOWED_HOSTS does not list: a browser HMI that follows the leader there is refused (403). Add it to NAUTILUS_ALLOWED_HOSTS on both controllers, or name the controllers by IP in redundancy.peers\n", h)
		}
	}
	if proj.Retain != nil {
		cm, file := proj.RetainNames()
		proj.Runtime.Retain = retain.New(cm, file)
	}

	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut run: compile:", err)
		return 1
	}
	// The write gate needs live scans, with redundancy or without (#621,
	// #625). Set before gate.Run below.
	gate := gateOnLiveScans(proj, el, rt, localGateLease)
	// Under systemd (Type=notify), the watchdog heartbeat follows the same
	// liveness verdicts the gate takes: recorded as the elector checks, not
	// asked a second time. A verdict older than two leases (eight of the
	// elector's ticks) counts as not live.
	sd := newNotifier()
	watch := &liveWatch{stale: 2 * localGateLease}
	if sd != nil {
		gate.SetHealth(watch.wrap(rt.Liveness))
	}

	// The audit journal's system alarm reads the server, which is built
	// after the alarms (it serves them): srvRef is set once it exists.
	var srvRef atomic.Pointer[server.Server]
	alarms, err := startAlarms(proj, rt, auditAlarm(&srvRef))
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut run: alarms:", err)
		return 1
	}
	if alarms != nil {
		defer alarms.Close()
	}

	// A pair's operator intent (substitutes, alarm acknowledgements and
	// shelves) goes through its replicated log, mounted on the peer
	// listener; a takeover catches it up before its first scan. After the
	// alarms exist (the log replays its file into them), before the
	// election and the scans run.
	var subLog *server.SubstituteLog
	var alarmLog *server.AlarmLog
	if pair != nil {
		var eng *alarm.Engine
		if alarms != nil {
			eng = alarms.Engine
		}
		if subLog, alarmLog, err = pair.startLog(rt, 0, eng); err != nil {
			fmt.Fprintln(os.Stderr, "naut run: redundancy:", err)
			return 1
		}
	}

	// Live I/O configuration (#706): registered before the first scan, so
	// a leadership edge (the process's first scan, or a takeover) re-applies
	// what the retain store keeps before any logic runs or any output goes
	// out. The /api/io routes are wired below.
	ioc, err := project.NewIOConfig(proj, rt, fsys, manifest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut run: live I/O configuration is off:", err)
		ioc = nil
	} else {
		ioc.Gate = gate.IsLeader
		if pair != nil {
			ioc.PeersKeepIO = pair.peersKeepIO
		}
		if proj.Runtime.Retain != nil {
			ioc.Keep = rt.SaveRetained
			ioc.KeepStore = proj.Runtime.Retain.Kind()
		}
		rt.SetIO(ioc)
	}

	// Retained values go in before anything can read or write a tag: the
	// tag API, OPC UA and the HMI all start below (nautilus#531's startup
	// half). Standalone only; a replica loads the store on takeover.
	rt.LoadRetained()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// A standby follows what the leader applied while it stands by, so its
	// takeover re-applies nothing (#706).
	if ioc != nil && el != nil && proj.Runtime.Retain != nil {
		go ioc.Follow(ctx, proj.Runtime.Retain.Load, el.IsLeader)
	}
	var dec decider
	if el != nil {
		dec = el
	}
	// The elector (or, without redundancy, the local gate) runs on its own
	// context, not the signal's: it keeps renewing the lease through the
	// last scan and the last retain save that follow a stop, so no standby
	// takes over and loads the store before this replica's final save
	// lands (nautilus#531). On the way out it stops renewing, then hands
	// the lease over instead of letting it expire: a standby takes over on
	// its next tick rather than 4s later. Release waits for the loop, so a
	// renew in flight cannot re-claim the lease after it was given up (a
	// local gate has no lease: Release does nothing).
	elCtx, elCancel := context.WithCancel(context.Background())
	elDone := make(chan struct{})
	started := time.Now() // the election starts: READY's clock for a pair member
	go func() { gate.Run(elCtx); close(elDone) }()
	defer func() {
		elCancel()
		<-elDone
		gate.Release()
	}()
	// Drivers with their own polling loop (eip, sparkplug-host, modbus)
	// start here, after the elector's first decision.
	if stop := startDriverWhenDecided(ctx, dec, firstDecisionTimeout, proj.Runtime.Driver); stop != nil {
		defer stop()
	}
	// Run returns after the last scan and, with a retain store, its last
	// save; the stop below waits for it, so an orderly stop keeps a
	// setpoint written just before it (nautilus#531).
	runDone := make(chan struct{})
	go func() { rt.Run(ctx); close(runDone) }()

	// The OPC UA server, when the manifest asks for it: the tag store as
	// an address space, fed by the store's change generations; writable
	// only where opcua.writable opens it, to a trusted client.
	opc, err := proj.OPCUAServer(rt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut run: opcua:", err)
		return 1
	}
	if opc != nil {
		// BuildInfo.SoftwareVersion is the version `naut version` prints.
		opc.SetSoftwareVersion(lsp.Version)
		if err := opc.Start(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "opcua:", err, "(continuing without it)")
		} else {
			defer opc.Stop()
		}
	}

	// Sparkplug B retransmission, when the manifest asks for it.
	spNode, err := proj.Sparkplug(rt)
	if err != nil {
		// Its errors name sparkplug already ("sparkplug: …"); add it only
		// to one that does not.
		msg := err.Error()
		if !strings.HasPrefix(msg, "sparkplug") {
			msg = "sparkplug: " + msg
		}
		fmt.Fprintln(os.Stderr, "naut run:", msg)
		return 1
	}
	sparkplugUp := false
	if spNode != nil {
		if err := spNode.Start(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "sparkplug:", err, "(continuing without it)")
		} else {
			defer spNode.Stop()
			sparkplugUp = true
		}
	}

	sopts := proj.Server
	if tok := os.Getenv("NAUTILUS_TOKEN"); tok != "" {
		sopts.AuthToken = tok
	}
	// The auth: block (#708) and its service tokens' secrets, each from
	// NAUTILUS_TOKEN_<NAME>: one that is unset, or shared, refuses to start
	// rather than run with a token nobody can present or two that look
	// alike.
	if err := withAuth(&sopts, proj.Auth, os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, "naut run: auth:", err)
		return 1
	}
	if h := os.Getenv("NAUTILUS_HISTORIAN_URL"); h != "" {
		sopts.HistorianURL = h
	}
	// Field-driver + Sparkplug status feed GET /api/drivers and the stream.
	sopts.Drivers = proj.DriverStatus(spNode, opc)
	if alarms != nil {
		sopts.Alarms = alarms.Engine
		sopts.AlarmShelveTimes = alarms.ShelveTimes
	}
	if el != nil {
		sopts.Cluster = el
	}
	if subLog != nil {
		sopts.SubstituteLog = subLog
	}
	if alarmLog != nil {
		sopts.AlarmLog = alarmLog
	}
	// Program provenance: a built binary decodes the history its archive
	// carries; a checkout captures live from git. Lazy (first request pays,
	// not startup) and nil-safe — no repo just means an empty history.
	hist := loadHistory(fsys, dir)
	sopts.History = hist
	sopts.SourcesAt = func(sha string) (map[string]string, error) {
		snap, ok := vcs.SnapshotFS(hist(), sha)
		if !ok {
			return nil, fmt.Errorf("no file snapshot captured for %s", sha)
		}
		return project.Sources(snap, manifest)
	}
	// Live I/O configuration (#706): a changed device or driver staged,
	// validated and applied over /api/io while the scan runs, and what runs
	// against the manifest on every frame.
	if ioc != nil {
		// Closed before the drivers stop and the lease is given up (defers
		// run last first): a revert under way still reaches its devices.
		defer ioc.Close()
		sopts.IO = ioc.Hook(func(sha string) (fs.FS, bool) {
			snap, ok := vcs.SnapshotFS(hist(), sha)
			return snap, ok
		}, fsys)
	}
	// naut's own build of the HMI (an app with src/routes, or the default
	// app from the mimics): served at "/", the dashboard moves to /_nautilus/
	builtHMI := buildHMIForRun(proj, dir, os.Stderr)
	if proj.Server.HMI != nil {
		sopts.HMI = proj.Server.HMI
	}
	if msg := authNotice(proj); msg != "" {
		fmt.Fprint(os.Stderr, msg)
	}
	// The audit journal (#709): every write through the tag API, accepted
	// or refused, fsynced before it is answered. If it cannot be opened the
	// controller still runs: setting forces and program edits are refused,
	// operator actions are journalled in memory only, and the drivers block
	// and the system alarm say so (#709, design §6 question 5).
	j, err := openAudit(proj, os.Stderr)
	sopts.AuditUnavailable = err
	if j != nil {
		sopts.Audit = j
		go pruneAudit(ctx, j, os.Stderr)
	}
	srv := server.New(rt, sopts)
	srvRef.Store(srv)
	if j != nil {
		// What the seam still owes (open refusal windows, entries kept
		// through a failure) is written before the journal closes.
		defer func() {
			srv.FlushAudit()
			j.Close()
		}()
	}
	go srv.Run(ctx)

	if apiUp {
		ln := apiLn
		if sopts.AuthToken == "" && !loopbackListen(addr) {
			fmt.Fprintf(os.Stderr, openListenWarning, addr)
		}
		// The HTTP port is the first thing shutdown gives back: it is
		// closed as soon as the stop arrives (below), before the wait for
		// the last scan and the deferred stops above (OPC UA sessions, the
		// Sparkplug death certificate, a driver's teardown), so a `naut
		// run` started again meanwhile still gets its port (nautilus#423,
		// #599). The deferred close above is only for an early return.
		go func() {
			if err := http.Serve(ln, srv.Handler()); err != nil && ctx.Err() == nil {
				fmt.Fprintln(os.Stderr, "tag api:", err)
			}
		}()
	}

	banner := "nautilus · " + proj.Name
	if label != "" {
		banner += " (" + label + ")"
	}
	if apiUp {
		banner += hmiBannerSuffix(proj, addr, builtHMI, os.Stderr)
		warnStaleHMI("naut run", fsys, proj, os.Stderr)
	}
	if sparkplugUp {
		banner += " — sparkplug up"
	}
	if opc != nil && opc.Status().Listening {
		banner += " — opc ua on " + opc.URL()
		if st := opc.Status(); st.AnonymousWrites && len(st.Writable) > 0 {
			banner += " (anonymous writes on)"
		}
	}
	if alarms != nil {
		banner += fmt.Sprintf(" — %d alarms", len(alarms.Defs))
	}
	if proj.Runtime.Retain != nil {
		banner += " — retain: " + proj.Runtime.Retain.Kind()
	}
	if el != nil {
		banner += " — redundancy: " + el.Status().Mode
	}
	banner += schedBannerSuffix(settledSched(rt, 500*time.Millisecond))
	fmt.Println(wrapBanner(banner+" — Ctrl+C to stop", consoleWidth(os.Stdout)))
	// READY=1 once a scan has completed or, as a replica, once the elector
	// has decided this one is a standby, which does not scan. A pair
	// member is a voter too, and must stay up with no leader to vote for
	// (a new pair waiting to be commissioned, a peer and witness both
	// away): it is ready after firstDecisionTimeout either way, or systemd
	// would restart it in a loop, each restart a new restart wait.
	go systemdNotify(ctx, sd, watch, func() bool {
		return replicaReady(rt.Stats().Count > 0, el, pair != nil, time.Since(started))
	})
	<-ctx.Done()
	// Give the HTTP port back first. Held through the wait below, a `naut
	// run` started right after the stop lost the bind and ran without its
	// tag API while this process finished its last scan (nautilus#599).
	if apiLn != nil {
		apiLn.Close()
		apiLn = nil
	}
	// Wait for the last scan and the last retain save before the deferred
	// teardown (the lease release, driver stops) runs, but not forever: a
	// store that hangs (an unreachable Kubernetes API) must not hold the
	// process past a supervisor's stop timeout.
	select {
	case <-runDone:
	case <-time.After(runStopTimeout):
		fmt.Fprintf(os.Stderr, "naut run: the scan loop did not stop within %s; exiting without its last retain save\n", runStopTimeout)
	}
	return 0
}

// bindAPI binds the tag API's address before anything else starts (#968).
// An address another process holds refuses the start: that is another
// controller, maybe this one's own service, and two must never drive the
// same I/O. On a bench (naut run from a project: dir set), with no
// NAUTILUS_ADDR and no redundancy, it moves to the next free port instead,
// said out loud. It returns the address actually bound.
//
// A built controller (dir "") must hold a fixed address, since that is its
// only exclusion: port 0 (a new port each start) and any bind failure
// refuse the start too. naut run, a bench tool, keeps running without its
// tag API on another failure, as before.
func bindAPI(addr, dir string, redundant bool) (net.Listener, string, error) {
	const why = "a built controller needs its tag API on a fixed address: holding it is what keeps a second instance off the same I/O"
	if _, port, err := net.SplitHostPort(addr); dir == "" && err == nil && port == "0" {
		return nil, addr, fmt.Errorf("tag api address %s has port 0: %s; set server.addr or NAUTILUS_ADDR to a port", addr, why)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil && addrInUse(err) {
		if dir != "" && os.Getenv("NAUTILUS_ADDR") == "" && !redundant {
			if alt, altLn := nextFreePort(addr); altLn != nil {
				fmt.Fprintf(os.Stderr, "tag api: %s is in use (another naut run?), so this one is on %s\n", addr, alt)
				return altLn, boundAddr(alt, altLn), nil
			}
		}
		return nil, addr, fmt.Errorf("%s is in use: another controller (this one's service?) is running; refusing to start a second one against the same I/O", addr)
	}
	if err != nil && dir == "" {
		return nil, addr, fmt.Errorf("tag api: %v: %s", err, why)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "tag api: %v (continuing without it)\n", err)
		return nil, addr, nil
	}
	// Name the port actually bound: with server.addr ":0" (or
	// "127.0.0.1:0") the banner and the warnings would otherwise say port
	// 0, and nothing could find the tag API.
	return ln, boundAddr(addr, ln), nil
}

// runStopTimeout bounds how long an orderly stop waits for the scan loop's
// last scan and retain save (a ConfigMap save already under way can take
// longer, up to its PUT and POST timeouts; the exit then cuts it short), leaving the driver teardown after it room
// inside a supervisor's stop timeout: the groov EPIC unit's
// TimeoutStopSec=10, Kubernetes' 30 s grace period, systemd's default 90 s.
const runStopTimeout = 5 * time.Second

// AuditAlarmID is the system alarm raised while the audit journal could
// not be opened or cannot be written (#709, design §6 question 5).
const AuditAlarmID = "nautilus.audit-journal"

// auditAlarm is the audit journal's system alarm, read from the server
// srv will hold. Unshelvable and acknowledged: while it is up, forces and
// program changes are refused and actions are journalled in memory only.
func auditAlarm(srv *atomic.Pointer[server.Server]) project.SystemAlarm {
	return project.SystemAlarm{
		Def: alarm.Def{
			ID: AuditAlarmID, Tag: "__nautilus.audit-journal-down",
			Name:     "Audit journal cannot be written: setting forces and program edits are refused",
			Priority: alarm.High, Class: "system",
			AckRequired: true, AutoClear: true,
		},
		Active: func() bool {
			s := srv.Load()
			return s != nil && s.AuditJournalDown()
		},
	}
}

// startAlarms builds the project's alarm engine over rt, before the scan
// loop starts: the engine registers itself on the runtime's post-scan
// hook, and SetAlarms hands it to the retain saver, so the first takeover
// restores the acknowledgements the last leader saved rather than
// re-annunciating them. Nil when the project has no alarms. Split out of
// runProject, which blocks on Ctrl+C, so the wiring has a test.
func startAlarms(proj *project.Project, rt *runtime.Runtime, sys ...project.SystemAlarm) (*project.Alarms, error) {
	alarms, err := proj.NewAlarms(rt, sys...)
	if err != nil || alarms == nil {
		return nil, err
	}
	rt.SetAlarms(alarms.Engine)
	return alarms, nil
}

// hmiBannerSuffix decides what runProject's banner says about the
// HMI/dashboard, printing a one-line warning to stderr when server.hmi is
// configured but not built yet. Split out of runProject (which blocks on
// Ctrl+C, so it isn't a convenient thing to unit test directly) so this
// decision — the same one `naut build` makes about its own warning — has
// a testable entry point of its own.
// boundAddr is the configured address with the port the listener actually
// got: the host stays as written ("", "0.0.0.0", "localhost"), so the banner
// reads like the manifest, and only a port 0 changes.
func boundAddr(configured string, ln net.Listener) string {
	host, _, err := net.SplitHostPort(configured)
	if err != nil {
		return ln.Addr().String()
	}
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		return configured
	}
	return net.JoinHostPort(host, port)
}

func hmiBannerSuffix(proj *project.Project, addr, built string, stderr io.Writer) string {
	switch {
	case built != "":
		return " — " + built + " + tag API on http://" + addr
	case proj.HMIDir != "" && proj.HMIMissing:
		// project.Load already left Server.HMI nil for this — the
		// built-in dashboard has "/", exactly as if server.hmi were
		// unset — but a dev who set server.hmi and expects to see their
		// own HMI deserves to know why they're not.
		fmt.Fprintf(stderr,
			"naut run: server.hmi: %s is not built yet — serving the "+
				"built-in dashboard instead; run the HMI's own build "+
				"(npm run build in %s) and restart to serve it\n",
			proj.HMIDir, project.HMIBuildHint(proj.HMIDir))
		return " — dashboard + tag API on http://" + addr
	case proj.HMIDir != "":
		return " — hmi (" + proj.HMIDir + ") + tag API on http://" + addr
	default:
		return " — dashboard + tag API on http://" + addr
	}
}

// warnStaleHMI prints a one-line warning when a source the HMI compiles
// in (its own code, the project's mimic and scene documents) changed after
// server.hmi was last built — the served or embedded HMI is then the old
// one. naut never runs the HMI's build itself: a controller has no Node.
func warnStaleHMI(cmd string, fsys fs.FS, proj *project.Project, w io.Writer) {
	if proj.HMIDir == "" || proj.HMIMissing || proj.HMIApp != "" {
		return
	}
	src := project.StaleHMISource(fsys, proj.HMIDir)
	if src == "" {
		return
	}
	hint := project.HMIBuildHint(proj.HMIDir)
	fmt.Fprintf(w, "%s: server.hmi: %s is older than %s — the HMI shows the last build; "+
		"run npm run build in %s (or npm run dev there while editing)\n", cmd, proj.HMIDir, src, hint)
}

// addrInUse reports a listen that failed because the port is taken (on
// Windows the error is WSAEADDRINUSE, which syscall.EADDRINUSE isn't).
func addrInUse(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "address already in use") || strings.Contains(msg, "Only one usage of each socket address")
}

// nextFreePort listens on the first free port of the next twenty after
// addr's, on the same host.
func nextFreePort(addr string) (string, net.Listener) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", nil
	}
	n, err := strconv.Atoi(port)
	if err != nil || n == 0 {
		return "", nil
	}
	for p := n + 1; p <= n+20 && p < 65536; p++ {
		alt := net.JoinHostPort(host, strconv.Itoa(p))
		if ln, err := net.Listen("tcp", alt); err == nil {
			return alt, ln
		}
	}
	return "", nil
}

// selfAddr is what a standby needs to reach this replica: the pod IP from
// the downward API joined with the API's listen port. Empty outside a
// cluster — standalone mode has nobody to proxy to.
func selfAddr(addr string) string {
	ip := os.Getenv("POD_IP")
	if ip == "" {
		return ""
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return ""
	}
	return net.JoinHostPort(ip, port)
}

func runRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	manifest := fs.String("m", "", manifestFlagUsage)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}
	return runProject(os.DirFS(dir), *manifest, "", dir)
}

// historyMarker is the archive entry holding a built binary's captured git
// history (a server.ProgramHistory as JSON). Dot-named like .manifest: the
// archive walk skips dotfiles, so nothing in a project can collide with it,
// and `unzip -p <binary> .history` answers "what commits shipped in this?".
const historyMarker = ".history"

// loadHistory resolves where this controller's program history comes from:
// the archive entry a `naut build` embedded, or a live git capture of
// the checkout `naut run` was pointed at. The work happens once, on
// first use — GET /api/program/history triggers it, startup never waits.
func loadHistory(fsys fs.FS, dir string) func() *server.ProgramHistory {
	return sync.OnceValue(func() *server.ProgramHistory {
		if raw, err := fs.ReadFile(fsys, historyMarker); err == nil {
			var h server.ProgramHistory
			if json.Unmarshal(raw, &h) == nil {
				return &h
			}
			return nil
		}
		if dir == "" {
			return nil
		}
		h, err := vcs.Capture(dir, vcs.DefaultDepth)
		if err != nil {
			fmt.Fprintln(os.Stderr, "program history:", err, "(continuing without it)")
			return nil
		}
		return h // nil when dir isn't a git checkout — an empty history
	})
}

// manifestFlagUsage documents -m on every command that loads a project.
// One project, one set of programs, several manifests: a site differs by
// which tag files it composes and which controller it points at, and each
// manifest is committed, so what deploys is always something you can read.
const manifestFlagUsage = "manifest to load, relative to the project " +
	"(default nautilus.yaml); e.g. sites/hayward.yaml"

// ── build: emit a deployable binary ─────────────────────────────────────────
// The output is THIS executable with the project zipped onto its tail and a
// 16-byte footer (offset + magic). On start, main() checks the footer and
// becomes the controller instead of the CLI — one file to ship, like any
// compiled program. NAUTILUS_CLI=1 forces CLI behavior on a built binary.

var embedMagic = [8]byte{'N', 'A', 'U', 'T', 'P', 'R', 'O', 'J'}

func runBuild(args []string) int {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	out := fs.String("o", "", "output binary name (default: the manifest's name)")
	manifest := fs.String("m", "", manifestFlagUsage)
	runner := fs.String("runtime", "", "the naut binary the controller is built on (default: this one); a release's linux_arm64 naut builds an ARM64 controller from any machine")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}
	// Check the runtime before the project, so a wrong path fails fast and
	// before any HMI build spends its seconds.
	var target string
	if *runner != "" {
		t, err := checkRuntime(*runner)
		if err != nil {
			fmt.Fprintln(os.Stderr, "naut build: -runtime:", err)
			return 1
		}
		target = t
	}

	// The project must load AND compile before it ships.
	proj, err := project.Load(os.DirFS(dir), *manifest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut build:", err)
		return 1
	}
	if _, err := runtime.New(proj.Runtime); err != nil {
		fmt.Fprintln(os.Stderr, "naut build: compile:", err)
		return 1
	}
	// server.hmi naming a directory that hasn't been built yet is not a
	// build error: `naut run` already falls back to the built-in
	// dashboard for the exact same reason (project.Load leaves Server.HMI
	// nil — see Project.HMIMissing), and the HMI's own build lives in a
	// separate project (npm run build) most devs haven't run before their
	// first `naut build`. Warn and ship the binary without an embedded
	// HMI — emitBinary's walk of dir simply won't find a directory that
	// isn't there, so nothing further needs to change to make that true.
	// A path that exists but isn't a directory, or one that escapes the
	// project, is already a hard error out of project.Load, above.
	if proj.HMIMissing {
		fmt.Fprintf(os.Stderr,
			"naut build: server.hmi: %s is not built yet — the binary will serve "+
				"the built-in dashboard; run the HMI's own build (npm run build in "+
				"%s) and rebuild to embed it\n",
			proj.HMIDir, project.HMIBuildHint(proj.HMIDir))
	}
	warnStaleHMI("naut build", os.DirFS(dir), proj, os.Stderr)
	// naut builds the HMI itself and ships it built, so the controller
	// serves it at boot (project.BuiltHMIDir) and never compiles: a screen
	// that doesn't build fails the build, as a program that doesn't would.
	var hmiFiles map[string][]byte
	if src := hmiSource(proj); src != "" {
		res, err := buildHMI(proj, dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "naut build: hmi: %s didn't build:\n", src)
			printBuildError(os.Stderr, "  ", err)
			return 1
		}
		printBuildWarnings(os.Stderr, "naut build: hmi: ", res.Warnings)
		hmiFiles = res.Files
		fmt.Printf("hmi: %s, %s, built in %s\n", src, plural(len(res.Pages), "page"), res.Took.Round(100*time.Millisecond))
	}

	name := *out
	if name == "" {
		name = proj.Name
	}
	self := *runner
	if self == "" {
		var err error
		if self, err = os.Executable(); err != nil {
			fmt.Fprintln(os.Stderr, "naut build:", err)
			return 1
		}
	}
	// Provenance rides the artifact: capture the project's git history here,
	// where .git exists, so the deployed controller can answer for its own
	// commits from an air-gapped network. No repo is fine — the endpoint
	// just serves an empty history.
	history, note := captureHistory(dir)
	archiveBytes, err := emitBinary(self, dir, name, *manifest, history, hmiFiles)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut build:", err)
		return 1
	}
	if target != "" {
		note += " — for " + target
	}
	fmt.Printf("built %s — a self-contained controller (run it like any binary; NAUTILUS_ADDR/NAUTILUS_TOKEN apply)%s\n", name, note)
	if archiveBytes > embedSizeWarning {
		fmt.Fprintf(os.Stderr,
			"naut build: warning: the embedded project is %.1f MB (over the %d MB comfort threshold) — "+
				"if server.hmi is set, check its build didn't drag in a dependency tree (node_modules etc. "+
				"should already be excluded; a stray copy elsewhere in the project would not be)\n",
			float64(archiveBytes)/(1024*1024), embedSizeWarning/(1024*1024))
	}
	return 0
}

// checkRuntime vets a -runtime path: it must be an executable, not the
// release archive it came in (the likeliest mistake is passing
// nautilus_<v>_linux_arm64.tar.gz). It returns the platform it targets
// ("linux/arm64") for the build line when the file is ELF, which every
// controller nautilus runs on is, and "" for another executable format.
func checkRuntime(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil {
		return "", err
	} else if !st.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a file", path)
	}
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return "", fmt.Errorf("%s is not an executable", path)
	}
	switch {
	case magic == [4]byte{0x7f, 'E', 'L', 'F'}:
		ef, err := elf.NewFile(f)
		if err != nil {
			return "", fmt.Errorf("%s: %v", path, err)
		}
		arch := map[elf.Machine]string{
			elf.EM_AARCH64: "arm64", elf.EM_ARM: "arm", elf.EM_X86_64: "amd64",
			elf.EM_386: "386", elf.EM_RISCV: "riscv64",
		}[ef.Machine]
		if arch == "" {
			arch = ef.Machine.String()
		}
		return "linux/" + arch, nil
	case magic[0] == 'M' && magic[1] == 'Z', // Windows
		magic == [4]byte{0xcf, 0xfa, 0xed, 0xfe}: // Mach-O 64-bit
		return "", nil
	case magic[0] == 0x1f && magic[1] == 0x8b:
		return "", fmt.Errorf("%s is a gzip archive: extract naut from it first (tar xzf %s naut)", path, filepath.Base(path))
	}
	return "", fmt.Errorf("%s is not an executable", path)
}

// embedSizeWarning is where `naut build` starts saying something: a
// controller binary is meant to be "one file to ship," and an HMI's own
// build output is normally a few MB of hashed JS/CSS — comfortably under
// this. Not a hard limit (the archive still embeds and the binary still
// runs), just a nudge to look at what's riding along.
const embedSizeWarning = 50 * 1024 * 1024

// captureHistory snapshots dir's git history as the JSON `.history` embeds,
// plus a note for the build line. Failures degrade to no history — a build
// must not fail because provenance couldn't be read.
func captureHistory(dir string) ([]byte, string) {
	h, err := vcs.Capture(dir, vcs.DefaultDepth)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut build: program history:", err, "(continuing without it)")
		return nil, ""
	}
	if h == nil {
		return nil, noHistoryNote(dir)
	}
	raw, err := json.Marshal(h)
	if err != nil {
		return nil, ""
	}
	note := fmt.Sprintf(" — %d commits of program history embedded", len(h.Commits))
	if h.BuiltDirty {
		note += " (working tree dirty)"
	}
	return raw, note
}

// noHistoryNote says why a build embeds no history. A directory `naut new`
// just made is a repository with no commits yet, and calling that "not a
// repo" sent people to run init in a directory that already has one.
func noHistoryNote(dir string) string {
	if err := exec.Command("git", "-C", dir, "rev-parse", "--git-dir").Run(); err == nil {
		return " — no git history embedded (no commits yet)"
	}
	return " — no git history embedded (not a repo)"
}

// manifestMarker records which manifest a built binary loads, for projects
// holding more than one (`naut build -m sites/hayward.yaml`). Every
// manifest ships in the archive — the marker only says which one boots, so
// the binary still carries the whole reviewable project. A dot-name because
// the archive walk skips dotfiles: nothing in the project can collide with
// it, and `unzip -p <binary> .manifest` answers "what is this built from?".
const manifestMarker = ".manifest"

// embeddedManifest reads the marker, or "" for a project built without -m
// (which means nautilus.yaml, the default everywhere else).
func embeddedManifest(fsys fs.FS) string {
	raw, err := fs.ReadFile(fsys, manifestMarker)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// authNotice is what naut run says about the manifest's auth: block, so
// nobody runs a controller believing a block protects it before it does.
// Today (#708 PR 4b) it decides every write through the HTTP API and
// /api/audit; reads (until sign-in, PR 5), the OPC UA server and
// Sparkplug writes (#708 step 8) are not covered yet.
func authNotice(proj *project.Project) string {
	if proj.Auth == nil {
		return ""
	}
	return authReadsNotice + authSurfacesNotice
}

const authReadsNotice = "auth: WARNING: reads are NOT GATED yet (#708, until sign-in): anyone who can reach this controller\n" +
	"      can read its tags, alarms and program. Writes and the audit journal follow the auth: block.\n"

// authFacadeNotice is what naut logix serve --project and naut siemens
// serve say about an auth: block: they serve the HTTP API only, with no
// audit journal.
const authFacadeNotice = "auth: WARNING: reads are NOT GATED yet (#708, until sign-in): anyone who can reach this serve\n" +
	"      can read the controller's tags and program. Writes and deploys follow the auth: block.\n"

const authSurfacesNotice = "auth: WARNING: the OPC UA server and Sparkplug writes are NOT covered by the auth: block yet (#708):\n" +
	"      a client that reaches them is not asked for a role.\n"

// withAuth puts the manifest's auth: block (#708) on sopts, with each
// service token's secret from NAUTILUS_TOKEN_<NAME> (serviceTokenSecrets).
// Nil (no block) leaves sopts as it is. naut run and naut logix serve
// --project both serve a manifest that can hold the block, so both
// enforce it through here.
func withAuth(sopts *server.Options, pol *auth.Policy, getenv func(string) string) error {
	if pol == nil {
		return nil
	}
	secrets, err := serviceTokenSecrets(pol.ServiceTokens(), sopts.AuthToken, getenv)
	if err != nil {
		return err
	}
	sopts.Auth, sopts.ServiceTokens = pol, secrets
	return nil
}

// serviceTokenSecrets reads each service token's secret from
// NAUTILUS_TOKEN_<NAME>. A missing one is an error naming the variable
// (never a value), and so is a secret that is NAUTILUS_TOKEN's or another
// token's: a request presenting it would be two principals.
func serviceTokenSecrets(names []string, authToken string, getenv func(string) string) (map[string]string, error) {
	out := map[string]string{}
	seen := map[string]string{}
	if authToken != "" {
		seen[authToken] = "NAUTILUS_TOKEN"
	}
	var missing []string
	for _, name := range names {
		env := "NAUTILUS_TOKEN_" + strings.ToUpper(name)
		secret := getenv(env)
		if secret == "" {
			missing = append(missing, env)
			continue
		}
		if other, dup := seen[secret]; dup {
			return nil, fmt.Errorf("%s is the same secret as %s: give each token its own", env, other)
		}
		seen[secret] = env
		out[name] = secret
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("service tokens without a secret: set %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// openAudit opens the audit journal the manifest's audit: section places
// (default ./audit). A failure is reported, not fatal: a read-only working
// directory must not stop a controller, and the server keeps the entries
// in memory instead.
func openAudit(proj *project.Project, stderr io.Writer) (*audit.FileJournal, error) {
	dir, keep := proj.AuditJournal()
	j, err := audit.OpenFile(dir, audit.FileOptions{Keep: keep})
	if err != nil {
		fmt.Fprintf(stderr, "audit: ERROR: %v: actions are allowed but journalled in memory only, lost on restart (set audit.path in nautilus.yaml to a writable directory)\n", err)
		return nil, err
	}
	return j, nil
}

// pruneAudit removes journal files older than audit.keep-days, at start
// and then daily.
func pruneAudit(ctx context.Context, j *audit.FileJournal, stderr io.Writer) {
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		if _, err := j.Prune(); err != nil {
			fmt.Fprintf(stderr, "audit: prune: %v\n", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// embedSkipDirs names project subdirectories that never ship, however large
// they are on disk: dependency trees a package manager can regenerate, not
// project sources. This matters more once an hmi/ directory can sit inside
// the project (server.hmi points at its *build* output, e.g. hmi/build) —
// without this, a `naut build` run from beside an un-.gitignore'd
// node_modules would embed the whole SvelteKit toolchain into the CLI
// binary instead of the few static files server.hmi actually needs. Named
// the same as runCheck's own source walk (cmd/naut/check.go), for the
// same reason: neither is a project source.
var embedSkipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true}

// isPKIStore reports whether dir is an OPC UA certificate store (it holds
// own/private, where the server's private key lives): `naut run` from the
// project directory makes ./pki there by default, and a key must never ride
// along inside a built binary.
func isPKIStore(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, "own", "private"))
	return err == nil && fi.IsDir()
}

// pemPrivateKey reports a PEM file holding a private key, wherever it sits
// in the project.
func pemPrivateKey(raw []byte) bool {
	head := raw[:min(len(raw), 4096)]
	return bytes.HasPrefix(bytes.TrimSpace(head), []byte("-----BEGIN")) && bytes.Contains(head, []byte("PRIVATE KEY-----"))
}

// emitBinary appends dir's project (plus manifest/history markers) onto a
// copy of the runner at self, writing the result to out. It returns the
// archive's uncompressed byte total — runBuild's size warning threshold —
// alongside the usual error.
func emitBinary(self, dir, out, manifest string, history []byte, hmi map[string][]byte) (int64, error) {
	src, err := os.Open(self)
	if err != nil {
		return 0, err
	}
	defer src.Close()
	f, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	// 1. the runner: this executable, byte for byte (minus any project a
	//    previous build appended to it).
	base, err := runnerSize(src)
	if err != nil {
		return 0, err
	}
	if _, err := io.CopyN(f, src, base); err != nil {
		return 0, err
	}
	// 2. the project archive.
	zipStart := base
	var archiveBytes int64
	// The virtual devices are test fixtures, like the suites: they gate
	// the deploy and do not ride along on the controller.
	fixtures := map[string]bool{}
	if set, err := loadVirtualDevices(dir, manifest); err == nil {
		for _, f := range set.Files() {
			if abs, err := filepath.Abs(f); err == nil {
				fixtures[abs] = true
			}
		}
	} else if _, serr := os.Stat(filepath.Join(dir, virtdev.FileName)); serr == nil {
		if abs, err := filepath.Abs(filepath.Join(dir, virtdev.FileName)); err == nil {
			fixtures[abs] = true
		}
	}
	zw := zip.NewWriter(f)
	err = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if embedSkipDirs[d.Name()] || isPKIStore(p) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		// Ship the sources, not the artifacts: skip dotdirs, prior builds,
		// and the acceptance suites — tests gate the deploy, they do not
		// ride along on the controller.
		if strings.HasPrefix(rel, ".") || rel == out || rel == filepath.Base(out) ||
			strings.HasSuffix(rel, acceptance.SuffixTest) {
			return nil
		}
		if abs, err := filepath.Abs(p); err == nil && fixtures[abs] {
			return nil
		}
		// A vendored profile ships (the project loads its devices from it);
		// the simulator beside it is a test fixture and does not.
		if strings.HasPrefix(rel, devprofile.ProjectDir+"/") && path.Ext(rel) != ".yaml" {
			return nil
		}
		// The audit journal `naut run` keeps (./audit by default) is this
		// machine's record, never part of the program.
		if audit.IsJournalFile(d.Name()) || d.Name() == audit.LockName {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if pemPrivateKey(raw) {
			return nil // a key (an OPC UA client's pki-dir in the project, say) never ships
		}
		w, err := zw.Create(rel)
		if err != nil {
			return err
		}
		archiveBytes += int64(len(raw))
		_, err = w.Write(raw)
		return err
	})
	if err != nil {
		return 0, err
	}
	if manifest != "" {
		w, err := zw.Create(manifestMarker)
		if err != nil {
			return 0, err
		}
		if _, err := w.Write([]byte(manifest)); err != nil {
			return 0, err
		}
	}
	// the HMI naut built, served from there as it is (project.Load)
	hmiNames := make([]string, 0, len(hmi))
	for n := range hmi {
		hmiNames = append(hmiNames, n)
	}
	sort.Strings(hmiNames)
	for _, n := range hmiNames {
		w, err := zw.Create(project.BuiltHMIDir + "/" + n)
		if err != nil {
			return 0, err
		}
		if _, err := w.Write(hmi[n]); err != nil {
			return 0, err
		}
		archiveBytes += int64(len(hmi[n]))
	}
	if len(history) > 0 {
		w, err := zw.Create(historyMarker)
		if err != nil {
			return 0, err
		}
		if _, err := w.Write(history); err != nil {
			return 0, err
		}
	}
	if err := zw.Close(); err != nil {
		return 0, err
	}
	// 3. the footer: where the zip starts, and the magic that marks a
	//    built controller.
	var footer [16]byte
	binary.LittleEndian.PutUint64(footer[:8], uint64(zipStart))
	copy(footer[8:], embedMagic[:])
	_, err = f.Write(footer[:])
	return archiveBytes, err
}

// runnerSize is where the executable's own bytes end: the whole file for a
// plain CLI, or the embedded zip's start when building FROM a built binary.
func runnerSize(f *os.File) (int64, error) {
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if off, ok := embeddedOffset(f, st.Size()); ok {
		return off, nil
	}
	return st.Size(), nil
}

func embeddedOffset(r io.ReaderAt, size int64) (int64, bool) {
	if size < 16 {
		return 0, false
	}
	var footer [16]byte
	if _, err := r.ReadAt(footer[:], size-16); err != nil {
		return 0, false
	}
	if [8]byte(footer[8:16]) != embedMagic {
		return 0, false
	}
	off := int64(binary.LittleEndian.Uint64(footer[:8]))
	if off < 0 || off >= size-16 {
		return 0, false
	}
	return off, true
}

// wantsHelp reports whether a built controller was asked for its usage. The
// binary takes no arguments, so a person who types --help is asking what it
// is, not asking it to start — and starting a controller (which also writes
// retain.json into the working directory) is a poor answer.
func wantsHelp(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "-h", "-help", "--help", "help":
		return true
	}
	return false
}

// builtArgs answers a built controller's arguments: help and its version,
// each printed without starting it, and anything else refused, also
// without starting (#968). done is false only with no arguments: then it
// runs.
func builtArgs(args []string, name string, stdout, stderr io.Writer) (code int, done bool) {
	if len(args) == 0 {
		return 0, false
	}
	switch {
	case wantsHelp(args):
		fmt.Fprint(stdout, builtUsage(name))
		return 0, true
	case args[0] == "version" || args[0] == "-v" || args[0] == "--version" || args[0] == "-version":
		fmt.Fprintln(stdout, "nautilus", lsp.Version)
		return 0, true
	}
	fmt.Fprintf(stderr, "%s: unknown argument %q: a built controller takes none (it starts with none; %s version prints its version, %s --help how it is configured)\n", name, args[0], name, name)
	return 2, true
}

// builtUsage is what a built controller prints for --help: how it is
// configured, since it has no flags to list.
func builtUsage(name string) string {
	return "usage: " + name + `

A controller built by naut build. Run with no arguments, it starts the scan
loop, the dashboard, and the tag API, using the project embedded in this
file. It refuses to start while another process holds its tag API address
(another instance, or its own service): two must never drive the same I/O.

  ` + name + ` version    print the naut version it was built with
  ` + name + ` --help     this

Environment:
  NAUTILUS_ADDR            listen address, overriding server.addr in the manifest
  NAUTILUS_TOKEN           bearer token every write requires, and every
                           request by a name not listed below
  NAUTILUS_ALLOWED_HOSTS   host names (comma-separated) the controller
                           answers by without the token, besides localhost
                           and an IP address
  NAUTILUS_HISTORIAN_URL   historian the dashboard queries for trends
  NAUTILUS_NODE            a or b: which controller of a redundant pair
                           (redundancy: peers:) this one is
  NAUTILUS_PEER_TOKEN      the secret every peer and witness request carries
  NAUTILUS_CLUSTER         the pair's cluster ID, drawn when it is commissioned
  NAUTILUS_CLI=1           run the naut command line instead of the controller

Retained state (the manifest's retain: block) is written to retain.json in the
working directory unless it targets a Kubernetes ConfigMap.
`
}

// embeddedProject opens the project a `naut build` appended to this
// executable, if any.
func embeddedProject() (fs.FS, bool) {
	if os.Getenv("NAUTILUS_CLI") == "1" {
		return nil, false
	}
	self, err := os.Executable()
	if err != nil {
		return nil, false
	}
	f, err := os.Open(self)
	if err != nil {
		return nil, false
	}
	st, err := f.Stat()
	if err != nil {
		return nil, false
	}
	off, ok := embeddedOffset(f, st.Size())
	if !ok {
		return nil, false
	}
	zr, err := zip.NewReader(io.NewSectionReader(f, off, st.Size()-16-off), st.Size()-16-off)
	if err != nil {
		return nil, false
	}
	// f stays open for the process lifetime — the zip reads from it lazily.
	return zr, true
}

// settledSched is the runtime's stats once every task that asked for a CPU
// or a priority has been placed or refused (the loops do it as they start),
// or after wait, whichever is first.
func settledSched(rt *runtime.Runtime, wait time.Duration) runtime.ScanStats {
	deadline := time.Now().Add(wait)
	for {
		s := rt.Stats()
		pending := schedPending(s.Sched)
		for _, t := range s.Tasks {
			pending = pending || schedPending(t.Sched)
		}
		if !pending || time.Now().After(deadline) {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func schedPending(s runtime.SchedStats) bool {
	return (len(s.CPUs) > 0 || s.Priority > 0) && !s.Applied && s.Error == ""
}

// schedBannerSuffix says, on the banner, which tasks did not get the CPU or
// real-time priority they asked for: a refusal is in the log and the task's
// scan.sched too, but a controller meant to run real-time that silently
// does not is the kind of wrong to say at start (#836).
func schedBannerSuffix(s runtime.ScanStats) string {
	var realtime, pin []string
	note := func(name string, sc runtime.SchedStats) {
		switch {
		case sc.Error == "":
		case sc.Priority > 0:
			realtime = append(realtime, name)
		default: // cpu: alone, no priority asked
			pin = append(pin, name)
		}
	}
	note(runtime.MainTaskName, s.Sched)
	for _, t := range s.Tasks {
		note(t.Name, t.Sched)
	}
	out := ""
	if len(realtime) > 0 {
		out += " — REAL-TIME REFUSED for " + strings.Join(realtime, ", ")
	}
	if len(pin) > 0 {
		out += " — CPU PIN FAILED for " + strings.Join(pin, ", ")
	}
	if out != "" {
		out += " (see the log)"
	}
	// The runtime raised GOMAXPROCS for a task at a priority on a box with
	// few cores (#830); an operator who set it, or a box with cores to
	// spare, sees nothing here.
	if s.Procs.RaisedFrom > 0 {
		out += fmt.Sprintf(" — GOMAXPROCS %d (raised from %d: a task requests a real-time priority)", s.Procs.Value, s.Procs.RaisedFrom)
	}
	return out
}
