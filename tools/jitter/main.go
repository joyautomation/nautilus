// jitter is the scan-loop lateness harness: it runs a small, deliberately
// mixed resource — a fast main task that owns loopback I/O, a second fast
// task, a slow task, an allocation-heavy task — on the real runtime.Run
// tickers for a fixed time, then reports every task's wake-up lateness
// (late count, overruns, p50/p99/p99.9/max, histogram) and the Go GC's
// stop-the-world pauses, alongside the machine it ran on.
//
// Every number in docs/design/realtime.md comes from this program. Run it
// the same way on the same box before and after a change, and the two
// reports are the before/after.
//
//	go run ./tools/jitter -duration 5m -out /tmp/jitter-idle
//	go run ./tools/jitter -duration 5m -slow 0 -alloc 0   # fast tasks alone
//
// Stdlib only, like the runtime it measures.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	goruntime "runtime"
	"runtime/debug"
	"runtime/metrics"
	"strconv"
	"strings"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// The fast main task: a PI loop on loopback I/O so the read → execute →
// write path runs every scan with outputs that actually change.
const fastST = `PROGRAM Fast
VAR_EXTERNAL
    PV      : REAL;
    SP      : REAL;
    Kp      : REAL;
    Ki      : REAL;
    DtS     : REAL;
    CV      : REAL;
    Phase   : REAL;
    Cycles  : INT;
    In1 : REAL; In2 : REAL; In3 : REAL; In4 : REAL;
    Out1 : REAL; Out2 : REAL; Out3 : REAL;
END_VAR
VAR
    integral : REAL;
    err      : REAL;
END_VAR
err := SP - PV;
integral := LIMIT(-100.0, integral + Ki * err * DtS, 100.0);
CV := LIMIT(0.0, Kp * err + integral, 100.0);
Phase := Phase + DtS;
IF Phase > 6.283185 THEN Phase := Phase - 6.283185; END_IF;
Out1 := SIN(Phase) * In1;
Out2 := COS(Phase) * In2 + In3;
Out3 := CV + In4;
Cycles := Cycles + 1;
END_PROGRAM`

// The second fast task: the same shape, no I/O (tasks compute on the store).
const fast2ST = `PROGRAM Fast2
VAR_EXTERNAL
    CV      : REAL;
    Dt2S    : REAL;
    Ramp    : REAL;
    Cycles2 : INT;
END_VAR
Ramp := Ramp + CV * Dt2S;
IF Ramp > 1000.0 THEN Ramp := 0.0; END_IF;
Cycles2 := Cycles2 + 1;
END_PROGRAM`

// The slow task: a FOR loop sized at startup to take about -slow-ms of
// VM time, so it holds the scan lock longer than the fast tasks' period.
const slowST = `PROGRAM Slow
VAR_EXTERNAL
    SlowIters : INT;
    SlowAcc   : REAL;
    CyclesS   : INT;
END_VAR
VAR
    i   : INT;
    acc : REAL;
END_VAR
acc := 0.0;
FOR i := 1 TO SlowIters DO
    acc := acc + SQRT(INT_TO_REAL(i)) * 1.0001;
END_FOR;
SlowAcc := acc;
CyclesS := CyclesS + 1;
END_PROGRAM`

// The allocation-heavy task: string building, which allocates on every
// iteration — garbage for the collector to find on the fast tasks' time.
const allocST = `PROGRAM Alloc
VAR_EXTERNAL
    AllocIters : INT;
    AllocLen   : INT;
    CyclesA    : INT;
END_VAR
VAR
    i : INT;
    s : STRING;
END_VAR
s := '';
FOR i := 1 TO AllocIters DO
    s := CONCAT(RIGHT(s, 48), INT_TO_STRING(i));
END_FOR;
AllocLen := LEN(s);
CyclesA := CyclesA + 1;
END_PROGRAM`

type config struct {
	Duration   time.Duration `json:"duration"`
	Fast       time.Duration `json:"fast"`
	Fast2      time.Duration `json:"fast2"`
	Slow       time.Duration `json:"slow"`
	SlowMs     float64       `json:"slowMs"`
	SlowIters  int64         `json:"slowIters"`
	Alloc      time.Duration `json:"alloc"`
	AllocIters int64         `json:"allocIters"`
	ChurnMBps  float64       `json:"churnMBps"`
	Threshold  time.Duration `json:"threshold"`
	Note       string        `json:"note,omitempty"`
	Listen     bool          `json:"listen"`
}

type env struct {
	Hostname   string `json:"hostname"`
	GOOS       string `json:"goos"`
	GOARCH     string `json:"goarch"`
	GoVersion  string `json:"goVersion"`
	NumCPU     int    `json:"numCPU"`
	GOMAXPROCS int    `json:"gomaxprocs"`
	GOGC       string `json:"gogc"`
	GOMEMLIMIT string `json:"gomemlimit,omitempty"`
	Kernel     string `json:"kernel"`
	PreemptRT  bool   `json:"preemptRT"`
	CPUModel   string `json:"cpuModel"`
	Governor   string `json:"governor,omitempty"`
	LoadStart  string `json:"loadStart,omitempty"`
	LoadEnd    string `json:"loadEnd,omitempty"`
}

type taskReport struct {
	Name     string  `json:"name"`
	TargetMs float64 `json:"targetMs"`
	Scans    uint64  `json:"scans"`
	Expected uint64  `json:"expected"` // duration / target
	LastMs   float64 `json:"lastExecMs"`
	MaxMs    float64 `json:"maxExecMs,omitempty"` // main task only
	runtime.Lateness
}

type gcReport struct {
	Collections uint64  `json:"collections"`
	TotalPause  float64 `json:"totalPauseMs"`
	P50Us       float64 `json:"pauseP50Us"`
	P99Us       float64 `json:"pauseP99Us"`
	MaxUs       float64 `json:"pauseMaxUs"`
	HeapMB      float64 `json:"heapMB"`
}

type report struct {
	Tool    string       `json:"tool"`
	Started time.Time    `json:"started"`
	Config  config       `json:"config"`
	Env     env          `json:"env"`
	Tasks   []taskReport `json:"tasks"`
	GC      gcReport     `json:"gc"`
}

func main() {
	var c config
	flag.DurationVar(&c.Duration, "duration", 2*time.Minute, "how long to run")
	flag.DurationVar(&c.Fast, "fast", time.Millisecond, "main (fast) task period; it owns the loopback I/O")
	flag.DurationVar(&c.Fast2, "fast2", 10*time.Millisecond, "second fast task period (0 = off)")
	flag.DurationVar(&c.Slow, "slow", 100*time.Millisecond, "slow task period (0 = off)")
	flag.Float64Var(&c.SlowMs, "slow-ms", 20, "slow task's execution time, calibrated at startup")
	flag.DurationVar(&c.Alloc, "alloc", 50*time.Millisecond, "allocation-heavy task period (0 = off)")
	flag.Int64Var(&c.AllocIters, "alloc-iters", 200, "string concatenations per alloc-task scan")
	flag.Float64Var(&c.ChurnMBps, "churn-mb", 0, "extra Go-side allocation churn, MB/s (0 = off)")
	flag.DurationVar(&c.Threshold, "threshold", 0, "late threshold (0 = a tenth of each task's period)")
	flag.StringVar(&c.Note, "note", "", "free text recorded in the report (what the box was doing)")
	out := flag.String("out", "", "directory for report.json and report.md (default: stdout only)")
	listen := flag.Bool("listen", true, "hold a loopback TCP listener open, so the Go poller is live as it is under naut run")
	flag.Parse()

	if *listen {
		// naut run always has the tag API listening, which puts the Go
		// runtime's network poller in charge of idle waits. Hold a socket
		// so the harness idles the same way a controller does.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			fmt.Fprintln(os.Stderr, "jitter:", err)
			os.Exit(1)
		}
		defer ln.Close()
	}

	c.Listen = *listen
	rep := report{Tool: "nautilus tools/jitter", Started: time.Now(), Config: c, Env: readEnv()}

	seed := nio.Values{
		"PV": 40.0, "SP": 50.0, "Kp": 2.0, "Ki": 0.5, "DtS": 0.001, "CV": 0.0, "Phase": 0.0, "Cycles": int64(0),
		"In1": 1.0, "In2": 2.0, "In3": 3.0, "In4": 4.0, "Out1": 0.0, "Out2": 0.0, "Out3": 0.0,
		"Dt2S": 0.01, "Ramp": 0.0, "Cycles2": int64(0),
		"SlowIters": int64(1000), "SlowAcc": 0.0, "CyclesS": int64(0),
		"AllocIters": int64(c.AllocIters), "AllocLen": int64(0), "CyclesA": int64(0),
	}
	drv := nio.NewMemory()
	_ = drv.WriteOutputs(nio.Values{"PV": 40.0, "In1": 1.0, "In2": 2.0, "In3": 3.0, "In4": 4.0})

	if c.Slow > 0 {
		c.SlowIters = calibrateSlow(c.SlowMs)
		rep.Config.SlowIters = c.SlowIters
		seed["SlowIters"] = int64(c.SlowIters)
	}

	var tasks []runtime.Task
	if c.Fast2 > 0 {
		tasks = append(tasks, runtime.Task{Name: "fast2", Program: fast2ST, Scan: c.Fast2, DtTag: "Dt2S"})
	}
	if c.Slow > 0 {
		tasks = append(tasks, runtime.Task{Name: "slow", Program: slowST, Scan: c.Slow})
	}
	if c.Alloc > 0 {
		tasks = append(tasks, runtime.Task{Name: "alloc", Program: allocST, Scan: c.Alloc})
	}
	rt, err := runtime.New(runtime.Options{
		Program:       fastST,
		Driver:        drv,
		Scan:          c.Fast,
		Inputs:        []string{"PV", "In1", "In2", "In3", "In4"},
		Outputs:       []string{"CV", "Out1", "Out2", "Out3"},
		Seed:          seed,
		DtTag:         "DtS",
		Tasks:         tasks,
		LateThreshold: c.Threshold,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "jitter:", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	ctx, cancelAfter := context.WithTimeout(ctx, c.Duration)
	defer cancelAfter()

	if c.ChurnMBps > 0 {
		go churn(ctx, c.ChurnMBps)
	}
	fmt.Fprintf(os.Stderr, "jitter: %s on %s (%s, %s), %d tasks, %v…\n",
		rep.Env.GoVersion, rep.Env.Hostname, rep.Env.CPUModel, rep.Env.Kernel, len(tasks)+1, c.Duration)
	start := time.Now()
	rt.Run(ctx)
	elapsed := time.Since(start)
	rep.Env.LoadEnd = readFirst("/proc/loadavg")

	// The main task's execution-time stats and every task's lateness.
	st := rt.Stats()
	rep.Tasks = append(rep.Tasks, taskReport{
		Name: "main", TargetMs: st.TargetMs, Scans: st.Count,
		Expected: uint64(elapsed / c.Fast), LastMs: st.LastMs, MaxMs: st.MaxMs, Lateness: st.Lateness,
	})
	for _, t := range st.Tasks {
		rep.Tasks = append(rep.Tasks, taskReport{
			Name: t.Name, TargetMs: t.TargetMs, Scans: t.Count,
			Expected: uint64(elapsed / (time.Duration(t.TargetMs * float64(time.Millisecond)))),
			LastMs:   t.LastMs, Lateness: t.Lateness,
		})
	}
	rep.GC = readGC()

	md := markdown(&rep, elapsed)
	fmt.Print(md)
	if *out != "" {
		if err := os.MkdirAll(*out, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "jitter:", err)
			os.Exit(1)
		}
		js, _ := json.MarshalIndent(rep, "", "  ")
		must(os.WriteFile(filepath.Join(*out, "report.json"), append(js, '\n'), 0o644))
		must(os.WriteFile(filepath.Join(*out, "report.md"), []byte(md), 0o644))
		fmt.Fprintf(os.Stderr, "jitter: wrote %s/report.{json,md}\n", *out)
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "jitter:", err)
		os.Exit(1)
	}
}

// calibrateSlow finds the FOR-loop count that makes one Slow scan take
// about targetMs on this machine: time a fixed count, scale linearly.
func calibrateSlow(targetMs float64) int64 {
	const probe = 20000
	r, err := runtime.New(runtime.Options{
		Program: slowST,
		Seed:    nio.Values{"SlowIters": int64(probe), "SlowAcc": 0.0, "CyclesS": int64(0)},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "jitter: calibrate:", err)
		os.Exit(1)
	}
	r.Scan() // warm
	best := math.MaxFloat64
	for i := 0; i < 5; i++ {
		t0 := time.Now()
		r.Scan()
		if d := time.Since(t0).Seconds() * 1000; d < best {
			best = d
		}
	}
	n := int64(float64(probe) * targetMs / best)
	fmt.Fprintf(os.Stderr, "jitter: slow task: %d iterations ≈ %.1f ms (%.0f iters/ms)\n", n, targetMs, probe/best)
	return n
}

// churn allocates and drops rate MB/s in 64 KiB chunks — background
// garbage from a process that is NOT the scan loop (a web server, a
// historian), so the GC has reasons to run that the logic did not give it.
func churn(ctx context.Context, rate float64) {
	const chunk = 64 << 10
	interval := time.Duration(float64(chunk) / (rate * 1e6) * float64(time.Second))
	t := time.NewTicker(interval)
	defer t.Stop()
	var keep [][]byte
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b := make([]byte, chunk)
			b[0] = 1
			keep = append(keep, b)
			if len(keep) > 64 {
				keep = keep[32:]
			}
		}
	}
}

func readEnv() env {
	host, _ := os.Hostname()
	e := env{
		Hostname: host, GOOS: goruntime.GOOS, GOARCH: goruntime.GOARCH,
		GoVersion: goruntime.Version(), NumCPU: goruntime.NumCPU(),
		GOMAXPROCS: goruntime.GOMAXPROCS(0),
		GOGC:       fmt.Sprint(debug.SetGCPercent(-1)),
		GOMEMLIMIT: os.Getenv("GOMEMLIMIT"),
		Kernel:     readFirst("/proc/sys/kernel/osrelease"),
		LoadStart:  readFirst("/proc/loadavg"),
		Governor:   readFirst("/sys/devices/system/cpu/cpu0/cpufreq/scaling_governor"),
	}
	debug.SetGCPercent(atoi(e.GOGC)) // SetGCPercent(-1) returned the previous value; put it back
	if _, err := os.Stat("/sys/kernel/realtime"); err == nil {
		e.PreemptRT = readFirst("/sys/kernel/realtime") == "1"
	}
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "model name") || strings.HasPrefix(line, "Model") {
				if _, v, ok := strings.Cut(line, ":"); ok {
					e.CPUModel = strings.TrimSpace(v)
					break
				}
			}
		}
	}
	if e.Kernel == "" {
		e.Kernel = goruntime.GOOS
	}
	return e
}

func atoi(s string) int {
	n := 0
	fmt.Sscan(s, &n)
	return n
}

func readFirst(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(b), "\n")
	return strings.TrimSpace(line)
}

// readGC reads the collector's stop-the-world pause distribution for the
// whole run from runtime/metrics (stdlib): pause count, total, p50/p99/max.
func readGC() gcReport {
	samples := []metrics.Sample{
		{Name: "/gc/pauses:seconds"},
		{Name: "/gc/cycles/total:gc-cycles"},
		{Name: "/memory/classes/heap/objects:bytes"},
	}
	metrics.Read(samples)
	var g gcReport
	if samples[1].Value.Kind() == metrics.KindUint64 {
		g.Collections = samples[1].Value.Uint64()
	}
	if samples[2].Value.Kind() == metrics.KindUint64 {
		g.HeapMB = float64(samples[2].Value.Uint64()) / 1e6
	}
	if samples[0].Value.Kind() != metrics.KindFloat64Histogram {
		return g
	}
	h := samples[0].Value.Float64Histogram()
	var n uint64
	for _, c := range h.Counts {
		n += c
	}
	if n == 0 {
		return g
	}
	k50, k99 := uint64(math.Ceil(0.5*float64(n))), uint64(math.Ceil(0.99*float64(n)))
	var cum uint64
	for i, c := range h.Counts {
		if c == 0 {
			continue
		}
		hi := h.Buckets[i+1]
		if math.IsInf(hi, 1) {
			hi = h.Buckets[i]
		}
		mid := (h.Buckets[i] + hi) / 2
		g.TotalPause += float64(c) * mid * 1000
		cum += c
		if g.P50Us == 0 && cum >= k50 {
			g.P50Us = hi * 1e6
		}
		if g.P99Us == 0 && cum >= k99 {
			g.P99Us = hi * 1e6
		}
		g.MaxUs = hi * 1e6
	}
	return g
}

func markdown(r *report, elapsed time.Duration) string {
	var b strings.Builder
	e := r.Env
	fmt.Fprintf(&b, "## Scan lateness — %s, %s\n\n", e.Hostname, r.Started.Format("2006-01-02 15:04"))
	rtk := ""
	if e.PreemptRT {
		rtk = " (PREEMPT_RT)"
	}
	fmt.Fprintf(&b, "%s · %s · kernel %s%s · %s · GOMAXPROCS=%d of %d · GOGC=%s", e.CPUModel, e.GOARCH, e.Kernel, rtk, e.GoVersion, e.GOMAXPROCS, e.NumCPU, e.GOGC)
	if e.GOMEMLIMIT != "" {
		fmt.Fprintf(&b, " · GOMEMLIMIT=%s", e.GOMEMLIMIT)
	}
	if e.Governor != "" {
		fmt.Fprintf(&b, " · governor %s", e.Governor)
	}
	fmt.Fprintf(&b, "\n\nRan %s. Load average %s → %s.", elapsed.Round(time.Second), e.LoadStart, e.LoadEnd)
	if r.Config.Note != "" {
		fmt.Fprintf(&b, " %s.", r.Config.Note)
	}
	c := r.Config
	fmt.Fprintf(&b, "\nTasks: main %v (loopback I/O)", c.Fast)
	if c.Fast2 > 0 {
		fmt.Fprintf(&b, ", fast2 %v", c.Fast2)
	}
	if c.Slow > 0 {
		fmt.Fprintf(&b, ", slow %v (≈%.0f ms of logic, %d iterations)", c.Slow, c.SlowMs, c.SlowIters)
	}
	if c.Alloc > 0 {
		fmt.Fprintf(&b, ", alloc %v (%d string concats)", c.Alloc, c.AllocIters)
	}
	if c.ChurnMBps > 0 {
		fmt.Fprintf(&b, "; Go-side churn %.0f MB/s", c.ChurnMBps)
	}
	b.WriteString(".\n\n")
	b.WriteString("| task | target | scans (ran / due) | late (> target + thr) | overruns | p50 | p99 | p99.9 | max |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, t := range r.Tasks {
		pct := 0.0
		if t.Scans > 0 {
			pct = 100 * float64(t.Late) / float64(t.Scans)
		}
		fmt.Fprintf(&b, "| %s | %s | %d / %d | %d (%.2f %%, thr %s) | %d | %s | %s | %s | %s |\n",
			t.Name, fmtMs(t.TargetMs), t.Scans, t.Expected, t.Late, pct, fmtMs(t.ThresholdMs), t.Overruns,
			fmtUs(t.P50Us), fmtUs(t.P99Us), fmtUs(t.P999Us), fmtUs(t.MaxUs))
	}
	fmt.Fprintf(&b, "\nGC: %d collections, %.1f ms total stop-the-world, pause p50 %s · p99 %s · max %s, heap %.1f MB.\n",
		r.GC.Collections, r.GC.TotalPause, fmtUs(r.GC.P50Us), fmtUs(r.GC.P99Us), fmtUs(r.GC.MaxUs), r.GC.HeapMB)
	b.WriteString("\nLateness histogram (scans per bucket, cumulative):\n\n")
	if len(r.Tasks) > 0 {
		edges := r.Tasks[0].BucketsUs
		b.WriteString("| bucket |")
		for _, t := range r.Tasks {
			fmt.Fprintf(&b, " %s |", t.Name)
		}
		b.WriteString("\n|---|")
		for range r.Tasks {
			b.WriteString("---:|")
		}
		b.WriteString("\n")
		for i := 0; i <= len(edges); i++ {
			label := ""
			switch {
			case i == 0:
				label = "< " + fmtUs(edges[0]) + " (incl. early)"
			case i == len(edges):
				label = "≥ " + fmtUs(edges[i-1])
			default:
				label = fmtUs(edges[i-1]) + " – " + fmtUs(edges[i])
			}
			fmt.Fprintf(&b, "| %s |", label)
			for _, t := range r.Tasks {
				fmt.Fprintf(&b, " %d |", t.Histogram[i])
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("\nLateness = (period − target) per scan, cumulative since start, cold start included. Percentiles are bucket upper edges (≤ 3.2 % above the true value). Overruns = scans whose execution exceeded the period.\n")
	return b.String()
}

func fmtMs(ms float64) string {
	if ms >= 1 {
		return strconv.FormatFloat(ms, 'f', -1, 64) + " ms"
	}
	return fmtUs(ms * 1000)
}

func fmtUs(us float64) string {
	switch {
	case us >= 1000:
		return fmt.Sprintf("%.2f ms", us/1000)
	case us >= 100:
		return fmt.Sprintf("%.0f µs", us)
	default:
		return fmt.Sprintf("%.1f µs", us)
	}
}
