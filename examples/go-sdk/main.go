// Command go-sdk is nautilus's one Go-tier example: a custom io.Driver
// plant model embedded in your own Go program, instead of a `naut run`
// manifest. This is the advanced path — reach for it when your field
// device needs real code (a stateful protocol, a richer simulation, logic
// that doesn't fit a declarative manifest), not by default.
//
// If you don't need Go, use `naut new` — pick the manifest form and see
// examples/lift-station for the no-Go path: the same kind of project,
// authored as ST/FBD/LD/SFC and run with `naut run`, no Go toolchain
// required.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/server"
)

// Seam 1: the IEC 61131-3 source is embedded into the binary — one
// artifact to build, test, and deploy. `naut check` compiles it in CI;
// the VS Code extension edits it with diagnostics and live values.
//
//go:embed program.st
var program string

func main() {
	rt, err := runtime.New(runtime.Options{
		Program: program,
		// Seam 2: io.Driver is the field-I/O boundary. NewPlant is an
		// in-process simulation here; a real deployment hands this same
		// slot a Modbus/EtherNet-IP/OPC-UA driver — the program above
		// does not change either way.
		Driver: NewPlant(),
		Scan:   100 * time.Millisecond, // 10 Hz
		DtTag:  "ScanDtS",
		// One entry per tag: its ROLE in the scan data path (driver-fed
		// input, operator setpoint, logic-driven output), its seed, and
		// its HMI documentation, together.
		Tags: []runtime.TagDef{
			runtime.Input("LevelPct", runtime.Desc("Tank level"), runtime.Unit("%")),
			runtime.Input("TempC", runtime.Desc("Tank temperature"), runtime.Unit("°C")),
			runtime.Setpoint("TempSP", 65.0, runtime.Desc("Temperature setpoint"), runtime.Unit("°C")),
			runtime.Setpoint("Kp", 12.0, runtime.Desc("PI proportional gain")),
			runtime.Setpoint("Ki", 0.15, runtime.Desc("PI integral gain"), runtime.Unit("1/s")),
			runtime.Setpoint("PumpStartLevel", 40.0, runtime.Desc("Pump seal-in level"), runtime.Unit("%")),
			runtime.Setpoint("PumpStopLevel", 75.0, runtime.Desc("Pump drop-out level"), runtime.Unit("%")),
			// The seal-in latch READS PumpRun before its first write — Init
			// makes the tag exist on scan one instead of faulting.
			runtime.Output("PumpRun", runtime.Init(false), runtime.Desc("Pump run command")),
			runtime.Output("Heater", runtime.Desc("Heater output command"), runtime.Unit("%")),
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "compile:", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go rt.Run(ctx)

	// Seam 3: the tag API serves the HMI kit and the VS Code extension's
	// inline live values (GET /api/state, GET /api/stream, POST /api/tags)
	// — opening this project in the editor shows the tank moving exactly
	// as this terminal does. OnlineEdits is on: this is a dev playground,
	// so PLC-style "Download Program to Controller" (edit program.st in
	// VS Code, push it live, diff it, roll it back) works against this
	// process too, same as against a manifest project.
	//
	// Writes are same-origin-only out of the box; set NAUTILUS_TOKEN to
	// require a token on writes instead (progressive auth).
	srv := server.New(rt, server.Options{
		AuthToken:   os.Getenv("NAUTILUS_TOKEN"),
		OnlineEdits: true,
	})
	go srv.Run(ctx)
	const apiAddr = "localhost:8080"
	apiUp := false
	if ln, err := net.Listen("tcp", apiAddr); err != nil {
		fmt.Fprintf(os.Stderr, "tag api: %v (continuing without it)\n", err)
	} else {
		apiUp = true
		go func() {
			if err := http.Serve(ln, srv.Handler()); err != nil && ctx.Err() == nil {
				fmt.Fprintln(os.Stderr, "tag api:", err)
			}
		}()
	}

	// Seams 4+ (retain.Store, runtime.Coordinator, hist.Sink) aren't wired
	// up here — this example fits on one line each in README.md instead of
	// growing this program. Nothing about them requires Go beyond this
	// same shape: implement the interface, hand it to Options.

	banner := "nautilus · go-sdk (heated tank) — Ctrl+C to stop"
	if apiUp {
		banner = "nautilus · go-sdk (heated tank) — tag API on http://" + apiAddr + " — Ctrl+C to stop"
	}
	fmt.Println(banner)
	t := rt.Tags()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			fmt.Println("\nstopped.")
			return
		case <-tick.C:
			fmt.Printf("level %5.1f%%  temp %5.1f°C  pump %-3v  heater %3.0f%%  scans %d\n",
				t.Real("LevelPct"), t.Real("TempC"), onOff(t.Bool("PumpRun")),
				t.Real("Heater"), rt.Stats().Count)
		}
	}
}

func onOff(b bool) string {
	if b {
		return "ON"
	}
	return "off"
}
