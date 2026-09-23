package main

// `naut modbus` is the commissioning side of the Modbus TCP driver:
// import (a committed device map → modbus_manifest.yaml + tags/modbus.yaml,
// offline), browse (read a live register range raw + decoded), serve (stand
// in for a skid of devices from the manifest alone), and tags (re-derive the
// tag file, no map needed).
//
// It mirrors `naut eip import|browse|tags` deliberately: one importer
// per protocol, one shape to learn. The structural difference is that the
// device map is committed and offline — sixty devices' register maps come
// off datasheets, not off the wire — so import never dials anything.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/joyautomation/nautilus/modbus"
	"github.com/joyautomation/nautilus/modbus/codegen"
	"github.com/joyautomation/nautilus/modbus/slave"
)

const modbusUsage = `naut modbus — Modbus TCP tools

Usage:
  naut modbus import --map devices.yaml [flags]
        Offline: generate modbus_manifest.yaml + tags/modbus.yaml from a
        committed device map (device types + instances; the schema is
        documented in modbus/codegen). Byte-identical on re-run.
  naut modbus browse --host <ip> [flags]
        Live: read a register range from one device and print every address
        raw and decoded in every format — the commissioning poke.
  naut modbus serve --manifest modbus_manifest.yaml [flags]
        Serve every source in a manifest as one in-process Modbus TCP slave
        (multi-unit on one listener) — a bench stand-in for the devices.
  naut modbus tags <modbus_manifest.yaml> [flags]
        Re-derive the tag file from an already-committed manifest — no map
        needed (--map recovers unit/desc/init, -o path).

Import flags:
  --map        Device map YAML (required)
  --instance   Generate for one instance id only (default: all)
  --tags       Comma-separated glob patterns selecting generated tags
  --writable   Comma-separated globs marking extra tags writable
  --out        Output directory (default ".")
  --tags-out   Tag file to emit, relative to --out (default tags/modbus.yaml)
  --tags-skip  Comma-separated globs to leave OUT of the tag file, for tags
               the project declares by hand
  --plan       Print the block-read plan the driver would poll with

Browse flags:
  --host       Device IP or hostname (required)
  --port       TCP port (default 502)
  --unit       Unit id (default 1)
  --table      holding | input | coil | discrete (default holding)
  --from       First 0-based PDU address (default 0; 40001 is holding 0)
  --count      Addresses to read (default 10)
  --format     Decode as one format only (int16 uint16 int32 uint32 float32
               float64 bool bit:N); default: every format side by side
  --word-order Register order of multi-register values: big | little
  --byte-order Byte order within each register: big | little
  --timeout    Per-request timeout (default 3s)

Serve flags:
  --manifest   The manifest whose sources to serve (required)
  --listen     Listen address (default 127.0.0.1:5020)
  --values     JSON file of {tag: value} seeding the registers through the
               manifest's bindings (encoded with each source's word order)
  --ramp       Drift the numeric input tags slowly, for a live-looking bench
`

func runModbus(args []string) int {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, modbusUsage)
		return 2
	}
	switch args[0] {
	case "import":
		return runModbusImport(args[1:])
	case "browse":
		return runModbusBrowse(args[1:])
	case "serve":
		return runModbusServe(args[1:])
	case "tags":
		return runModbusTags(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "naut modbus: unknown subcommand %q\n\n%s", args[0], modbusUsage)
		return 2
	}
}

// ── import ───────────────────────────────────────────────────────────────

func runModbusImport(args []string) int {
	fs := flag.NewFlagSet("modbus import", flag.ContinueOnError)
	mapPath := fs.String("map", "", "device map YAML")
	instance := fs.String("instance", "", "generate for one instance id only")
	tags := fs.String("tags", "", "comma-separated glob patterns selecting generated tags")
	writable := fs.String("writable", "", "comma-separated globs marking extra tags writable")
	outDir := fs.String("out", ".", "output directory")
	tagsOut := fs.String("tags-out", "tags/modbus.yaml", "tag file to emit, relative to --out")
	tagsSkip := fs.String("tags-skip", "", "comma-separated globs to leave OUT of the tag file")
	plan := fs.Bool("plan", false, "print the block-read plan")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *mapPath == "" {
		fmt.Fprintln(os.Stderr, "naut modbus import: --map is required")
		return 2
	}

	raw, err := os.ReadFile(*mapPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut modbus import:", err)
		return 1
	}
	dm, err := codegen.ParseDeviceMap(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut modbus import: %s: %v\n", *mapPath, err)
		return 1
	}
	out, err := codegen.Generate(dm, codegen.Options{
		Instance: *instance,
		Writable: splitPatterns(*writable),
		Tags:     splitPatterns(*tags),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut modbus import:", err)
		return 1
	}

	// The generating command rides in the manifest header so the file says
	// how to regenerate itself. The map path is reduced to its base name —
	// the same map from a different working directory must produce the same
	// bytes, or "byte-identical on re-run" is a lie.
	command := "naut modbus import --map " + filepath.Base(*mapPath)
	if *instance != "" {
		command += " --instance " + *instance
	}
	if *tags != "" {
		command += " --tags " + *tags
	}
	if *writable != "" {
		command += " --writable " + *writable
	}

	if *plan {
		p, err := modbus.BuildPlan(out.Manifest, -1)
		if err != nil {
			fmt.Fprintln(os.Stderr, "naut modbus import:", err)
			return 1
		}
		fmt.Print(p.String())
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "naut modbus import:", err)
		return 1
	}
	manifestPath := filepath.Join(*outDir, "modbus_manifest.yaml")
	if err := os.WriteFile(manifestPath, codegen.ManifestYAML(out.Manifest, command), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "naut modbus import:", err)
		return 1
	}
	tagsYAML, err := codegen.TagsYAML(out.Manifest, out.Meta, splitPatterns(*tagsSkip))
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut modbus import:", err)
		return 1
	}
	tagsPath := filepath.Join(*outDir, *tagsOut)
	if dir := filepath.Dir(tagsPath); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "naut modbus import:", err)
			return 1
		}
	}
	if err := os.WriteFile(tagsPath, tagsYAML, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "naut modbus import:", err)
		return 1
	}
	fmt.Printf("wrote %s (%d sources, %d tag bindings)\n",
		manifestPath, len(out.Manifest.Sources), len(out.Manifest.Tags))
	fmt.Printf("wrote %s — compose it with `tag-files: [%s]`\n", tagsPath, *tagsOut)
	return 0
}

// ── tags ─────────────────────────────────────────────────────────────────

// runModbusTags re-derives the tag file from an ALREADY COMMITTED manifest,
// so it can be regenerated during review or in CI with nothing but the
// repo. The manifest alone knows names and roles; --map recovers the
// unit/desc/init columns and makes the output byte-identical to import's.
func runModbusTags(args []string) int {
	fs := flag.NewFlagSet("modbus tags", flag.ContinueOnError)
	out := fs.String("out", "tags/modbus.yaml", "output tag file")
	fs.StringVar(out, "o", "tags/modbus.yaml", "output tag file (alias for --out)")
	skip := fs.String("skip", "", "comma-separated globs to leave OUT, for tags declared by hand")
	mapPath := fs.String("map", "", "device map, to recover unit/desc/init")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: naut modbus tags [-o tags/modbus.yaml] [--map devices.yaml] <modbus_manifest.yaml>")
		return 2
	}
	raw, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut modbus tags:", err)
		return 1
	}
	m, err := modbus.ParseManifest(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut modbus tags: %s: %v\n", fs.Arg(0), err)
		return 1
	}
	var meta map[string]codegen.TagMeta
	if *mapPath != "" {
		mraw, err := os.ReadFile(*mapPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "naut modbus tags:", err)
			return 1
		}
		dm, err := codegen.ParseDeviceMap(mraw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "naut modbus tags: %s: %v\n", *mapPath, err)
			return 1
		}
		meta = codegen.MetaFromMap(dm)
	}
	body, err := codegen.TagsYAML(m, meta, splitPatterns(*skip))
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut modbus tags:", err)
		return 1
	}
	if dir := filepath.Dir(*out); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "naut modbus tags:", err)
			return 1
		}
	}
	if err := os.WriteFile(*out, body, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "naut modbus tags:", err)
		return 1
	}
	fmt.Printf("wrote %s (%d tag bindings) — compose it with `tag-files: [%s]`\n",
		*out, len(m.Tags), *out)
	return 0
}

// ── browse ───────────────────────────────────────────────────────────────

func runModbusBrowse(args []string) int {
	fs := flag.NewFlagSet("modbus browse", flag.ContinueOnError)
	host := fs.String("host", "", "device IP or hostname")
	port := fs.Int("port", 502, "TCP port")
	unit := fs.Int("unit", 1, "unit id")
	table := fs.String("table", "holding", "holding | input | coil | discrete")
	from := fs.Int("from", 0, "first 0-based PDU address")
	count := fs.Int("count", 10, "addresses to read")
	format := fs.String("format", "", "decode as one format only")
	wordOrder := fs.String("word-order", "big", "register order of multi-register values")
	byteOrder := fs.String("byte-order", "big", "byte order within each register")
	timeout := fs.Duration("timeout", 3*time.Second, "per-request timeout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *host == "" {
		fmt.Fprintln(os.Stderr, "naut modbus browse: --host is required")
		return 2
	}
	switch *table {
	case modbus.TableHolding, modbus.TableInput, modbus.TableCoil, modbus.TableDiscrete:
	default:
		fmt.Fprintf(os.Stderr, "naut modbus browse: unknown table %q (want holding, input, coil or discrete)\n", *table)
		return 2
	}
	if *unit < 0 || *unit > 255 {
		fmt.Fprintln(os.Stderr, "naut modbus browse: --unit must be 0..255")
		return 2
	}
	if *from < 0 || *count < 1 || *from+*count > 0x10000 {
		fmt.Fprintln(os.Stderr, "naut modbus browse: --from/--count must fit the 16-bit address space")
		return 2
	}
	var f modbus.Format
	if *format != "" {
		var err error
		if f, err = modbus.ParseFormat(*format); err != nil {
			fmt.Fprintln(os.Stderr, "naut modbus browse:", err)
			return 2
		}
	}

	addr := net.JoinHostPort(*host, strconv.Itoa(*port))
	ctx := context.Background()
	cl, err := modbus.Dial(ctx, addr, *timeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut modbus browse: cannot connect to %s: %v\n", addr, err)
		return 1
	}
	defer cl.Close()

	u := uint8(*unit)
	if *table == modbus.TableCoil || *table == modbus.TableDiscrete {
		bits, err := readAllBits(ctx, cl, u, *table, uint16(*from), *count)
		if err != nil {
			return browseError(err, u, *timeout)
		}
		for i, v := range bits {
			fmt.Printf("%-6d %t\n", *from+i, v)
		}
		return 0
	}

	regs, err := readAllRegisters(ctx, cl, u, *table, uint16(*from), *count)
	if err != nil {
		return browseError(err, u, *timeout)
	}
	printRegisters(os.Stdout, *from, regs, f, *wordOrder, *byteOrder)
	return 0
}

// readAllRegisters reads count registers in protocol-sized chunks over one
// connection.
func readAllRegisters(ctx context.Context, cl *modbus.Client, unit uint8, table string, from uint16, count int) ([]uint16, error) {
	regs := make([]uint16, 0, count)
	for got := 0; got < count; {
		n := min(count-got, 125)
		r, err := cl.ReadRegisters(ctx, unit, table, from+uint16(got), uint16(n))
		if err != nil {
			return nil, err
		}
		regs = append(regs, r...)
		got += n
	}
	return regs, nil
}

func readAllBits(ctx context.Context, cl *modbus.Client, unit uint8, table string, from uint16, count int) ([]bool, error) {
	bits := make([]bool, 0, count)
	for got := 0; got < count; {
		n := min(count-got, 2000)
		b, err := cl.ReadBits(ctx, unit, table, from+uint16(got), uint16(n))
		if err != nil {
			return nil, err
		}
		bits = append(bits, b...)
		got += n
	}
	return bits, nil
}

// printRegisters renders the browse table: every address raw, then decoded
// — in the one requested format, or in every format side by side so a tech
// can spot which one makes the numbers sane.
func printRegisters(w io.Writer, from int, regs []uint16, f modbus.Format, wordOrder, byteOrder string) {
	tw := tabwriter.NewWriter(w, 2, 0, 2, ' ', 0)
	defer tw.Flush()
	if f.Kind != "" {
		fmt.Fprintln(tw, "addr\traw\t"+f.String())
		for i := 0; i+f.Words() <= len(regs); i += f.Words() {
			v, err := modbus.Decode(f, regs[i:i+f.Words()], wordOrder, byteOrder, 0, 0)
			cell := fmt.Sprintf("%v", v)
			if err != nil {
				cell = "?"
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\n", from+i, hexWords(regs[i:i+f.Words()]), cell)
		}
		return
	}
	fmt.Fprintln(tw, "addr\traw\tuint16\tint16\tint32\tuint32\tfloat32\tfloat64")
	for i := range regs {
		cells := []string{
			decodeCell("uint16", regs[i:i+1], wordOrder, byteOrder),
			decodeCell("int16", regs[i:i+1], wordOrder, byteOrder),
		}
		for _, kind := range []string{"int32", "uint32", "float32"} {
			if i+2 <= len(regs) {
				cells = append(cells, decodeCell(kind, regs[i:i+2], wordOrder, byteOrder))
			} else {
				cells = append(cells, "")
			}
		}
		if i+4 <= len(regs) {
			cells = append(cells, decodeCell("float64", regs[i:i+4], wordOrder, byteOrder))
		} else {
			cells = append(cells, "")
		}
		fmt.Fprintf(tw, "%d\t0x%04X\t%s\n", from+i, regs[i], strings.Join(cells, "\t"))
	}
}

func decodeCell(kind string, regs []uint16, wordOrder, byteOrder string) string {
	f, err := modbus.ParseFormat(kind)
	if err != nil {
		return "?"
	}
	v, err := modbus.Decode(f, regs, wordOrder, byteOrder, 0, 0)
	if err != nil {
		return "?"
	}
	if fv, ok := v.(float64); ok {
		return strconv.FormatFloat(fv, 'g', 6, 64)
	}
	return fmt.Sprintf("%v", v)
}

func hexWords(regs []uint16) string {
	parts := make([]string, len(regs))
	for i, r := range regs {
		parts[i] = fmt.Sprintf("%04X", r)
	}
	return "0x" + strings.Join(parts, " ")
}

// browseError renders a live-read failure the way a commissioning tech
// needs it: what the device SAID, not just what Go saw.
func browseError(err error, unit uint8, timeout time.Duration) int {
	var exc modbus.ExceptionError
	var nerr net.Error
	switch {
	case errors.As(err, &exc):
		fmt.Fprintf(os.Stderr, "naut modbus browse: %v\n"+
			"  the device answered and refused this request — wrong table, an\n"+
			"  unimplemented address in the range, or the wrong unit id.\n", err)
	case errors.As(err, &nerr) && nerr.Timeout():
		fmt.Fprintf(os.Stderr, "naut modbus browse: no answer from unit %d within %s —\n"+
			"  wrong unit id, or a gateway drop with no device behind it?\n", unit, timeout)
	default:
		fmt.Fprintln(os.Stderr, "naut modbus browse:", err)
	}
	return 1
}

// ── serve ────────────────────────────────────────────────────────────────

func runModbusServe(args []string) int {
	fs := flag.NewFlagSet("modbus serve", flag.ContinueOnError)
	manifest := fs.String("manifest", "", "the manifest whose sources to serve")
	listen := fs.String("listen", "127.0.0.1:5020", "listen address")
	valuesPath := fs.String("values", "", "JSON file of {tag: value} seeds")
	ramp := fs.Bool("ramp", false, "drift numeric input tags slowly")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *manifest == "" {
		fmt.Fprintln(os.Stderr, "naut modbus serve: --manifest is required")
		return 2
	}
	raw, err := os.ReadFile(*manifest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut modbus serve:", err)
		return 1
	}
	m, err := modbus.ParseManifest(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut modbus serve: %s: %v\n", *manifest, err)
		return 1
	}
	var values map[string]any
	if *valuesPath != "" {
		vraw, err := os.ReadFile(*valuesPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "naut modbus serve:", err)
			return 1
		}
		if err := json.Unmarshal(vraw, &values); err != nil {
			fmt.Fprintf(os.Stderr, "naut modbus serve: %s: %v\n", *valuesPath, err)
			return 1
		}
	}
	srv, stop, err := startModbusServe(m, *listen, values, *ramp, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut modbus serve:", err)
		return 1
	}
	defer stop()
	fmt.Printf("listening on %s — ctrl-c to stop\n", srv.Addr())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	<-ch
	return 0
}

// startModbusServe stands the slave up: validate the manifest, refuse unit
// collisions one listener cannot express, seed the values, start the ramp.
// Exported to the package (not the module) so the smoke test drives it
// in-process instead of exec'ing a child that never returns.
func startModbusServe(m modbus.Manifest, listen string, values map[string]any, ramp bool, out io.Writer) (*slave.Server, func(), error) {
	plan, err := modbus.BuildPlan(m, -1)
	if err != nil {
		return nil, nil, err
	}

	srcByID := make(map[string]modbus.Source, len(m.Sources))
	for _, s := range m.Sources {
		srcByID[s.ID] = s
	}
	// One listener answers by unit id alone: two sources sharing a unit id
	// AND a table would be indistinguishable on the wire. Different hosts
	// in the manifest do not help — serve collapses them onto one address.
	type tableKey struct {
		unit  uint8
		table string
	}
	owner := map[tableKey]string{}
	for _, t := range m.Tags {
		k := tableKey{srcByID[t.Source].UnitID, t.Table}
		if prev, ok := owner[k]; ok && prev != t.Source {
			return nil, nil, fmt.Errorf(
				"sources %s and %s share unit id %d and both bind the %s table — one listener cannot tell them apart",
				prev, t.Source, k.unit, t.Table)
		}
		owner[k] = t.Source
	}

	srv := slave.NewServer(listen, nil)
	if err := srv.Start(); err != nil {
		return nil, nil, err
	}
	for _, s := range m.Sources {
		srv.Unit(s.UnitID) // create the store, so the unit answers zeros rather than exception 0x0B
	}

	byName := make(map[string]modbus.TagBinding, len(m.Tags))
	for _, t := range m.Tags {
		byName[t.Name] = t
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	base := map[string]float64{} // seeded engineering values, for the ramp
	for _, name := range names {
		t, ok := byName[name]
		if !ok {
			srv.Stop()
			return nil, nil, fmt.Errorf("--values: no binding named %q in the manifest", name)
		}
		if err := seedBinding(srv, srcByID[t.Source], t, values[name]); err != nil {
			srv.Stop()
			return nil, nil, fmt.Errorf("--values: %s: %w", name, err)
		}
		if f, ok := values[name].(float64); ok {
			base[name] = f
		}
	}

	stopRamp := func() {}
	if ramp {
		done := make(chan struct{})
		tick := time.NewTicker(500 * time.Millisecond)
		go func() {
			phase := 0.0
			for {
				select {
				case <-done:
					return
				case <-tick.C:
				}
				phase += 0.1
				for _, t := range m.Tags {
					if t.Writable || !(t.Table == modbus.TableHolding || t.Table == modbus.TableInput) {
						continue
					}
					f, err := modbus.ParseFormat(t.Format)
					if err != nil || f.Kind == "bool" || f.Kind == "bit" {
						continue
					}
					scale := t.Scale
					if scale == 0 {
						scale = 1
					}
					// ~10 raw counts of drift, always non-negative so
					// unsigned formats never refuse the encode.
					v := base[t.Name] + 10*math.Abs(scale)*(0.5+0.5*math.Sin(phase))
					_ = seedBinding(srv, srcByID[t.Source], t, v)
				}
			}
		}()
		stopRamp = func() { tick.Stop(); close(done) }
	}

	fmt.Fprintf(out, "serving %d source(s):\n", len(m.Sources))
	for _, s := range m.Sources {
		fmt.Fprintf(out, "  %s: unit %d (manifest says %s)\n", s.ID, s.UnitID, s.Addr())
	}
	fmt.Fprint(out, plan.String())
	return srv, func() { stopRamp(); srv.Stop() }, nil
}

// seedBinding writes one engineering value into the slave's store through
// the binding — the same codec the driver will read it back with.
func seedBinding(srv *slave.Server, src modbus.Source, t modbus.TagBinding, v any) error {
	u := srv.Unit(src.UnitID)
	switch t.Table {
	case modbus.TableCoil:
		u.SetCoil(t.Address, truthy(v))
		return nil
	case modbus.TableDiscrete:
		u.SetDiscrete(t.Address, truthy(v))
		return nil
	}
	format := t.Format
	if format == "" {
		format = "uint16"
	}
	f, err := modbus.ParseFormat(format)
	if err != nil {
		return err
	}
	if f.Kind == "bit" {
		// Encode(true) carries exactly the mask for this bit in this byte
		// order; merge it so the register's other bits survive.
		mask, err := modbus.Encode(f, true, src.WordOrder, src.ByteOrder, 0, 0)
		if err != nil {
			return err
		}
		cur := u.Holding(t.Address)
		if t.Table == modbus.TableInput {
			cur = u.Input(t.Address)
		}
		if truthy(v) {
			cur |= mask[0]
		} else {
			cur &^= mask[0]
		}
		if t.Table == modbus.TableInput {
			u.SetInput(t.Address, cur)
		} else {
			u.SetHolding(t.Address, cur)
		}
		return nil
	}
	regs, err := modbus.Encode(f, v, src.WordOrder, src.ByteOrder, t.Scale, t.Offset)
	if err != nil {
		return err
	}
	for i, r := range regs {
		if t.Table == modbus.TableInput {
			u.SetInput(t.Address+uint16(i), r)
		} else {
			u.SetHolding(t.Address+uint16(i), r)
		}
	}
	return nil
}

// truthy is the JSON-value face of a bool seed: true, or any nonzero number.
func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case float64:
		return x != 0
	}
	return false
}
