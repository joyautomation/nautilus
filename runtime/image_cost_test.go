//go:build !js

package runtime

import (
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
)

// BenchmarkImageSecondCopy is what the second round of an image costs a
// fast task (#710 S2c): one more copy of its frame and carried tags in one
// of its scans, against the scan without one. The image makes at most two
// copies per task, so this is the whole extra a pair's leader pays per
// image, per fast task.
func BenchmarkImageSecondCopy(b *testing.B) {
	rt, err := New(tagsOptions(&imgClock{t: time.Unix(1_700_000_000, 0)}))
	if err != nil {
		b.Fatal(err)
	}
	if err := rt.Tags().WriteGlobal("Level", ir.RealVal(0)); err != nil { // the driver's first read
		b.Fatal(err)
	}
	rt.Scan()
	var img Image
	for range 3 { // warm the buffers
		rt.RequestImage()
		rt.Scan()
		_ = rt.ScanTask("aux")
		rt.TakeImage(&img)
		rt.Scan()
		rt.TakeImage(&img)
	}
	b.Run("scan", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			rt.Scan()
		}
	})
	b.Run("scan-with-copy", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			rt.prog.requestImage(imageStamp(1, 1))
			rt.Scan()
			rt.prog.takeImage(MainTaskName, &img.Tasks[0], imageStamp(1, 1))
		}
	})
}

// BenchmarkTagWriteDuringCopies is the latency of a tag writer while a
// scan copies its carried tags under the store's read lock, for a copy in
// every scan (the worst case), and for the copies one and two rounds an
// image make at one image per 100 scans (the stream's 2 ms interval against
// a 1 ms scan is far more than that case's rate of one in 100, so this
// is the upper side).
func BenchmarkTagWriteDuringCopies(b *testing.B) {
	for _, mode := range []struct {
		name  string
		every int // a copy in every Nth scan; 0 never
	}{{"no-copies", 0}, {"copy-1-in-100-scans", 100}, {"copy-2-in-100-scans", 50}, {"copy-every-scan", 1}} {
		b.Run(mode.name, func(b *testing.B) {
			rt, err := New(tagsOptions(&imgClock{t: time.Unix(1_700_000_000, 0)}))
			if err != nil {
				b.Fatal(err)
			}
			if err := rt.Tags().WriteGlobal("Level", ir.RealVal(0)); err != nil {
				b.Fatal(err)
			}
			rt.Scan()
			var stop atomic.Bool
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				for n := 1; !stop.Load(); n++ {
					if mode.every > 0 && n%mode.every == 0 {
						rt.prog.requestImage(imageStamp(1, 0))
					}
					rt.Scan()
				}
			}()
			lat := make([]time.Duration, 0, b.N)
			ops := []WriteOp{{Path: "Mode", Value: int64(3)}}
			b.ResetTimer()
			for range b.N {
				t0 := time.Now()
				rt.Tags().Write(ops)
				lat = append(lat, time.Since(t0))
			}
			b.StopTimer()
			stop.Store(true)
			wg.Wait()
			slices.Sort(lat)
			var sum time.Duration
			for _, d := range lat {
				sum += d
			}
			b.ReportMetric(float64(sum.Nanoseconds())/float64(len(lat)), "write-mean-ns")
			b.ReportMetric(float64(lat[len(lat)*99/100].Nanoseconds()), "write-p99-ns")
			b.ReportMetric(float64(lat[len(lat)-1].Nanoseconds()), "write-max-ns")
		})
	}
}
