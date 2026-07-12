// Command heated-tank-fbd is the heated surge tank controller with its logic
// written as an IEC 61131-3 Function Block Diagram (.fbd) instead of ST. The
// netlist transpiles through lang/fbd at startup and runs on the same runtime
// — one compiler, two source languages.
//
// It exists to exercise the whole FBD toolchain against a live controller:
// open program.fbd in VS Code for syntax highlighting, diagnostics as you
// type, the live diagram preview, the visual diff vs git HEAD, and inline
// live tag values streaming from this process.
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

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/fbd"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/server"
)

//go:embed program.fbd
var program string

func main() {
	// FBD compiles by transpiling to ST — the runtime never knows the source
	// was a diagram. A syntax error reports the .fbd line.
	stProgram, err := fbd.Transpile(program)
	if err != nil {
		fmt.Fprintln(os.Stderr, "transpile:", err)
		os.Exit(1)
	}

	rt, err := runtime.New(runtime.Options{
		Program: stProgram,
		Driver:  NewPlant(),
		Scan:    100 * time.Millisecond, // 10 Hz
		Inputs:  []string{"LevelPct", "TempC"},
		Outputs: []string{"PumpRun", "Heater"},
		DtTag:   "ScanDtS",
		Seed: nio.Values{
			"TempSP": 65.0, "Kp": 12.0, "Ki": 0.15,
			"PumpStartLevel": 40.0, "PumpStopLevel": 75.0,
			// The seal-in latch reads PumpRun before the first write — seed
			// its initial state so the tag exists on scan one.
			"PumpRun": false,
		},
		// HMI documentation: served at GET /api/meta, shown in the built-in
		// dashboard's live tag table (description / unit / quality columns).
		Meta: map[string]runtime.TagMeta{
			"LevelPct":       {Desc: "Tank level", Unit: "%"},
			"TempC":          {Desc: "Tank temperature", Unit: "°C"},
			"ScanDtS":        {Desc: "Measured scan interval", Unit: "s"},
			"TempSP":         {Desc: "Temperature setpoint", Unit: "°C"},
			"Kp":             {Desc: "PI proportional gain"},
			"Ki":             {Desc: "PI integral gain", Unit: "1/s"},
			"PumpStartLevel": {Desc: "Pump seal-in level", Unit: "%"},
			"PumpStopLevel":  {Desc: "Pump drop-out level", Unit: "%"},
			"PumpRun":        {Desc: "Pump run command"},
			"Heater":         {Desc: "Heater output command", Unit: "%"},
			"TempLowAlm":     {Desc: "Low temperature alarm (10 s delay)"},
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "compile:", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go rt.Run(ctx)

	// Tag API for the HMI kit and the VS Code extension's inline live values.
	// NAUTILUS_API overrides the bind address (e.g. localhost:8081 when
	// another controller already owns 8080 — match nautilus.runtimeUrl).
	srv := server.New(rt, server.Options{AuthToken: os.Getenv("NAUTILUS_TOKEN")})
	go srv.Run(ctx)
	apiAddr := os.Getenv("NAUTILUS_API")
	if apiAddr == "" {
		apiAddr = "localhost:8080"
	}
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

	banner := "nautilus · heated-tank (FBD) — Ctrl+C to stop"
	if apiUp {
		banner = "nautilus · heated-tank (FBD) — tag API on http://" + apiAddr + " — Ctrl+C to stop"
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
			fmt.Printf("level %5.1f%%  temp %5.1f°C  pump %-3v  heater %3.0f%%  tempLowAlm %-3v  scans %d\n",
				t.Real("LevelPct"), t.Real("TempC"), onOff(t.Bool("PumpRun")),
				t.Real("Heater"), onOff(t.Bool("TempLowAlm")), rt.Stats().Count)
		}
	}
}

func onOff(b bool) string {
	if b {
		return "ON"
	}
	return "off"
}
