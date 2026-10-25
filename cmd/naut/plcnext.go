package main

// `naut plcnext` is the commissioning side of running nautilus on a
// Phoenix Contact PLCnext controller: browse (bring the bus up through
// BusConductor and dump the mirrored process image — run ON the
// controller, where the socket is), read / write (any GDS name, to learn
// what it is before a manifest binds it), shm (look at the in-process
// shim's exchange page — what it read from the bus, what it wrote to it,
// its cycle), and tags (derive the tag file from a committed
// plcnext_manifest.yaml, anywhere).

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/plcnext"
)

const plcnextUsage = `naut plcnext — Phoenix Contact PLCnext tools (nautilus as the runtime on the controller)

Usage:
  naut plcnext browse [--socket /run/plcnext/grpc.sock] [--bus-conductor BcComponent1]
        On the controller: bring the local bus up through BusConductor
        the way the driver does at start (idempotent: a configured,
        running bus is left alone), report CONFIGURED / NUM_MODULES /
        RUN, and dump the first bytes of the mirrored input and output
        images, so the offsets in a manifest can be read against the
        modules actually on the bus. --no-handshake only reads.
  naut plcnext read [--socket …] <port> [<port> …]
        On the controller: read any GDS name through the data-access
        service and print its type and value — the way to learn what a
        name is (and whether the firmware will answer for it) before a
        manifest binds it. Array elements: Name[3]; struct fields: Name.f.
  naut plcnext write [--socket …] <port> <value>
        On the controller: write a scalar port as its own type (true,
        17, 2.5 …). BusConductor's requests act on a RISING EDGE sampled
        every 100 ms: write false, then true, to make one.
  naut plcnext shm [--shm /dev/shm/nautilus-plcnext-io] [--watch 250ms]
        On the controller: read the exchange page of rt/plcnext-io's
        nautilus_io (transport: shm) without writing it — the shim's
        cycle and lateness, the bus RUN bit, the input frame, and the
        output bytes it put on the bus (its own view of the frame), with
        who owns them and how fresh they are. --watch prints one line per
        interval until interrupted.
  naut plcnext bench [--for 5m] [--label …] [--out bench.jsonl] [--api http://127.0.0.1:8080]
        On the controller: measure one window of the shim and naut — the
        shim's lateness percentiles and worst 10 s window, naut's scan
        lateness, and the CPU each used — for comparing runs (best-effort
        against SCHED_FIFO, quiet against loaded). --out appends a JSON line.
  naut plcnext tags <plcnext_manifest.yaml> [-o tags/plcnext.yaml]
        Anywhere: derive the tag file from the committed manifest.

Flags:
  --socket         The firmware's gRPC Unix socket (default /run/plcnext/grpc.sock)
  --target         Any grpc target instead of the socket (a test server's host:port)
  --bus-conductor  BusConductor component instance (default BcComponent1; "" = none)
  --run-port       The bus RUN diagnostic (default Arp.Plc.Eclr/AXIO_DIAG_STATUS_REG_RUN; "" = none)
  --inputs         Mirrored input image port (default Arp.Plc.Eclr/PND_S1_INPUTS)
  --outputs        Mirrored output image port (default Arp.Plc.Eclr/PND_S1_OUTPUTS)
  --bytes          How many image bytes to dump (default 32)
  --no-handshake   Do not touch BusConductor; just read

Deploying a project to the controller (an AXC F 2152 is 32-bit ARMv7):
  CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -o naut ./cmd/naut
  naut build -o controller .      # the project embedded on the tail of THAT naut
  scp controller admin@<plc>:/opt/plcnext/apps/nautilus/ && ssh admin@<plc> /opt/plcnext/apps/nautilus/controller
`

func runPlcnext(args []string) int {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, plcnextUsage)
		return 2
	}
	switch args[0] {
	case "browse":
		return runPlcnextBrowse(args[1:])
	case "read":
		return runPlcnextRead(args[1:])
	case "write":
		return runPlcnextWrite(args[1:])
	case "shm":
		return runPlcnextShm(args[1:])
	case "bench":
		return runPlcnextBench(args[1:])
	case "tags":
		return runPlcnextTags(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "naut plcnext: unknown subcommand %q\n\n%s", args[0], plcnextUsage)
		return 2
	}
}

// plcnextConn is the dial every on-controller subcommand shares.
func plcnextConn(fs *flag.FlagSet, name string) (*plcnext.Client, context.Context, context.CancelFunc, bool) {
	socket := fs.Lookup("socket").Value.String()
	target := fs.Lookup("target").Value.String()
	if target == "" {
		target = plcnext.SocketTarget(socket)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	c, err := plcnext.Dial(ctx, target)
	if err != nil {
		cancel()
		fmt.Fprintf(os.Stderr, "naut plcnext %s: %v\n", name, err)
		return nil, nil, nil, false
	}
	return c, ctx, cancel, true
}

func plcnextFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("plcnext "+name, flag.ContinueOnError)
	fs.String("socket", plcnext.DefaultSocket, "firmware gRPC Unix socket")
	fs.String("target", "", "grpc target instead of the socket")
	return fs
}

func runPlcnextBrowse(args []string) int {
	fs := plcnextFlags("browse")
	component := fs.String("bus-conductor", plcnext.DefaultComponent, "BusConductor component instance")
	runPort := fs.String("run-port", plcnext.DefaultRunPort, "bus RUN diagnostic port")
	inputs := fs.String("inputs", plcnext.DefaultInputs, "mirrored input image port")
	outputs := fs.String("outputs", plcnext.DefaultOutputs, "mirrored output image port")
	n := fs.Int("bytes", 32, "image bytes to dump")
	noHandshake := fs.Bool("no-handshake", false, "do not touch BusConductor")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	c, ctx, cancel, ok := plcnextConn(fs, "browse")
	if !ok {
		return 1
	}
	defer cancel()
	defer c.Close()

	if !*noHandshake {
		st, err := plcnext.Handshake(ctx, c, *component, *runPort)
		if err != nil {
			fmt.Fprintf(os.Stderr, "naut plcnext browse: %v\n  (is BusConductor deployed and the plcnext runtime restarted since? see examples/plcnext-bench/README.md)\n", err)
			return 1
		}
		fmt.Printf("%s: CONFIGURED=%t NUM_MODULES=%d RUN=%t\n", *component, st.Configured, st.Modules, st.Running)
	} else if *runPort != "" {
		if run, err := c.ReadBool(ctx, *runPort); err == nil {
			fmt.Printf("%s = %t\n", *runPort, run)
		} else {
			fmt.Printf("%s: %v\n", *runPort, err)
		}
	}
	rc := 0
	for _, port := range []string{*inputs, *outputs} {
		img, _, err := c.ReadBytes(ctx, port, *n)
		if err != nil {
			fmt.Printf("%s: %v\n", port, err)
			rc = 1
			continue
		}
		fmt.Printf("%s[0..%d):\n", port, len(img))
		fmt.Print(hexdump(img))
	}
	return rc
}

func runPlcnextRead(args []string) int {
	fs := plcnextFlags("read")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "naut plcnext read: at least one port name")
		return 2
	}
	c, ctx, cancel, ok := plcnextConn(fs, "read")
	if !ok {
		return 1
	}
	defer cancel()
	defer c.Close()
	rc := 0
	for _, port := range fs.Args() {
		desc, err := c.Describe(ctx, port)
		if err != nil {
			fmt.Printf("%s: %v\n", port, err)
			rc = 1
			continue
		}
		fmt.Printf("%s: %s\n", port, desc)
	}
	return rc
}

func runPlcnextWrite(args []string) int {
	fs := plcnextFlags("write")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "naut plcnext write: <port> <value>")
		return 2
	}
	c, ctx, cancel, ok := plcnextConn(fs, "write")
	if !ok {
		return 1
	}
	defer cancel()
	defer c.Close()
	port, text := fs.Arg(0), fs.Arg(1)
	if err := c.WriteScalar(ctx, port, text); err != nil {
		fmt.Fprintf(os.Stderr, "naut plcnext write: %v\n", err)
		return 1
	}
	desc, err := c.Describe(ctx, port)
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut plcnext write: written, but reading back: %v\n", err)
		return 1
	}
	fmt.Printf("%s: %s\n", port, desc)
	return 0
}

func runPlcnextShm(args []string) int {
	fs := flag.NewFlagSet("plcnext shm", flag.ContinueOnError)
	path := fs.String("shm", plcnext.DefaultShm, "the shim's exchange page")
	n := fs.Int("bytes", 16, "frame bytes to dump")
	watch := fs.Duration("watch", 0, "print one line per interval until interrupted")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	*n = min(max(*n, 1), 512)
	v, err := plcnext.OpenShm(*path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut plcnext shm: %v\n", err)
		return 1
	}
	defer v.Close()
	st, stampAge, frameAge, err := v.Read()
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut plcnext shm: %v\n", err)
		return 1
	}
	if *watch > 0 {
		for {
			fmt.Printf("in % x | out % x | run %s | input age %.2f ms | gen %d/%d%s\n",
				st.Inputs[:min(*n, 8)], st.Echo[:min(*n, 8)], runText(st), plcnextMs(frameAge), st.Applied, st.OutGen, wdText(st))
			time.Sleep(*watch)
			if st, _, frameAge, err = v.Read(); err != nil {
				fmt.Fprintf(os.Stderr, "naut plcnext shm: %v\n", err)
				return 1
			}
		}
	}
	sched := "best-effort"
	if st.FIFO() {
		sched = fmt.Sprintf("SCHED_FIFO %d", st.Prio)
	}
	if st.CPU > 0 {
		sched += fmt.Sprintf(" on CPU %d", st.CPU-1)
	}
	fmt.Printf("nautilus_io pid %d: %d µs cycle, %s; status %#02x (PLC started %t, bus RUN %s)\n",
		st.PID, st.PeriodNs/1000, sched, st.Status, st.PLC(), runText(st))
	fmt.Printf("last cycle %.2f ms ago; input frame %.2f ms old\n", plcnextMs(stampAge), plcnextMs(frameAge))
	fmt.Printf("%d cycles, %d late (>%d µs, %.3f %%), worst %.2f ms, this cycle %d µs\n",
		st.Cycles, st.Late, st.LateUs, plcnextPct(st.Late, st.Cycles), float64(st.WorstUs)/1000, st.LastUs)
	if st.OutLen == 0 {
		fmt.Println("outputs: nobody has written the page; the frame is held at zero")
	} else {
		fmt.Printf("outputs: naut owns %d byte(s), generation %d (%d applied to the frame), watchdog %d ms%s; torn reads %d\n",
			st.OutLen, st.OutGen, st.Applied, st.WatchdogMs, wdText(st), st.Torn)
	}
	fmt.Printf("input frame[0..%d):\n%s", *n, hexdump(st.Inputs[:*n]))
	fmt.Printf("output frame[0..%d), as the shim wrote it:\n%s", *n, hexdump(st.Echo[:*n]))
	return 0
}

func runText(st plcnext.ShimState) string {
	switch {
	case !st.RunKnown():
		return "unknown"
	case st.Running():
		return "true"
	default:
		return "false"
	}
}

func wdText(st plcnext.ShimState) string {
	if st.Watchdog() {
		return " — WATCHDOG: outputs stale, frame driven to zero"
	}
	return ""
}

func plcnextMs(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func plcnextPct(a, b uint64) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}

func hexdump(b []byte) string {
	var sb strings.Builder
	for i := 0; i < len(b); i += 16 {
		end := min(i+16, len(b))
		fmt.Fprintf(&sb, "  %04x:", i)
		for _, x := range b[i:end] {
			fmt.Fprintf(&sb, " %02x", x)
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

func runPlcnextTags(args []string) int {
	fs := flag.NewFlagSet("plcnext tags", flag.ContinueOnError)
	out := fs.String("out", "tags/plcnext.yaml", "output tag file")
	fs.StringVar(out, "o", "tags/plcnext.yaml", "output tag file (alias for --out)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: naut plcnext tags [-o tags/plcnext.yaml] <plcnext_manifest.yaml>")
		return 2
	}
	raw, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut plcnext tags:", err)
		return 1
	}
	m, err := plcnext.ParseManifest(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut plcnext tags: %s: %v\n", fs.Arg(0), err)
		return 1
	}
	body, err := plcnext.TagsYAML(m, *out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut plcnext tags:", err)
		return 1
	}
	if dir := filepath.Dir(*out); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "naut plcnext tags:", err)
			return 1
		}
	}
	if err := os.WriteFile(*out, body, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "naut plcnext tags:", err)
		return 1
	}
	fmt.Printf("wrote %s (%d tags) — compose it with `tag-files: [%s]`\n", *out, len(m.Tags), *out)
	return 0
}
