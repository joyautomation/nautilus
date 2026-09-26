package main

// `naut logix emulate` stands a ControlLogix up on the bench: the in-repo
// emulator (eip/logixserver) serving a tag surface derived from an L5X
// export, or from the surface JSON the library already reads. It is to
// EtherNet/IP what `naut modbus serve` is to Modbus: browse, import, poll
// and write all work with no PLC on the network.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/joyautomation/nautilus/eip/cip"
	"github.com/joyautomation/nautilus/eip/logixserver"
	"github.com/joyautomation/nautilus/lang/l5x"
)

const logixEmulateUsage = `usage: naut logix emulate (--l5x <file.L5X> | --surface <surface.json>) [flags]

Serve a tag surface as an Allen-Bradley ControlLogix over EtherNet/IP, so
naut eip browse|import and a driver: {type: eip} project run with no PLC.

  --l5x       Derive the surface from a Logix Designer export: every
              DataType and AOI becomes a template, every controller tag a
              symbol, every program tag a symbol read as
              Program:<prog>.<tag>. Initial values come from the export's
              decorated <Data>. Aliases, multi-dimensional arrays and types
              with no public shape (MESSAGE, AXIS_*) are left out, and said so.
  --surface   The tag-surface JSON logixserver.LoadTagSurface reads:
                {"controllerName": "Line1",
                 "templates": [{"name": "Motor", "members": [
                     {"name": "Speed", "datatype": "REAL"},
                     {"name": "Hist", "datatype": "REAL", "dimension": 10}]}],
                 "symbols": [{"name": "M1", "datatype": "Motor"},
                             {"name": "Program:Main", "program": true},
                             {"name": "Count", "scope": "Program:Main", "datatype": "DINT"}],
                 "tags": [{"path": "M1.Speed", "datatype": "REAL"},
                          {"path": "Program:Main.Count", "datatype": "DINT"}, ...]}
              tags lists every elementary leaf a client can read; see the
              TagSurfaceSpec Go doc in eip/logixserver.
  --listen    Listen address (default 127.0.0.1:44818; port 0 picks one)
  --values    JSON {"path": value} seeds applied after load, paths as the
              client reads them ("Line1_PIT_001.VALUE",
              "Program:MainProgram.LevelPct"). A value may be an object
              (members), an array (elements) or a string (a STRING tag).
  --ramp      Drift every numeric leaf slowly so live values visibly move.
              A leaf a client writes stops drifting and keeps the write.
  --name      Controller name the emulator reports (default: the export's
              controller name, or the surface's controllerName)

Client writes land in the emulator's tag store, so a project's outputs read
back on the next poll. Ctrl-C stops it.
`

func runLogixEmulate(args []string) int {
	fs := flag.NewFlagSet("logix emulate", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, logixEmulateUsage) }
	l5xPath := fs.String("l5x", "", "derive the tag surface from this L5X export")
	surfacePath := fs.String("surface", "", "tag-surface JSON file")
	listen := fs.String("listen", "127.0.0.1:44818", "listen address")
	valuesPath := fs.String("values", "", "JSON file of {path: value} seeds")
	ramp := fs.Bool("ramp", false, "drift numeric leaves slowly")
	name := fs.String("name", "", "controller name to report")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "naut logix emulate: unexpected argument %q (the export goes in --l5x)\n", fs.Arg(0))
		return 2
	}
	if (*l5xPath == "") == (*surfacePath == "") {
		fmt.Fprintln(os.Stderr, "naut logix emulate: exactly one of --l5x or --surface is required")
		return 2
	}
	if _, _, err := net.SplitHostPort(*listen); err != nil {
		fmt.Fprintf(os.Stderr, "naut logix emulate: --listen %q: %v\n", *listen, err)
		return 2
	}

	surf, err := loadLogixSurface(*l5xPath, *surfacePath, *name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut logix emulate:", err)
		return 1
	}
	var values map[string]any
	if *valuesPath != "" {
		raw, err := os.ReadFile(*valuesPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "naut logix emulate:", err)
			return 1
		}
		if err := json.Unmarshal(raw, &values); err != nil {
			fmt.Fprintf(os.Stderr, "naut logix emulate: %s: %v\n", *valuesPath, err)
			return 1
		}
	}
	em, err := startLogixEmulate(surf, *listen, values, *ramp, os.Stdout, os.Stderr)
	if err != nil {
		if errors.Is(err, errLogixValues) {
			fmt.Fprintf(os.Stderr, "naut logix emulate: --values %s: %s\n",
				*valuesPath, strings.TrimPrefix(err.Error(), errLogixValues.Error()+": "))
		} else {
			fmt.Fprintln(os.Stderr, "naut logix emulate:", err)
		}
		return 1
	}
	defer em.Stop()
	fmt.Println("ctrl-c to stop")
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	<-ch
	return 0
}

// logixSurface is a loaded tag surface, whichever file it came from.
type logixSurface struct {
	spec    *logixserver.TagSurfaceSpec
	values  map[string]any // initial values (L5X only), seeded leniently
	skipped []string
	source  string
}

// loadLogixSurface reads the surface from an L5X export or a surface JSON.
// Errors name the file.
func loadLogixSurface(l5xPath, surfacePath, name string) (*logixSurface, error) {
	if l5xPath != "" {
		f, err := l5x.ParseFile(l5xPath) // its errors name the file
		if err != nil {
			return nil, err
		}
		s, err := logixserver.SurfaceFromL5X(f, name)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", l5xPath, err)
		}
		return &logixSurface{spec: s.Spec, values: s.Values, skipped: s.Skipped, source: l5xPath}, nil
	}
	raw, err := os.ReadFile(surfacePath)
	if err != nil {
		return nil, err
	}
	var spec logixserver.TagSurfaceSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, fmt.Errorf("%s: %w", surfacePath, err)
	}
	if name != "" {
		spec.ControllerName = name
	}
	return &logixSurface{spec: &spec, source: surfacePath}, nil
}

var errLogixValues = errors.New("--values")

// logixEmulator is a running emulator: its address, store, and stop.
type logixEmulator struct {
	Addr  string
	Store *logixserver.TagStore
	Name  string
	stop  func()
}

func (e *logixEmulator) Stop() { e.stop() }

// startLogixEmulate compiles the surface, seeds it, and serves it until
// Stop — no signal handling, so tests drive it in-process the way they
// drive startModbusServe. Port 0 in listen picks a free port; Addr says
// which.
func startLogixEmulate(surf *logixSurface, listen string, values map[string]any, ramp bool, out, warn io.Writer) (*logixEmulator, error) {
	schema, tags, name, err := logixserver.CompileSurface(surf.spec)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", surf.source, err)
	}
	if name == "" {
		name = logixserver.DefaultControllerName
	}
	store := logixserver.NewTagStore()
	for _, t := range tags {
		store.Set(t.Path, t.LeafType, t.Default)
	}
	for _, p := range sortedKeys(surf.values) {
		_ = logixserver.SeedValue(store, p, surf.values[p], true)
	}
	for _, p := range sortedKeys(values) {
		if err := logixserver.SeedValue(store, p, values[p], false); err != nil {
			return nil, fmt.Errorf("%w: %v", errLogixValues, err)
		}
	}

	// The CIP server binds TCP and UDP to the same address and does not
	// report what port 0 chose, so choose it here.
	if _, port, _ := net.SplitHostPort(listen); port == "0" {
		ln, err := net.Listen("tcp", listen)
		if err != nil {
			return nil, err
		}
		listen = ln.Addr().String()
		_ = ln.Close()
	}

	log := slog.New(slog.NewTextHandler(warn, &slog.HandlerOptions{Level: slog.LevelWarn}))
	srv := logixserver.NewServer(store, schema, name, listen, log)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	if err := waitListening(listen, done); err != nil {
		cancel()
		return nil, fmt.Errorf("listen %s: %w", listen, err)
	}

	stopRamp := func() {}
	if ramp {
		stopRamp = startLogixRamp(store, tags)
	}

	for _, s := range surf.skipped {
		fmt.Fprintln(warn, "  skipped:", s)
	}
	symbols, programs := 0, 0
	for _, s := range surf.spec.Symbols {
		if s.Program {
			programs++
		} else {
			symbols++
		}
	}
	fmt.Fprintf(out, "emulating ControlLogix %q on %s — %s, %s, %s, %s\n",
		name, listen, plural(symbols, "tag"), plural(programs, "program"),
		plural(len(surf.spec.Templates), "template"), plural(len(tags), "leaf"))
	return &logixEmulator{
		Addr:  listen,
		Store: store,
		Name:  name,
		stop: func() {
			stopRamp()
			cancel()
			<-done
		},
	}, nil
}

// waitListening returns once addr accepts TCP, or with the server's own
// error if Run failed first (address in use, say).
func waitListening(addr string, done <-chan error) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case err := <-done:
			if err == nil {
				err = errors.New("server stopped")
			}
			return err
		default:
		}
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not accepting connections: %w", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// startLogixRamp drifts every numeric leaf around its seeded value, the way
// `naut modbus serve --ramp` does. A leaf whose value is no longer what the
// ramp last wrote has been written by a client: it is left alone from then
// on, so setpoints and handshakes a project writes stick.
func startLogixRamp(store *logixserver.TagStore, tags []logixserver.TagConfig) func() {
	type leaf struct {
		path  string
		code  uint16
		base  float64
		last  float64
		taken bool
	}
	var leaves []*leaf
	for _, t := range tags {
		if t.LeafType == cip.TypeBOOL || stringLeaf(store, t.Path) {
			continue
		}
		_, v, _ := store.Resolve(t.Path)
		f, _ := v.(float64)
		leaves = append(leaves, &leaf{path: t.Path, code: t.LeafType, base: f, last: f})
	}
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
			for i, l := range leaves {
				if l.taken {
					continue
				}
				amp := math.Max(10, math.Abs(l.base)*0.05)
				// Offset each leaf's phase so a screen of them does not
				// move in lockstep; the drift is non-negative so unsigned
				// types never wrap.
				v := l.base + amp*(0.5+0.5*math.Sin(phase+float64(i)*0.7))
				if l.code != cip.TypeREAL && l.code != cip.TypeLREAL {
					v = math.Round(v)
				}
				if !store.CompareAndSwap(l.path, l.last, v) {
					l.taken = true // a client wrote it
					continue
				}
				l.last = v
			}
		}
	}()
	return func() { tick.Stop(); close(done) }
}

// stringLeaf reports whether path is the LEN or a DATA[i] byte of a STRING
// — drifting those would scramble the text.
func stringLeaf(store *logixserver.TagStore, path string) bool {
	if base, ok := strings.CutSuffix(path, ".LEN"); ok {
		_, _, has := store.Resolve(base + ".DATA[0]")
		return has
	}
	if i := strings.LastIndex(path, ".DATA["); i >= 0 {
		_, _, has := store.Resolve(path[:i] + ".LEN")
		return has
	}
	return false
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	if noun == "leaf" {
		return fmt.Sprintf("%d leaves", n)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
