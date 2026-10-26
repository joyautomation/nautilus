package main

// `naut optommp` is the commissioning side of an Opto 22 groov chassis:
// browse (list the modules and channels the processor reports, with live
// values) and import (turn that chassis into a modbus_manifest.yaml plus
// tags/optommp.yaml the ordinary `modbus` driver polls). Nothing here is
// a driver: the processor's own Modbus/TCP server is the scan-rate
// transport, so the runtime gains no new dependency and no new driver type,
// and Opto 22's spellings stay inside optommp/.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/joyautomation/nautilus/modbus/codegen"
	"github.com/joyautomation/nautilus/optommp"
)

const optommpUsage = `naut optommp — Opto 22 groov EPIC / groov RIO tools (OptoMMP, TCP 2001)

Usage:
  naut optommp browse --host <ip> [flags]
        Live: list every module and configured channel the processor
        reports — part number, channel type, unit, state or value.
  naut optommp import --host <ip> [flags]
        Live: scan the chassis and write modbus_manifest.yaml +
        tags/optommp.yaml binding every I/O channel to the processor's own
        Modbus/TCP server. Run it with driver: {type: modbus, manifest:
        modbus_manifest.yaml}. Re-run after a module or channel-type change
        in groov Manage; byte-identical when nothing changed.

Flags:
  --host         Processor IP or hostname (required)
  --port         OptoMMP port (default 2001)
  --timeout      Per-request timeout (default 3s)
Import flags:
  --id           Manifest source id and tag prefix (default EPIC)
  --modbus-port  Modbus/TCP port the manifest polls (default 502)
  --out          Output directory (default ".")
  --tags-out     Tag file to emit, relative to --out (default tags/optommp.yaml)

Tag names are <id>_M<slot>_<DI|DO|AI|AO><channel>: EPIC_M0_DI03 is slot 0
channel 3. Outputs (DO, AO) are writable bindings; the runtime's writes
reach the module through the processor's coils and holding registers.
`

func runOptommp(args []string) int {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, optommpUsage)
		return 2
	}
	switch args[0] {
	case "browse":
		return runOptommpBrowse(args[1:])
	case "import":
		return runOptommpImport(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "naut optommp: unknown subcommand %q\n\n%s", args[0], optommpUsage)
		return 2
	}
}

func optommpScan(fs *flag.FlagSet, args []string, host *string, port *int, timeout *time.Duration, verb string) (optommp.Chassis, bool) {
	if err := fs.Parse(args); err != nil {
		return optommp.Chassis{}, false
	}
	if *host == "" {
		fmt.Fprintf(os.Stderr, "naut optommp %s: --host is required\n", verb)
		return optommp.Chassis{}, false
	}
	c, err := optommp.Dial(context.Background(), *host, *port, *timeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut optommp %s: %v\n", verb, err)
		return optommp.Chassis{}, false
	}
	defer c.Close()
	ch, err := optommp.Scan(c)
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut optommp %s: %v\n", verb, err)
		return optommp.Chassis{}, false
	}
	return ch, true
}

func runOptommpBrowse(args []string) int {
	fs := flag.NewFlagSet("optommp browse", flag.ContinueOnError)
	host := fs.String("host", "", "processor IP or hostname")
	port := fs.Int("port", optommp.DefaultPort, "OptoMMP port")
	timeout := fs.Duration("timeout", 3*time.Second, "per-request timeout")
	ch, ok := optommpScan(fs, args, host, port, timeout, "browse")
	if !ok {
		return 1
	}
	fmt.Print(ch.String())
	return 0
}

func runOptommpImport(args []string) int {
	fs := flag.NewFlagSet("optommp import", flag.ContinueOnError)
	host := fs.String("host", "", "processor IP or hostname")
	port := fs.Int("port", optommp.DefaultPort, "OptoMMP port")
	timeout := fs.Duration("timeout", 3*time.Second, "per-request timeout")
	id := fs.String("id", "EPIC", "manifest source id and tag prefix")
	modbusPort := fs.Int("modbus-port", 502, "Modbus/TCP port the manifest polls")
	outDir := fs.String("out", ".", "output directory")
	tagsOut := fs.String("tags-out", "tags/optommp.yaml", "tag file to emit, relative to --out")
	ch, ok := optommpScan(fs, args, host, port, timeout, "import")
	if !ok {
		return 1
	}
	opts := optommp.MapOptions{ID: *id, Host: *host, Port: *modbusPort}
	if opts.Port == 502 {
		opts.Port = 0
	}
	out, err := optommp.Generate(ch, opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut optommp import:", err)
		return 1
	}
	command := optommp.Command(opts)
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "naut optommp import:", err)
		return 1
	}
	manifestPath := filepath.Join(*outDir, "modbus_manifest.yaml")
	if err := os.WriteFile(manifestPath, codegen.ManifestYAML(out.Manifest, command), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "naut optommp import:", err)
		return 1
	}
	tagsYAML, err := optommp.TagsYAML(out, command, *tagsOut)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut optommp import:", err)
		return 1
	}
	tagsPath := filepath.Join(*outDir, *tagsOut)
	if dir := filepath.Dir(tagsPath); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "naut optommp import:", err)
			return 1
		}
	}
	if err := os.WriteFile(tagsPath, tagsYAML, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "naut optommp import:", err)
		return 1
	}
	fmt.Printf("%s firmware %s: %d module(s)\n", ch.Part, ch.Firmware, len(ch.Modules))
	fmt.Printf("wrote %s (%d tag bindings on the processor's Modbus/TCP server)\n", manifestPath, len(out.Manifest.Tags))
	fmt.Printf("wrote %s — compose it with `tag-files: [%s]`\n", tagsPath, *tagsOut)
	return 0
}
