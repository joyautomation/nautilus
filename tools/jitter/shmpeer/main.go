//go:build linux

// shmpeer is the supervisor side of the Phase 3 shared-memory spike: it
// maps the segment rt/fastloop writes, feeds it setpoints the way a
// Nautilus task would (through the `in` seqlock), reads the loop's
// results (through the `out` seqlock), and measures the exchange — how
// old a result is when the supervisor sees it, how many reads were torn
// and retried, and whether the loop's scan counter advances at its rate.
//
//	go run ./tools/jitter/shmpeer -duration 60s [-shm /dev/shm/nautilus-rt] [-poll 1ms]
//
// Stdlib only: syscall.Mmap and sync/atomic on the mapped words. The
// layout is documented in rt/fastloop/src/main.rs.
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

const (
	magic     = 0x4e41555452545348
	shmSize   = 4096
	nIn       = 8
	offInSeq  = 64
	offInVals = 72
	offOutSeq = 256
	offStamp  = 264
	offScan   = 272
	offOut    = 280
)

func monotonicNs() int64 {
	var ts syscall.Timespec
	_, _, _ = syscall.Syscall(syscall.SYS_CLOCK_GETTIME, 1, uintptr(unsafe.Pointer(&ts)), 0)
	return ts.Nano()
}

func main() {
	shmPath := flag.String("shm", "/dev/shm/nautilus-rt", "segment path")
	dur := flag.Duration("duration", 60*time.Second, "how long to run")
	poll := flag.Duration("poll", time.Millisecond, "how often the supervisor reads results and writes setpoints")
	flag.Parse()

	f, err := os.OpenFile(*shmPath, os.O_RDWR, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "shmpeer:", err, "(start rt/fastloop first)")
		os.Exit(1)
	}
	mem, err := syscall.Mmap(int(f.Fd()), 0, shmSize, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		fmt.Fprintln(os.Stderr, "shmpeer: mmap:", err)
		os.Exit(1)
	}
	if binary.LittleEndian.Uint64(mem[0:8]) != magic {
		fmt.Fprintln(os.Stderr, "shmpeer: segment has no fastloop header")
		os.Exit(1)
	}
	periodNs := int64(binary.LittleEndian.Uint32(mem[12:16]))
	inSeq := (*uint32)(unsafe.Pointer(&mem[offInSeq]))
	outSeq := (*uint32)(unsafe.Pointer(&mem[offOutSeq]))

	writeIn := func(vals [nIn]float64) {
		s := atomic.LoadUint32(inSeq)
		atomic.StoreUint32(inSeq, s+1) // odd: writing
		for i, v := range vals {
			binary.LittleEndian.PutUint64(mem[offInVals+8*i:], math.Float64bits(v))
		}
		atomic.StoreUint32(inSeq, s+2)
	}
	readOut := func() (stamp int64, scan uint64, cv float64, torn int) {
		for {
			s1 := atomic.LoadUint32(outSeq)
			if s1&1 == 1 {
				torn++
				continue
			}
			stamp = int64(binary.LittleEndian.Uint64(mem[offStamp:]))
			scan = binary.LittleEndian.Uint64(mem[offScan:])
			cv = math.Float64frombits(binary.LittleEndian.Uint64(mem[offOut:]))
			if atomic.LoadUint32(outSeq) == s1 {
				return
			}
			torn++
		}
	}

	var ages []float64 // µs between the loop writing a result and us reading it
	var torn, reads int
	var firstScan, lastScan uint64
	var lastSeen uint64
	fresh := 0 // reads that saw a new scan
	start := time.Now()
	t := time.NewTicker(*poll)
	defer t.Stop()
	i := 0
	for range t.C {
		if time.Since(start) > *dur {
			break
		}
		i++
		// A setpoint that moves: a 0.1 Hz triangle plus the PI gains.
		sp := 40 + 20*math.Abs(math.Mod(float64(i)*poll.Seconds()*0.1, 2)-1)
		writeIn([nIn]float64{30 + 0.4*sp, sp, 2.0, 0.5})
		stamp, scan, _, tn := readOut()
		now := monotonicNs()
		torn += tn
		reads++
		if scan != lastSeen {
			fresh++
			ages = append(ages, float64(now-stamp)/1e3)
			if firstScan == 0 {
				firstScan = scan
			}
			lastScan = scan
			lastSeen = scan
		}
	}
	sort.Float64s(ages)
	q := func(p float64) float64 {
		if len(ages) == 0 {
			return 0
		}
		return ages[int(p*float64(len(ages)-1))]
	}
	elapsed := time.Since(start)
	loopRate := float64(lastScan-firstScan) / elapsed.Seconds()
	fmt.Printf("shmpeer: %d reads over %v at %v; loop period %d µs; loop advanced %d scans (%.1f/s, expected %.0f/s)\n",
		reads, elapsed.Round(time.Second), *poll, periodNs/1000, lastScan-firstScan, loopRate, 1e9/float64(periodNs))
	fmt.Printf("| exchange | fresh results seen | torn reads retried | result age p50 | p99 | p99.9 | max |\n|---|---:|---:|---:|---:|---:|---:|\n")
	fmt.Printf("| shm seqlock, Go reads every %v | %d | %d | %.1f µs | %.1f µs | %.1f µs | %.1f µs |\n",
		*poll, fresh, torn, q(.5), q(.99), q(.999), q(1))
}
