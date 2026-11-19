package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joyautomation/nautilus/quorum/node"
)

const witnessUsage = `naut witness: the third voter of a redundant controller pair
(docs/design/redundancy.md). It runs only the peer listener and answers
votes; it never runs a project. One witness can serve several pairs, each
keyed by its project name, with one state file per pair.

Usage: naut witness [flags]

  --listen addr   the peer port (default :9460): the address the pair's
                  manifest names under redundancy: witness:
  --state dir     where each pair's state file lives (default
                  $STATE_DIRECTORY, which systemd's StateDirectory= sets).
                  Never a temporary directory: a lost state is a new
                  incarnation, which the pair adopts only together.
  --lease d       this witness's promise (default 2s). At least every
                  pair's lease: a vote with a longer lease is refused.
  --rebuild       start over a state file that can't be read: it is moved
                  aside and its pair gets a new incarnation. Without it,
                  such a file stops the start.

NAUTILUS_PEER_TOKEN is required: every peer request must carry it, and
the witness answers nothing without it.
`

func runWitness(args []string) int {
	useConsoleLog()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return witnessMain(ctx, args, os.Getenv, os.Stdout, os.Stderr, nil)
}

// witnessMain runs the witness until ctx ends. ready, when not nil, gets
// the address it listens on once it answers.
func witnessMain(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer, ready chan<- string) int {
	fs := flag.NewFlagSet("witness", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, witnessUsage) }
	stateDefault, _, _ := strings.Cut(getenv("STATE_DIRECTORY"), ":")
	listen := fs.String("listen", ":9460", "")
	state := fs.String("state", stateDefault, "")
	lease := fs.Duration("lease", node.DefaultLease, "")
	rebuild := fs.Bool("rebuild", false, "")
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
	w, err := node.NewWitness(node.WitnessOptions{Dir: *state, Token: token, Lease: *lease, Rebuild: *rebuild})
	if err != nil {
		fmt.Fprintln(stderr, "naut witness:", err)
		return 1
	}
	defer w.Close()
	h, err := node.Guard(token, w.Handler())
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
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 2 * time.Minute}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	fmt.Fprintf(stdout, "naut witness — peer port %s — state %s — lease %v — Ctrl+C to stop\n", ln.Addr(), *state, *lease)
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
