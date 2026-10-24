package main

// `naut plcnext` is the commissioning side of running nautilus on a
// Phoenix Contact PLCnext controller: browse (what the firmware's data
// access says about BusConductor and the process image — run ON the
// controller, where the socket is) and tags (derive the tag file from a
// committed plcnext_manifest.yaml, anywhere).

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
        On the controller: report BusConductor's state (CONFIGURED,
        NUM_MODULES) and dump the first bytes of the input and output
        images, so the offsets in a manifest can be read against the
        modules actually on the bus. --no-handshake only reads; by
        default it pulses CONFIG_REQ / START_IO_REQ first, as the driver
        does at start.
  naut plcnext tags <plcnext_manifest.yaml> [-o tags/plcnext.yaml]
        Anywhere: derive the tag file from the committed manifest.

Flags:
  --socket         The firmware's gRPC Unix socket (default /run/plcnext/grpc.sock)
  --target         Any grpc target instead of the socket (a test server's host:port)
  --bus-conductor  BusConductor component instance (default BcComponent1; "" = none)
  --inputs         Input image port (default Arp.Io.AxlC/0.DI4096)
  --outputs        Output image port (default Arp.Io.AxlC/0.DO4096)
  --bytes          How many image bytes to dump (default 32)
  --no-handshake   Do not pulse BusConductor; just read

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
	case "tags":
		return runPlcnextTags(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "naut plcnext: unknown subcommand %q\n\n%s", args[0], plcnextUsage)
		return 2
	}
}

func runPlcnextBrowse(args []string) int {
	fs := flag.NewFlagSet("plcnext browse", flag.ContinueOnError)
	socket := fs.String("socket", plcnext.DefaultSocket, "firmware gRPC Unix socket")
	target := fs.String("target", "", "grpc target instead of the socket")
	component := fs.String("bus-conductor", plcnext.DefaultComponent, "BusConductor component instance")
	inputs := fs.String("inputs", plcnext.DefaultInputs, "input image port")
	outputs := fs.String("outputs", plcnext.DefaultOutputs, "output image port")
	n := fs.Int("bytes", 32, "image bytes to dump")
	noHandshake := fs.Bool("no-handshake", false, "do not pulse BusConductor")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *target == "" {
		*target = plcnext.SocketTarget(*socket)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := plcnext.Dial(ctx, *target)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut plcnext browse:", err)
		return 1
	}
	defer c.Close()

	if *component != "" {
		p := func(name string) string { return *component + "/BusConductor." + name }
		if !*noHandshake {
			for _, step := range []struct {
				port string
				v    bool
			}{{"CONFIG_MUST_MATCH", false}, {"CONFIG_REQ", true}} {
				if err := c.WriteBool(ctx, p(step.port), step.v); err != nil {
					fmt.Fprintf(os.Stderr, "naut plcnext browse: %v\n  (is BusConductor deployed and the plcnext runtime restarted since? see docs/design/open-controllers.md)\n", err)
					return 1
				}
			}
			deadline := time.Now().Add(20 * time.Second)
			for {
				ok, err := c.ReadBool(ctx, p("CONFIGURED"))
				if err != nil {
					fmt.Fprintln(os.Stderr, "naut plcnext browse:", err)
					return 1
				}
				if ok || time.Now().After(deadline) {
					break
				}
				time.Sleep(250 * time.Millisecond)
			}
			_ = c.WriteBool(ctx, p("CONFIG_REQ"), false)
			_ = c.WriteBool(ctx, p("START_IO_REQ"), true)
			_ = c.WriteBool(ctx, p("START_IO_REQ"), false)
		}
		configured, err := c.ReadBool(ctx, p("CONFIGURED"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "naut plcnext browse:", err)
			return 1
		}
		modules, _ := c.ReadNumber(ctx, p("NUM_MODULES"))
		fmt.Printf("%s: CONFIGURED=%t NUM_MODULES=%d\n", *component, configured, int(modules))
	}
	for _, port := range []string{*inputs, *outputs} {
		img, err := c.ReadBytes(ctx, port)
		if err != nil {
			fmt.Printf("%s: %v\n", port, err)
			continue
		}
		fmt.Printf("%s: %d bytes\n", port, len(img))
		fmt.Print(hexdump(img[:min(*n, len(img))]))
	}
	return 0
}

func hexdump(b []byte) string {
	var sb strings.Builder
	for i := 0; i < len(b); i += 16 {
		end := min(i+16, len(b))
		fmt.Fprintf(&sb, "  %04x ", i)
		for j := i; j < end; j++ {
			fmt.Fprintf(&sb, " %02x", b[j])
		}
		sb.WriteString("\n")
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
