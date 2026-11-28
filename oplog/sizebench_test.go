//go:build oplogbench

// The operator-intent log's size benchmark (#1455): what a log of a week,
// a month, a year of writes costs at startup, in memory, on disk, and for
// a replacement peer's catch-up. Measurement only; behind its own tag so
// no CI job runs it:
//
//	OPLOG_BENCH_ENTRIES=16800,72000,876000 OPLOG_BENCH_DIR=/var/tmp/oplogbench \
//	  go test -tags oplogbench -run '^TestOplogSizeBench$' -v -timeout 4h ./oplog
//
// OPLOG_BENCH_CATCHUP_MAX (default 1000000) skips the catch-up above that
// many entries. Each size's startup runs in a child process of the test
// binary, so its memory is that open's alone.
package oplog

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// benchMix writes n entries the way a live log holds them: each write an
// entry line and then its commit line. The mix is a busy plant's: per
// alarm activation an ack, an ack-clear and, with an escalation policy,
// an occurrence start and end; a few sessions, shelves, notes and
// substitutes a day. Names are synthetic and plant-shaped.
func benchMix(w *bufio.Writer, n int) error {
	r := rand.New(rand.NewPCG(1455, 7))
	alarm := func() string {
		return fmt.Sprintf("Area%d/Unit%02d/%s%03d.%s", 1+r.IntN(4), 1+r.IntN(30),
			[]string{"Pump", "Valve", "Tank", "Motor", "Compressor"}[r.IntN(5)], r.IntN(200),
			[]string{"HighPressure", "LowLevel", "HighTemp", "Fault", "NotRunning"}[r.IntN(5)])
	}
	users := []string{"operator1", "operator2", "supervisor", "engineer"}
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	for seq := uint64(1); seq <= uint64(n); seq++ {
		ts += int64(30_000 + r.IntN(15_000)) // about 2,400 a day
		u := users[r.IntN(len(users))]
		e := Entry{Seq: seq, Term: 1, TS: ts, By: u, Origin: "http://10.0.0.1:8080", Principal: "user:" + u}
		occ := func() Occ {
			return Occ{ID: alarm(), Occ: strconv.FormatInt(ts-int64(r.IntN(600_000)), 36) + "-" + strconv.Itoa(r.IntN(1000)), ActiveMs: ts - int64(r.IntN(600_000))}
		}
		switch k := r.IntN(100); {
		case k < 24:
			o := occ()
			e.Kind, e.IDs, e.Occs = KindAck, []string{o.ID}, []Occ{o}
		case k < 48:
			e.Kind, e.ID, e.By, e.Principal = KindAckClear, alarm(), "system", ""
		case k < 72:
			e.Kind, e.Occs, e.By, e.Principal = KindOccurrenceStart, []Occ{occ()}, "system", ""
		case k < 96:
			o := occ()
			o.ActiveMs = 0
			e.Kind, e.Occs, e.By, e.Principal = KindOccurrenceEnd, []Occ{o}, "system", ""
		case k < 97:
			e.Kind, e.ID, e.Until = KindShelve, alarm(), ts+8*3600_000
		case k < 98:
			e.Kind = KindNote
			e.Value = json.RawMessage(fmt.Sprintf(`{"v":1,"id":"n%d","rev":1,"target":{"kind":"equipment","id":"Area2/Unit07/Pump012"},"text":%q}`,
				seq, strings.Repeat("Pump 12 seal weeping again; maintenance called, run on B until relieved. ", 1+r.IntN(3))))
		case k < 99:
			e.Kind = KindSession
			e.Value = json.RawMessage(fmt.Sprintf(`{"v":1,"op":"open","id":"%016x","user":%q,"role":"operator","exp":%d}`, r.Uint64(), u, ts+12*3600_000))
		default:
			e.Kind, e.Tag, e.Value, e.GrantedMs, e.Until = KindSubstitute, "plc1/"+alarm(), json.RawMessage(`42.5`), 3600_000, ts+3600_000
		}
		for _, l := range []fileLine{{E: &e}, {C: &seq}} {
			b, _ := json.Marshal(l)
			if _, err := w.Write(append(b, '\n')); err != nil {
				return err
			}
		}
	}
	return w.Flush()
}

func benchDir(t *testing.T) string {
	if d := os.Getenv("OPLOG_BENCH_DIR"); d != "" {
		d = filepath.Join(d, fmt.Sprint(time.Now().UnixNano()))
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(d) })
		return d
	}
	return t.TempDir()
}

// procKB reads a /proc/self/status field in kB (0 where there is none).
func procKB(field string) int64 {
	b, _ := os.ReadFile("/proc/self/status")
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, field+":"); ok {
			n, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
			return n
		}
	}
	return 0
}

type openResult struct {
	Seconds   float64 `json:"seconds"`
	Applied   int     `json:"applied"`
	PeakRSSMB float64 `json:"peakRSSMB"` // VmHWM: the high-water mark, the open included
	RSSMB     float64 `json:"rssMB"`     // VmRSS after the open and a GC: what it keeps
	HeapMB    float64 `json:"heapMB"`    // HeapInuse after a GC
	BaseMB    float64 `json:"baseMB"`    // VmRSS before the open
}

// TestOplogSizeBenchOpen is the child: it opens OPLOG_BENCH_OPEN (a
// standalone node, the way a lone controller opens its log) and prints
// what it cost.
func TestOplogSizeBenchOpen(t *testing.T) {
	file := os.Getenv("OPLOG_BENCH_OPEN")
	if file == "" {
		t.Skip("the size benchmark's child")
	}
	runtime.GC()
	res := openResult{BaseMB: float64(procKB("VmRSS")) / 1024}
	start := time.Now()
	n, err := New(Options{Self: "http://a", File: file, Apply: func(Entry) error { res.Applied++; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	res.Seconds = time.Since(start).Seconds()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	res.HeapMB = float64(ms.HeapInuse) / (1 << 20)
	res.RSSMB = float64(procKB("VmRSS")) / 1024
	res.PeakRSSMB = float64(procKB("VmHWM")) / 1024
	runtime.KeepAlive(n)
	b, _ := json.Marshal(res)
	fmt.Println("BENCHOPEN " + string(b))
}

func TestOplogSizeBench(t *testing.T) {
	sizes := os.Getenv("OPLOG_BENCH_ENTRIES")
	if sizes == "" {
		t.Skip("set OPLOG_BENCH_ENTRIES (a comma list)")
	}
	catchMax := 1_000_000
	if v, err := strconv.Atoi(os.Getenv("OPLOG_BENCH_CATCHUP_MAX")); err == nil {
		catchMax = v
	}
	host, _ := os.Hostname()
	t.Logf("host %s, %s/%s, %d CPUs", host, runtime.GOOS, runtime.GOARCH, runtime.NumCPU())
	fmt.Println("| entries | disk MB | bytes/entry | startup s | peak RSS MB | kept RSS MB | heap MB | catch-up s | catch-up entries/s |")
	fmt.Println("|---|---|---|---|---|---|---|---|---|")
	for _, s := range strings.Split(sizes, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			t.Fatal(err)
		}
		dir := benchDir(t)
		file := filepath.Join(dir, "leader.jsonl")
		f, err := os.Create(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := benchMix(bufio.NewWriterSize(f, 1<<20), n); err != nil {
			t.Fatal(err)
		}
		f.Close()
		st, _ := os.Stat(file)

		cmd := exec.Command(os.Args[0], "-test.run=^TestOplogSizeBenchOpen$", "-test.v")
		cmd.Env = append(os.Environ(), "OPLOG_BENCH_OPEN="+file)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("the open child: %v\n%s", err, out)
		}
		var res openResult
		for _, l := range strings.Split(string(out), "\n") {
			if j, ok := strings.CutPrefix(l, "BENCHOPEN "); ok {
				json.Unmarshal([]byte(j), &res)
			}
		}
		if res.Applied != n {
			t.Fatalf("the open applied %d of %d", res.Applied, n)
		}

		catch, rate := "skipped", "–"
		if n <= catchMax {
			d := benchCatchUp(t, dir, file, n)
			catch, rate = fmt.Sprintf("%.1f", d.Seconds()), fmt.Sprintf("%.0f", float64(n)/d.Seconds())
		}
		fmt.Printf("| %d | %.1f | %.0f | %.2f | %.0f | %.0f | %.0f | %s | %s |\n",
			n, float64(st.Size())/(1<<20), float64(st.Size())/float64(n), res.Seconds, res.PeakRSSMB, res.RSSMB, res.HeapMB, catch, rate)
		os.RemoveAll(dir)
	}
}

// benchCatchUp times a replacement peer, its log empty, catching up the
// whole of file from a leader over HTTP (CatchUp: every page fetched,
// then every entry written and synced), the way #1029's replacement box
// joins: the leader and the witness's copy hold the log and the leader
// has synced its term with the witness. Every file is on the same disk.
func benchCatchUp(t *testing.T, dir, file string, n int) time.Duration {
	wit := filepath.Join(dir, "witness.jsonl")
	if b, err := os.ReadFile(file); err != nil || os.WriteFile(wit, b, 0o600) != nil {
		t.Fatalf("the witness's copy: %v", err)
	}
	ss := make([]*httptest.Server, 3)
	urls := make([]string, 3)
	for i := range ss {
		ss[i] = httptest.NewUnstartedServer(nil)
		urls[i] = "http://" + ss[i].Listener.Addr().String()
	}
	files := []string{file, wit, filepath.Join(dir, "replacement.jsonl")}
	var nodes []*Node
	for i := range ss {
		nd, err := New(Options{Self: urls[i], Peers: urls, Leadership: Static{Self: urls[i], Leader: urls[0]}, File: files[i], Token: "t",
			Apply: func(Entry) error { return nil }, Timeout: 10 * time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, nd)
		ss[i].Config.Handler = nd.Handler()
	}
	ss[0].Start()
	ss[1].Start()
	defer func() {
		for i := range ss {
			ss[i].Close()
			nodes[i].Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)
	defer cancel()
	if err := nodes[0].SyncLeader(ctx); err != nil {
		t.Fatalf("the leader's sync with the witness: %v", err)
	}
	want := nodes[0].Commit()
	ss[2].Start()
	start := time.Now()
	for nodes[2].Commit() < want {
		if err := nodes[2].CatchUp(ctx); err != nil {
			t.Fatalf("catch-up after %v at commit %d of %d: %v", time.Since(start), nodes[2].Commit(), want, err)
		}
	}
	return time.Since(start)
}
