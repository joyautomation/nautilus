package main

// `naut plcnext bench` measures one window of the in-process tier on the
// controller, for comparing runs (best-effort against SCHED_FIFO, quiet
// against loaded): the shim's lateness histogram and naut's scan-lateness
// histogram are both cumulative, so the window is the difference between
// a read at its start and one at its end; the shim's worst per 10 000-cycle
// window is sampled through it; CPU comes from /proc.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/plcnext"
)

// benchResult is one window, as a JSON line in --out and as the summary.
type benchResult struct {
	Label     string    `json:"label"`
	Start     time.Time `json:"start"`
	Seconds   float64   `json:"seconds"`
	ShimPrio  uint32    `json:"shimPrio"`
	ShimFIFO  bool      `json:"shimFifo"`
	NautSched string    `json:"nautSched"`

	Cycles       uint64    `json:"cycles"`
	Late         uint64    `json:"late"` // over the shim's own threshold
	LateUs       uint32    `json:"lateUs"`
	ShimHist     []uint64  `json:"shimHist"` // the window's cycles per bucket of plcnext.ShimHistLeUs
	ShimP50      string    `json:"shimP50"`
	ShimP99      string    `json:"shimP99"`
	ShimP999     string    `json:"shimP999"`
	ShimP9999    string    `json:"shimP9999"`
	ShimWorstUs  uint32    `json:"shimWorstUs"` // worst 10 000-cycle window seen during this one
	ShimCPU      float64   `json:"shimCpuPct"`
	NautCPU      float64   `json:"nautCpuPct"`
	SystemCPU    float64   `json:"systemCpuPct"`
	Held         uint64    `json:"heldReads,omitempty"`
	NautScans    uint64    `json:"nautScans"`
	NautHist     []uint64  `json:"nautHist,omitempty"`
	NautBuckets  []float64 `json:"nautBucketsUs,omitempty"`
	NautP99      string    `json:"nautP99,omitempty"`
	NautP999     string    `json:"nautP999,omitempty"`
	NautWorstBkt string    `json:"nautWorst,omitempty"`
}

func runPlcnextBench(args []string) int {
	fs := flag.NewFlagSet("plcnext bench", flag.ContinueOnError)
	path := fs.String("shm", plcnext.DefaultShm, "the shim's exchange page")
	dur := fs.Duration("for", 5*time.Minute, "how long the window is")
	api := fs.String("api", "http://127.0.0.1:8080", "naut's dashboard, for its scan lateness (\"\" = shim only)")
	label := fs.String("label", "", "what this run is, for the JSON line")
	out := fs.String("out", "", "append the result as one JSON line to this file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fail := func(err error) int { fmt.Fprintf(os.Stderr, "naut plcnext bench: %v\n", err); return 1 }
	v, err := plcnext.OpenShm(*path)
	if err != nil {
		return fail(err)
	}
	defer v.Close()
	a, _, _, err := v.Read()
	if err != nil {
		return fail(err)
	}
	if sum(a.Hist[:]) == 0 {
		return fail(fmt.Errorf("the shim (pid %d) publishes no lateness histogram: it predates it; deploy the current nautilus_io", a.PID))
	}
	nautPID := findNaut()
	na, _ := nautScan(*api)
	ca := cpuSample(a.PID, nautPID)
	t0 := time.Now()

	// The shim's window worst is its last completed 10 000 cycles; sample
	// it often enough that no window goes by unseen.
	worst := uint32(0)
	b := a
	for deadline := t0.Add(*dur); time.Now().Before(deadline); {
		time.Sleep(min(2*time.Second, time.Until(deadline)))
		if b, _, _, err = v.Read(); err != nil {
			return fail(err)
		}
		if b.StartedNs != a.StartedNs {
			return fail(fmt.Errorf("the shim restarted during the window (now pid %d); run again", b.PID))
		}
		if b.Cycles-a.Cycles >= 20000 { // a window wholly inside this one
			worst = max(worst, b.WorstWinUs)
		}
	}
	cb := cpuSample(b.PID, nautPID)
	nb, nautErr := nautScan(*api)
	secs := time.Since(t0).Seconds()

	r := benchResult{
		Label: *label, Start: t0.UTC(), Seconds: secs, ShimPrio: b.Prio, ShimFIFO: b.FIFO(),
		Cycles: b.Cycles - a.Cycles, Late: b.Late - a.Late, LateUs: b.LateUs, ShimWorstUs: worst,
	}
	hist := make([]uint64, len(b.Hist))
	for i := range hist {
		hist[i] = b.Hist[i] - a.Hist[i]
	}
	shimLe := make([]float64, len(plcnext.ShimHistLeUs))
	for i, x := range plcnext.ShimHistLeUs {
		shimLe[i] = float64(x)
	}
	r.ShimHist = hist
	r.ShimP50, r.ShimP99, r.ShimP999, r.ShimP9999 = pctl(hist, shimLe, 0.5), pctl(hist, shimLe, 0.99), pctl(hist, shimLe, 0.999), pctl(hist, shimLe, 0.9999)
	if ticks := cb.total - ca.total; ticks > 0 {
		r.SystemCPU = 100 * float64(ticks-(cb.idle-ca.idle)) / float64(ticks)
	}
	hz := 100.0 // USER_HZ
	r.ShimCPU = 100 * float64(cb.shim-ca.shim) / hz / secs
	if nautPID > 0 {
		r.NautCPU = 100 * float64(cb.naut-ca.naut) / hz / secs
	}
	if nautErr == nil && na != nil && len(nb.Lateness.Histogram) == len(na.Lateness.Histogram) {
		r.NautScans = nb.Count - na.Count
		r.NautHist = make([]uint64, len(nb.Lateness.Histogram))
		for i := range r.NautHist {
			r.NautHist[i] = nb.Lateness.Histogram[i] - na.Lateness.Histogram[i]
		}
		r.NautBuckets = nb.Lateness.BucketsUs
		r.NautP99, r.NautP999, r.NautWorstBkt = pctl(r.NautHist, r.NautBuckets, 0.99), pctl(r.NautHist, r.NautBuckets, 0.999), pctl(r.NautHist, r.NautBuckets, 1)
		r.NautSched = "normal"
		if nb.Sched.Applied {
			r.NautSched = "applied"
		}
	}

	sched := "normal"
	if r.ShimFIFO {
		sched = fmt.Sprintf("SCHED_FIFO %d", r.ShimPrio)
	}
	fmt.Printf("%s — %.0f s, shim %s, naut %s\n", orDash(r.Label), secs, sched, orDash(r.NautSched))
	fmt.Printf("shim:  %d cycles, %d late (>%d µs, %.3f %%); p50 %s, p99 %s, p99.9 %s, p99.99 %s; worst 10 s window %s\n",
		r.Cycles, r.Late, r.LateUs, plcnextPct(r.Late, r.Cycles), r.ShimP50, r.ShimP99, r.ShimP999, r.ShimP9999, usText(float64(worst)))
	fmt.Printf("       lateness ≤")
	for i, n := range hist {
		bound := "more"
		if i < len(shimLe) {
			bound = usText(shimLe[i])
		}
		fmt.Printf(" %s:%d", bound, n)
	}
	fmt.Println()
	fmt.Printf("cpu:   shim %.1f %%, naut %.1f %% (of one core), system %.1f %% (of all)\n", r.ShimCPU, r.NautCPU, r.SystemCPU)
	if r.NautHist != nil {
		fmt.Printf("naut:  %d scans; lateness p99 %s, p99.9 %s, worst %s\n", r.NautScans, r.NautP99, r.NautP999, r.NautWorstBkt)
	} else if *api != "" {
		fmt.Printf("naut:  no scan lateness from %s (%v)\n", *api, nautErr)
	}
	if *out != "" {
		f, err := os.OpenFile(*out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return fail(err)
		}
		defer f.Close()
		line, _ := json.Marshal(r)
		if _, err := f.Write(append(line, '\n')); err != nil {
			return fail(err)
		}
	}
	return 0
}

func sum(xs []uint64) (n uint64) {
	for _, x := range xs {
		n += x
	}
	return n
}

// pctl is the bucket the q-th quantile falls in, as its upper bound: "≤ 50
// µs", or "> 20 ms" for the open last bucket.
func pctl(hist []uint64, le []float64, q float64) string {
	total := sum(hist)
	if total == 0 {
		return "—"
	}
	need := uint64(q * float64(total))
	if need == 0 {
		need = 1
	}
	var c uint64
	for i, n := range hist {
		c += n
		if c >= need {
			if i < len(le) {
				return "≤ " + usText(le[i])
			}
			return "> " + usText(le[len(le)-1])
		}
	}
	return "—"
}

func usText(us float64) string {
	if us >= 1000 {
		return strconv.FormatFloat(us/1000, 'g', 4, 64) + " ms"
	}
	return strconv.FormatFloat(us, 'g', 4, 64) + " µs"
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// nautScanState is the part of naut's /api/state the bench reads.
type nautScanState struct {
	Count    uint64 `json:"count"`
	Lateness struct {
		Histogram []uint64  `json:"histogram"`
		BucketsUs []float64 `json:"bucketsUs"`
	} `json:"lateness"`
	Sched struct {
		Applied bool `json:"applied"`
	} `json:"sched"`
}

func nautScan(api string) (*nautScanState, error) {
	if api == "" {
		return nil, fmt.Errorf("no --api")
	}
	c := http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get(strings.TrimSuffix(api, "/") + "/api/state")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var st struct {
		Scan nautScanState `json:"scan"`
	}
	if err := json.Unmarshal(body, &st); err != nil {
		return nil, err
	}
	return &st.Scan, nil
}

// findNaut is the running `naut run` (not this process), or 0.
func findNaut() int {
	self := os.Getpid()
	dirs, _ := filepath.Glob("/proc/[0-9]*")
	for _, d := range dirs {
		pid, _ := strconv.Atoi(filepath.Base(d))
		if pid == self {
			continue
		}
		cmd, err := os.ReadFile(d + "/cmdline")
		if err != nil {
			continue
		}
		argv := strings.Split(string(cmd), "\x00")
		if len(argv) > 1 && filepath.Base(argv[0]) == "naut" && argv[1] == "run" {
			return pid
		}
	}
	return 0
}

type cpuTicks struct{ shim, naut, total, idle uint64 }

func cpuSample(shimPID uint32, nautPID int) cpuTicks {
	var c cpuTicks
	c.shim = procTicks(int(shimPID))
	if nautPID > 0 {
		c.naut = procTicks(nautPID)
	}
	if b, err := os.ReadFile("/proc/stat"); err == nil {
		line, _, _ := strings.Cut(string(b), "\n")
		for i, f := range strings.Fields(line)[1:] {
			n, _ := strconv.ParseUint(f, 10, 64)
			c.total += n
			if i == 3 || i == 4 { // idle, iowait
				c.idle += n
			}
		}
	}
	return c
}

// procTicks is a process's user+system time, every thread, in USER_HZ.
func procTicks(pid int) uint64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	_, rest, ok := strings.Cut(string(b), ") ") // past the comm, which may hold spaces
	if !ok {
		return 0
	}
	f := strings.Fields(rest)
	if len(f) < 13 {
		return 0
	}
	u, _ := strconv.ParseUint(f[11], 10, 64)
	s, _ := strconv.ParseUint(f[12], 10, 64)
	return u + s
}
