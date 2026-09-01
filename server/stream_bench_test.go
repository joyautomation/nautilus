package server

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// The measurement this whole feature was proposed on. The Pomona WRD demo's
// central host carries 10,000-odd tags: /api/state is 571 KB and one SSE
// client pulls ~2 MB per ten seconds. These benchmarks answer, in bytes,
// what a delta stream costs instead — and the answer has to be reported as
// a RATIO, because the absolute number is a property of somebody's tag
// naming, not of the protocol.
//
//	go test ./server/ -run XXX -bench FrameBytes -benchtime 1x -v

const benchTags = 10000

// benchStore builds a store of n scalar tags with names shaped like a real
// plant's (site_area_device_point), because JSON key length is a real and
// non-trivial share of a frame.
func benchStore(n int) *runtime.Tags {
	tags := runtime.NewTags()
	for i := 0; i < n; i++ {
		tags.SetReal(benchName(i), float64(i)*1.5)
	}
	return tags
}

// benchName is one plant-shaped tag name (site_area_device_point), UNIQUE
// per index. JSON key length is a real share of a frame — the WRD host's
// names average ~28 characters — so a benchmark on "t0".."t9999" would
// flatter the full frame and understate what a delta saves.
func benchName(i int) string {
	pts := [...]string{"FIT", "LIT", "PIT", "TIT", "ZSC", "HS", "PMP", "VLV"}
	return fmt.Sprintf("RTU%02d_WEL%02d_%s_%03d", i/500, (i/8)%50, pts[i%8], i%500)
}

// churn moves pct% of the tags to new values, the way a scan would.
func churn(tags *runtime.Tags, names []string, pct int, round int) {
	step := 100 / pct
	for i := 0; i < len(names); i += step {
		tags.SetReal(names[i], float64(round*7+i)*1.5)
	}
}

// BenchmarkFrameBytes reports the bytes on the wire per broadcast tick for
// a full frame versus a delta frame at 5% churn, and the ratio between
// them. It is a measurement, not a performance gate: -benchtime 1x is
// enough, and the numbers that matter are the b.ReportMetric lines.
func BenchmarkFrameBytes(b *testing.B) {
	for _, pct := range []int{1, 5, 20} {
		b.Run(fmt.Sprintf("churn%d%%", pct), func(b *testing.B) {
			tags := benchStore(benchTags)
			names := tags.AppendNames(nil)

			// Every frame — delta or full — carries the scan diagnostics
			// whole, and they are not free: 180 scan times, 180 periods and
			// a histogram are a fixed floor of several kilobytes that a
			// delta frame cannot amortise. Measuring without them would
			// overstate the saving on exactly the frames that matter.
			stats := benchStats()

			var fullBytes, deltaBytes, ticks int
			var chBuf []runtime.Change
			_, gen := tags.ChangedSince(0, nil)

			if len(names) != benchTags {
				b.Fatalf("store has %d tags, want %d — the name generator collides",
					len(names), benchTags)
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for round := 1; round <= 40; round++ {
					churn(tags, names, pct, round)

					// Full: the whole store, rendered and encoded.
					full := map[string]any{}
					for _, n := range names {
						v, _ := tags.ReadGlobal(n)
						full[n] = runtime.Plain(v)
					}
					fb, _ := json.Marshal(Frame{Tags: full, Scan: &stats})

					// Delta: only what moved since the last tick.
					chBuf, gen = tags.ChangedSince(gen, chBuf[:0])
					d := make(map[string]any, len(chBuf))
					for j := range chBuf {
						d[chBuf[j].Name] = runtime.Plain(chBuf[j].Value)
					}
					db, _ := json.Marshal(Frame{Tags: d, Seq: uint64(round), Scan: &stats})

					fullBytes += len(fb)
					deltaBytes += len(db)
					ticks++
				}
			}
			b.StopTimer()
			if ticks == 0 || deltaBytes == 0 {
				b.Fatal("nothing measured")
			}
			b.ReportMetric(float64(fullBytes)/float64(ticks), "full-B/frame")
			b.ReportMetric(float64(deltaBytes)/float64(ticks), "delta-B/frame")
			b.ReportMetric(float64(fullBytes)/float64(deltaBytes), "×smaller")
		})
	}
}

// benchStats is a fully-populated ScanStats — the diagnostics block a real
// controller puts on every frame once its history rings have filled.
func benchStats() runtime.ScanStats {
	st := runtime.ScanStats{
		Count: 918273, TargetMs: 100, LastMs: 3.14159, MinMs: 1.5,
		MaxMs: 41.9, AvgMs: 3.2, ReadMs: 1.9, ExecUs: 210.5, WriteMs: 0.8,
		PeriodMs: 100.4, JitterMs: 0.41, IOHealthy: true,
		Histogram: make([]int, 15),
	}
	for i := 0; i < 180; i++ {
		st.Recent = append(st.Recent, 2.5+float64(i%13)*0.137)
		st.Periods = append(st.Periods, 99.5+float64(i%7)*0.211)
	}
	for i := range st.Histogram {
		st.Histogram[i] = 1000 - i*13
	}
	return st
}

// BenchmarkBroadcast is the server-side cost of one tick with many delta
// clients connected — the thing that has to stay flat as tablets are added,
// since the per-tick sweep and render are shared and only the per-client
// JSON is not.
func BenchmarkBroadcast(b *testing.B) {
	for _, clients := range []int{1, 10, 50} {
		b.Run(fmt.Sprintf("delta/%dclients", clients), func(b *testing.B) {
			benchBroadcast(b, clients, true)
		})
		b.Run(fmt.Sprintf("full/%dclients", clients), func(b *testing.B) {
			benchBroadcast(b, clients, false)
		})
	}
}

func benchBroadcast(b *testing.B, nClients int, delta bool) {
	drv := nio.NewMemory()
	rt, err := runtime.New(runtime.Options{
		Program: testProgram,
		Driver:  drv,
		Seed:    nio.Values{"Level": 40.0, "SP": 65.0, "Out": 0.0},
	})
	if err != nil {
		b.Fatal(err)
	}
	tags := rt.Tags()
	for i := 0; i < benchTags; i++ {
		tags.SetReal(benchName(i), float64(i))
	}
	names := tags.AppendNames(nil)

	srv := New(rt)
	for i := 0; i < nClients; i++ {
		// A generous buffer: this measures frame construction, not the
		// drop path.
		c := &client{ch: make(chan []byte, 1<<20), delta: delta}
		c.lastGen, c.lastKeyGen, c.seq = tags.Generation(), tags.NameGeneration(), 1
		c.lastFull = time.Now()
		srv.clients[c] = struct{}{}
		// Drain, so the send never blocks the loop under measurement.
		go func() {
			for range c.ch {
			}
		}()
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		churn(tags, names, 5, i+1)
		srv.broadcast()
	}
}

// ── the frame floor ───────────────────────────────────────────────────────
//
// The measurement that produced the non-tag block gate. On the Pomona WRD
// host every frame carried ~17.9 kB that had nothing to do with tags — a
// 55-device driver status, the scan diagnostics, the alarm counts — so a
// client that had filtered its subscription down to NOTHING still pulled
// 4.35 MB a minute. Tag filtering and tag deltas could not touch it.
//
//	go test ./server/ -run XXX -bench FrameFloor -benchtime 1x -v

// benchDrivers is a Pomona-shaped driver status: one Sparkplug host in
// front of 55 edge nodes, with the per-node roster in Extra that made the
// block 13 kB. The counters climb on every call, the way a live host's do.
func benchDrivers(round *int, flip *bool) func() []DriverStatus {
	const nodes = 55
	return func() []DriverStatus {
		*round++
		nodeList := make([]any, 0, nodes)
		devs := make([]DriverDevice, 0, nodes)
		for i := 0; i < nodes; i++ {
			online := true
			// One node flapping is a real change — the kind the gate must
			// let through — and it is the only one in this fixture.
			if i == 7 && *flip {
				online = false
			}
			id := fmt.Sprintf("RTU%02d_WEL%02d", i/8, i)
			detail := "offline"
			if online {
				detail = fmt.Sprintf("%d tags", 120+i)
			}
			devs = append(devs, DriverDevice{ID: id, Online: online, Detail: detail})
			nodeList = append(nodeList, map[string]any{
				"id": id, "group": "WRD", "edgeNode": id, "online": online,
				"state": map[bool]string{true: "online", false: "offline"}[online],
				"tags": float64(120 + i), "msgs": float64(*round*3 + i),
				"lastMs": float64(1_700_000_000_000 + *round*250), "births": float64(i%3 + 1),
				"lastBirthMs": float64(1_700_000_000_000), "bdSeq": float64(i % 7),
			})
		}
		return []DriverStatus{{
			Kind: "sparkplug", Name: "WRD/Host", Detail: "tcp://broker.wrd:1883",
			State: "connected", Message: "Publishing · bdSeq 4", SinceMs: 1_700_000_000_000,
			Metrics: []DriverMetric{
				{Label: "messages", Value: float64(*round * 17), Volatile: true},
				{Label: "bdSeq", Value: 4},
				{Label: "seq", Value: float64(*round % 256), Volatile: true},
				{Label: "last publish", AtMs: 1_700_000_000_000 + int64(*round)*250, Text: "0.2s"},
			},
			Devices:       devs,
			Extra: map[string]any{"born": true, "primaryHost": "SCADA", "nodes": nodeList},
			// The per-node roster stays watched — a site going offline is
			// the change this block exists to carry — and only the two
			// fields that step on every message are excluded. That is the
			// shape the live host runs; see TestHashDriversIgnoresNestedChurn.
			VolatileExtra: []string{"nodes.*.msgs", "nodes.*.lastMs"},
		}}
	}
}

// BenchmarkFrameFloor reports what one minute of stream costs a client
// subscribed to no tags at all — before the gate (every block on every
// frame, which is what this server used to send) and after it. Sixty
// seconds of a 250 ms stream is 240 ticks, with 5% of the store churning
// each tick, a resync every 30 s, and one edge node dropping offline
// mid-minute so the driver block has a real change to carry.
func BenchmarkFrameFloor(b *testing.B) {
	const tick = 250 * time.Millisecond
	const ticks = 240 // 60 s

	drv := nio.NewMemory()
	rt, err := runtime.New(runtime.Options{
		Program: testProgram, Driver: drv,
		Seed: nio.Values{"Level": 40.0, "SP": 65.0, "Out": 0.0},
	})
	if err != nil {
		b.Fatal(err)
	}
	// Fill the scan history rings: an idle runtime's diagnostics block is a
	// few hundred bytes, a running one's is ~5 kB, and it is the running
	// one this measurement is about.
	for i := 0; i < 200; i++ {
		rt.Scan()
	}
	tags := rt.Tags()
	for i := 0; i < benchTags; i++ {
		tags.SetReal(benchName(i), float64(i))
	}
	names := tags.AppendNames(nil)

	round, flip := 0, false
	cond := map[string]any{"HiTempAlm": false}
	eng, err := alarm.New(alarm.Options{
		Defs: []alarm.Def{{ID: "HiTemp", Tag: "HiTempAlm", Name: "High temperature",
			Priority: alarm.High, Site: "Plant", AckRequired: true, AutoClear: true}},
		Read: func(p string) (any, bool) { v, ok := cond[p]; return v, ok },
	})
	if err != nil {
		b.Fatal(err)
	}
	defer eng.Close()
	eng.Evaluate()

	srv := New(rt, Options{Drivers: benchDrivers(&round, &flip), Alarms: eng})

	// A client that asked for nothing: the pattern matches no tag name, so
	// every byte it receives is the floor under measurement.
	c := &client{ch: make(chan []byte, 4), delta: true, blocks: true, pats: []string{"__no_tags__"}}
	c.lastGen, c.lastKeyGen = tags.Generation(), tags.NameGeneration()
	srv.mu.Lock()
	srv.clients[c] = struct{}{}
	srv.mu.Unlock()

	var after, before int
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		after, before = 0, 0
		for t := 1; t <= ticks; t++ {
			churn(tags, names, 5, t)
			if t == 120 {
				flip = true // one node drops, halfway through the minute
			}
			// The cadences read a real clock and this loop runs a minute of
			// stream in milliseconds, so age the two deadlines by one tick
			// each time round — exactly what wall time would have done.
			srv.scanAt = srv.scanAt.Add(-tick)
			c.mu.Lock()
			c.lastFull = c.lastFull.Add(-tick)
			c.mu.Unlock()

			srv.broadcast()
			raw := <-c.ch
			after += len(raw)

			// The same frame as this server used to build it: every non-tag
			// block, every tick.
			var f Frame
			if err := json.Unmarshal(raw, &f); err != nil {
				b.Fatal(err)
			}
			stats := rt.Stats()
			f.Scan = &stats
			f.Drivers = srv.driverStatus(time.Now())
			f.Alarms = srv.alarmSummary()
			legacy, err := json.Marshal(f)
			if err != nil {
				b.Fatal(err)
			}
			before += len(legacy)
		}
	}
	b.StopTimer()
	if after == 0 {
		b.Fatal("nothing measured")
	}
	const mb = 1024 * 1024
	b.ReportMetric(float64(before)/mb, "before-MB/min")
	b.ReportMetric(float64(after)/mb, "after-MB/min")
	b.ReportMetric(float64(before)/float64(after), "×smaller")
}
