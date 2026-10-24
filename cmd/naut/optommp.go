package main

// `naut optommp` is the commissioning side of an Opto 22 groov chassis:
// browse (list the modules and channels the processor reports, with live
// values) and import (turn that chassis into an optommp_manifest.yaml plus
// tags/optommp.yaml for `driver: {type: optommp}` — or, with --modbus, a
// modbus_manifest.yaml on the processor's Modbus/TCP map for the ordinary
// modbus driver). Opto 22's spellings stay inside optommp/.

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
        Live: scan the chassis and write optommp_manifest.yaml +
        tags/optommp.yaml binding every I/O channel for the optommp driver
        (driver: {type: optommp, manifest: optommp_manifest.yaml}): state
        or value with per-channel quality, outputs read back. Re-run after
        a module or channel-type change in groov Manage; byte-identical
        when nothing changed.
  naut optommp import --modbus --host <ip> [flags]
        The same chassis as modbus_manifest.yaml on the processor's own
        Modbus/TCP map, for driver: {type: modbus} (no per-channel quality
        beyond NaN).

Flags:
  --host         Processor IP or hostname (required)
  --port         OptoMMP port (default 2001)
  --timeout      Per-request timeout (default 3s)
Import flags:
  --id           Tag prefix (and Modbus source id) (default EPIC)
  --out          Output directory (default ".")
  --tags-out     Tag file to emit, relative to --out (default tags/optommp.yaml)
  --manifest-host  Host the manifest dials (default --host); 127.0.0.1 for a
                 project that runs on the processor itself
  --scan-rate    Poll interval written into the manifest (default: the driver's 100ms)
  --latches      Add <tag>_OnLatch / _OffLatch for every digital input: true
                 for one scan when the channel turned on / off since the
                 previous scan (the driver reads and clears the latch)
  --quality      Add <tag>_Quality for every analog channel: the processor's
                 quality indicator code, 0 = good
  --scratch-bits N, --scratch-ints N, --scratch-floats N
                 Add N writable scratchpad tags of that kind from element 0
                 (<id>_SP_B0, _I0, _F0): the registers a PAC Control
                 strategy or groov View on the same unit shares
  --modbus       Write the Modbus projection instead
  --modbus-port  Modbus/TCP port that manifest polls (default 502)

Tag names are <id>_M<slot>_<DI|DO|AI|AO><channel>: EPIC_M0_DI03 is slot 0
channel 3, the same with or without --modbus. A digital channel with a
feature configured in groov Manage (counter, totalizer …) also gets
<tag>_Count. Outputs (DO, AO) are writable.
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
	id := fs.String("id", "EPIC", "tag prefix (and Modbus source id)")
	outDir := fs.String("out", ".", "output directory")
	tagsOut := fs.String("tags-out", "tags/optommp.yaml", "tag file to emit, relative to --out")
	manifestHost := fs.String("manifest-host", "", "host the manifest dials (default --host)")
	scanRate := fs.Duration("scan-rate", 0, "poll interval written into the manifest")
	latches := fs.Bool("latches", false, "add on/off-latch tags for every digital input")
	quality := fs.Bool("quality", false, "add a quality-code tag for every analog channel")
	sBits := fs.Int("scratch-bits", 0, "writable scratchpad bit tags to add")
	sInts := fs.Int("scratch-ints", 0, "writable scratchpad integer tags to add")
	sFloats := fs.Int("scratch-floats", 0, "writable scratchpad float tags to add")
	asModbus := fs.Bool("modbus", false, "write the Modbus projection instead")
	modbusPort := fs.Int("modbus-port", 502, "Modbus/TCP port the --modbus manifest polls")
	ch, ok := optommpScan(fs, args, host, port, timeout, "import")
	if !ok {
		return 1
	}
	dialHost := *manifestHost
	if dialHost == "" {
		dialHost = *host
	}
	fail := func(err error) int {
		fmt.Fprintln(os.Stderr, "naut optommp import:", err)
		return 1
	}

	var manifestPath, command string
	var manifestYAML, tagsYAML []byte
	var bindings int
	if *asModbus {
		opts := optommp.MapOptions{ID: *id, Host: dialHost, Port: *modbusPort}
		if opts.Port == 502 {
			opts.Port = 0
		}
		out, err := optommp.Generate(ch, opts)
		if err != nil {
			return fail(err)
		}
		command = optommp.Command(opts)
		manifestPath = filepath.Join(*outDir, "modbus_manifest.yaml")
		manifestYAML = codegen.ManifestYAML(out.Manifest, command)
		if tagsYAML, err = optommp.TagsYAML(out, command, *tagsOut); err != nil {
			return fail(err)
		}
		bindings = len(out.Manifest.Tags)
	} else {
		opts := optommp.ManifestOptions{
			ID: *id, Host: dialHost, Port: *port, ScanRate: *scanRate,
			Latches: *latches, Quality: *quality,
			ScratchBits: *sBits, ScratchInts: *sInts, ScratchFloats: *sFloats,
		}
		if opts.Port == optommp.DefaultPort {
			opts.Port = 0
		}
		m, err := optommp.GenerateManifest(ch, opts)
		if err != nil {
			return fail(err)
		}
		command = optommp.ManifestCommand(opts)
		manifestPath = filepath.Join(*outDir, "optommp_manifest.yaml")
		manifestYAML = optommp.ManifestYAML(m, command)
		if tagsYAML, err = optommp.ManifestTagsYAML(m, command, *tagsOut); err != nil {
			return fail(err)
		}
		bindings = len(m.Tags)
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return fail(err)
	}
	if err := os.WriteFile(manifestPath, manifestYAML, 0o644); err != nil {
		return fail(err)
	}
	tagsPath := filepath.Join(*outDir, *tagsOut)
	if err := os.MkdirAll(filepath.Dir(tagsPath), 0o755); err != nil {
		return fail(err)
	}
	if err := os.WriteFile(tagsPath, tagsYAML, 0o644); err != nil {
		return fail(err)
	}
	fmt.Printf("%s firmware %s: %d module(s)\n", ch.Part, ch.Firmware, len(ch.Modules))
	if *asModbus {
		fmt.Printf("wrote %s (%d tag bindings on the processor's Modbus/TCP server) — driver: {type: modbus}\n", manifestPath, bindings)
	} else {
		fmt.Printf("wrote %s (%d tag bindings) — driver: {type: optommp, manifest: optommp_manifest.yaml}\n", manifestPath, bindings)
	}
	fmt.Printf("wrote %s — compose it with `tag-files: [%s]`\n", tagsPath, *tagsOut)
	return 0
}
