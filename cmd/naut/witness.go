package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/joyautomation/nautilus/internal/peertls"
	"github.com/joyautomation/nautilus/quorum/node"
)

const witnessUsage = `naut witness: the third voter of a redundant controller pair
(docs/design/redundancy.md). It runs only the peer listener and answers
votes; it never runs a project. One witness can serve several pairs, each
keyed by its cluster ID (drawn when the pair is commissioned) and bound to
its two controllers, with one state file per pair. It also keeps the third
copy of each pair's operator-intent log (who substituted or acknowledged
what, and when): plant operator data, kept in --state beside the votes.

Usage: naut witness [flags]

  --listen addr   the peer port (default :9460): the address the pair's
                  manifest names under redundancy: witness:
  --state dir     where each pair's state file lives (default
                  $STATE_DIRECTORY, which systemd's StateDirectory= sets).
                  Never a temporary directory: a lost state is a new
                  incarnation, which the pair adopts only together.
  --lease d       this witness's promise (default 1s). At least every
                  pair's lease: a vote with a longer lease is refused,
                  and the pair then runs without fault tolerance. A
                  pair with its own redundancy.lease gives the witness
                  the same --lease.
  --rebuild       start over a state file that can't be read: it is moved
                  aside and its pair gets a new incarnation (its log copy
                  is moved aside with it, and the pair's leader fills it
                  again). Without it, such a file stops the start. Never
                  restore --state from a backup: rebuild instead.
  --peer-tls on|off  TLS on the peer port (default on), keyed by the peer
                  token: the pairs' redundancy.peer-tls. Off is for a lab
                  bench only: votes, the log and the token cross the
                  network in the clear.

NAUTILUS_PEER_TOKEN is required: every peer request must carry it, and
the witness answers nothing without it.
`

// witnessMaxConns caps the witness's open connections: each pair keeps a
// few alive per controller.
const witnessMaxConns = 256

func runWitness(args []string) int {
	useConsoleLog()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return witnessMain(ctx, args, os.Getenv, os.Stdout, os.Stderr, nil)
}

// witnessMain runs the witness until ctx ends. ready, when not nil, gets
// the address it listens on once it answers.
func witnessMain(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer, ready chan<- string) int {
	if len(args) > 0 && args[0] == "unpin" {
		return witnessUnpinMain(args[1:], getenv, stdout, stderr)
	}
	fs := flag.NewFlagSet("witness", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, witnessUsage) }
	stateDefault, _, _ := strings.Cut(getenv("STATE_DIRECTORY"), ":")
	listen := fs.String("listen", ":9460", "")
	state := fs.String("state", stateDefault, "")
	lease := fs.Duration("lease", node.DefaultLease, "")
	rebuild := fs.Bool("rebuild", false, "")
	peerTLS := fs.String("peer-tls", "", "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "naut witness: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	token := getenv("NAUTILUS_PEER_TOKEN")
	if token == "" {
		fmt.Fprintln(stderr, "naut witness: NAUTILUS_PEER_TOKEN is not set: the witness answers only peers that carry it, so it refuses to start without one")
		return 1
	}
	if *state == "" {
		fmt.Fprintln(stderr, "naut witness: no state directory: pass --state, or run it under systemd with StateDirectory=")
		return 1
	}
	if *lease <= 0 {
		fmt.Fprintln(stderr, "naut witness: --lease must be positive")
		return 2
	}
	switch *peerTLS {
	case "", "on", "off":
	default:
		fmt.Fprintf(stderr, "naut witness: --peer-tls %q: want on (the default) or off\n", *peerTLS)
		return 2
	}
	stateDir := *state
	// The witness, once built: its certificate names each pair's
	// incarnation (#710 2b), witness@0 before.
	var wit atomic.Pointer[node.Witness]
	pins := newPinLog("witness")
	pins.slogOnly = true // the witness has no journal: its log is the record
	pt, err := buildPeerTLS(peertls.Options{Token: token, Self: peertls.Witness,
		// One witness serves many pairs: its key is bound to its box (no
		// links to name, so its physical NICs or its machine-id alone), its
		// pins kept per cluster beside the cluster's members file.
		KeyFile: filepath.Join(stateDir, peerKeyFile),
		Box:     func() (peertls.Box, error) { return witnessKeyBox(nil) },
		PinsFile: func(cluster string) string {
			return filepath.Join(stateDir, cluster+"."+peerPinsFile)
		},
		Accept:  func(c, n string) bool { return node.ValidCluster(c) && (n == "a" || n == "b") },
		OnEvent: pins.event,
		Incarnation: func(cluster string) uint64 {
			if w := wit.Load(); w != nil {
				return w.Incarnation(cluster)
			}
			return 0
		},
	}, peerTLSIsOff(*peerTLS), "--peer-tls=off here and redundancy.peer-tls: off on the pair")
	if err != nil {
		fmt.Fprintln(stderr, "naut witness:", err)
		return 1
	}
	if pt == nil {
		fmt.Fprintln(stderr, "naut witness: warning: "+peerTLSOff)
	}
	w, err := node.NewWitness(node.WitnessOptions{Dir: *state, Token: token, Lease: *lease, Rebuild: *rebuild, StateWrite: testStateWrite})
	if err != nil {
		fmt.Fprintln(stderr, "naut witness:", err)
		return 1
	}
	defer w.Close()
	wit.Store(w)
	// Each pair's operator-intent log beside its votes (#710 PR 3b-4b).
	logs := newWitnessLogs(w, *state, token, *lease, slog.Default())
	defer logs.Close()
	mux := http.NewServeMux()
	mux.Handle(node.Prefix, w.Handler())
	mux.Handle(node.Prefix+"{cluster}/log/", logs.Handler())
	if pt != nil {
		mux.Handle("POST "+node.Prefix+"{cluster}"+node.UnpinPath, witnessUnpin(pt, w.Bound))
	}
	var bound http.Handler = mux
	if pt != nil {
		bound = pt.Bind(mux, node.WithPeer) // every request: the caller's key against its pin
	}
	h, err := node.Guard(token, bound)
	if err != nil {
		fmt.Fprintln(stderr, "naut witness:", err)
		return 1
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintln(stderr, "naut witness:", err)
		return 1
	}
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() { w.Run(wctx); close(done) }()
	// A vote is one small frame: a slow or idle client is cut off, and at
	// most witnessMaxConns connections are served at once.
	srv := peerServer(h)
	served := make(chan error, 1)
	lim := node.LimitListener(ln, witnessMaxConns)
	if pt != nil {
		lim = tls.NewListener(lim, pt.Server())
	}
	go func() { served <- srv.Serve(lim) }()
	wire := "TLS"
	if pt == nil {
		wire = "plain HTTP (peer TLS off)"
	} else if fp, err := pt.Fingerprint(); err != nil {
		fmt.Fprintln(stderr, "naut witness: its peer key:", err)
		return 1
	} else {
		wire = "TLS, key " + fp
	}
	fmt.Fprintf(stdout, "naut witness — peer port %s, %s — state %s — lease %v — Ctrl+C to stop\n", ln.Addr(), wire, *state, *lease)
	if ready != nil {
		ready <- ln.Addr().String()
	}
	code := 0
	select {
	case <-ctx.Done():
	case err := <-served:
		fmt.Fprintln(stderr, "naut witness:", err)
		code = 1
	}
	sctx, scancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer scancel()
	_ = srv.Shutdown(sctx)
	cancel()
	<-done
	return code
}
