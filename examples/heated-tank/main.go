// Command heated-tank is a complete nautilus controller in ~30 lines: an
// IEC 61131-3 program on a 10 Hz scan loop, driving a simulated field device.
// This is what "SCADA as a library" looks like — no vendor runtime, no IDE,
// just Go you compile, test, and deploy.
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
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/server"
)

//go:embed program.st
var program string

func main() {
	rt, err := runtime.New(runtime.Options{
		Program: program,
		Driver:  NewPlant(),
		Scan:    100 * time.Millisecond, // 10 Hz
		Inputs:  []string{"LevelPct", "TempC"},
		Outputs: []string{"PumpRun", "Heater"},
		DtTag:   "ScanDtS",
		Seed: nio.Values{
			"TempSP": 65.0, "Kp": 12.0, "Ki": 0.15,
			"PumpStartLevel": 40.0, "PumpStopLevel": 75.0,
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "compile:", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go rt.Run(ctx)

	// Tag API: feeds the HMI kit and the VS Code extension's inline live
	// values (GET /api/state, GET /api/stream, POST /api/tags). Bind the
	// port up front so the banner reports the API honestly — a port
	// conflict shouldn't stop the controller, but it also shouldn't be
	// announced as running.
	srv := server.New(rt)
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

	banner := "nautilus · heated-tank — Ctrl+C to stop"
	if apiUp {
		banner = "nautilus · heated-tank — tag API on http://" + apiAddr + " — Ctrl+C to stop"
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
