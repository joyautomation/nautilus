package main

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/retain"
	"github.com/joyautomation/nautilus/runtime"
)

// imageExamples are the shipped projects a pair's state image is checked
// and measured on (#710 S1): ladder, FBD, SFC and ST programs, several
// tasks, UDT tags.
var imageExamples = []string{"lift-station", "batch-skid", "remote-fleet/scada", "well-site", "bin-fill", "it-rack", "plclab"}

type exampleImage struct {
	name   string
	rt     *runtime.Runtime
	sch    *acceptance.Scheduler
	img    runtime.Image
	wire   []byte
	leaves int
}

// imageOf runs an example a while on a virtual clock and takes one image of
// it, retain section included, as a leader would.
func imageOf(t *testing.T, name string) *exampleImage {
	t.Helper()
	proj, err := project.Load(os.DirFS(filepath.Join("..", "..", "examples", name)), "nautilus.yaml")
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	o := proj.Runtime
	o.Retain = retain.New("", filepath.Join(t.TempDir(), "retain.json"))
	rt, sch, err := acceptance.NewRuntime(o)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	sch.Advance(10 * time.Second)
	ex := &exampleImage{name: name, rt: rt, sch: sch}
	rt.RequestImage()
	if ok, _ := sch.AdvanceUntil(time.Minute, 0, func() bool { return rt.TakeImage(&ex.img) }); !ok {
		t.Fatalf("%s: no image within a minute of scans", name)
	}
	ex.img.Term, ex.img.Build = 1, "test"
	if ex.img.Retain, err = rt.RetainImage(); err != nil {
		t.Fatal(err)
	}
	if ex.wire, err = runtime.EncodeImage(nil, &ex.img); err != nil {
		t.Fatal(err)
	}
	for _, ti := range ex.img.Tasks {
		ex.leaves += len(ti.Leaves)
	}
	return ex
}

// Every example's image decodes to what was encoded and applies, every
// task, to a shadow of a second runtime of the same build: the frames of
// real ladder, FBD and SFC programs fit leaf for leaf.
func TestStateImageOfEveryExampleFitsItsOwnBuild(t *testing.T) {
	for _, name := range imageExamples {
		t.Run(name, func(t *testing.T) {
			ex := imageOf(t, name)
			var got runtime.Image
			if err := runtime.DecodeImage(ex.wire, &got); err != nil {
				t.Fatal(err)
			}
			for i := range got.Tasks { // an empty frame decodes as an empty slice
				if len(got.Tasks[i].Leaves) == 0 {
					got.Tasks[i].Leaves = ex.img.Tasks[i].Leaves
				}
			}
			if !reflect.DeepEqual(got, ex.img) {
				t.Fatal("decoded image differs from the encoded one")
			}
			proj, err := project.Load(os.DirFS(filepath.Join("..", "..", "examples", name)), "nautilus.yaml")
			if err != nil {
				t.Fatal(err)
			}
			stby, _, err := acceptance.NewRuntime(proj.Runtime)
			if err != nil {
				t.Fatal(err)
			}
			rep, err := stby.NewShadow().Apply(&got)
			if err != nil || !rep.InSync() || len(rep.Applied) != len(got.Tasks) {
				t.Fatalf("apply: %+v, %v", rep, err)
			}
		})
	}
}

// TestStateImageMeasure prints what the image costs on each example (#710
// S1 guards): bytes per image and per second at the 100 ms cap, the
// encode time, and a main scan's p50/p99 with and without the copy.
// Measurement, not a gate: NAUTILUS_IMAGE_MEASURE=1 to run it.
func TestStateImageMeasure(t *testing.T) {
	if os.Getenv("NAUTILUS_IMAGE_MEASURE") == "" {
		t.Skip("set NAUTILUS_IMAGE_MEASURE=1 to measure")
	}
	const scans = 4000
	for _, name := range imageExamples {
		ex := imageOf(t, name)
		buf := ex.wire
		t0 := time.Now()
		const encodes = 500
		for range encodes {
			buf, _ = runtime.EncodeImage(buf[:0], &ex.img)
		}
		enc := time.Since(t0) / encodes

		var img runtime.Image
		plain := make([]time.Duration, 0, scans)
		copied := make([]time.Duration, 0, scans)
		for i := range 2 * scans {
			ask := i%2 == 1
			if ask {
				ex.rt.RequestImage()
			}
			s := time.Now()
			ex.rt.Scan()
			d := time.Since(s)
			if ask {
				copied = append(copied, d)
				for _, tn := range ex.rt.Schedule() { // the other tasks' copies, so the take succeeds
					if tn.Name != runtime.MainTaskName {
						_ = ex.rt.ScanTask(tn.Name)
					}
				}
				ex.rt.TakeImage(&img)
			} else {
				plain = append(plain, d)
			}
		}
		p := func(ds []time.Duration, q float64) time.Duration {
			slices.Sort(ds)
			return ds[int(q*float64(len(ds)-1))]
		}
		t.Logf("%-13s tasks %d leaves %6d retain %6d B image %7d B = %7.1f kB/s at 10/s; encode %v; main scan p50 %v -> %v, p99 %v -> %v",
			name, len(ex.img.Tasks), ex.leaves, len(ex.img.Retain), len(ex.wire), float64(len(ex.wire))*10/1000, enc,
			p(plain, .5), p(copied, .5), p(plain, .99), p(copied, .99))
	}
}
