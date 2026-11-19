package driverconformance

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"io/fs"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing/fstest"

	"github.com/joyautomation/nautilus/replay"
)

// replay against a recording in an in-memory file system. Its link is the
// recording itself: cut, the file is not there (a dataset not mounted
// yet). Once loaded, the recording is in memory, so most contracts cannot
// apply: nothing can fail or go stale after the load, and its outputs
// steer its own clock, not a device. Stop has nothing to wait for (C6).
func init() {
	const loaded = "once loaded, the recording is in memory: no read of it can fail, and there is no connection to lose or regain (a tag the recording has no series for reads Bad from the load, not after a failed read)"
	register(subject{
		name: "replay",
		byDesign: map[string]string{
			"C3": loaded,
			"C4": loaded,
			"C7": loaded,
			"C5": "its outputs (speed, pause, seek) steer its own clock: nothing reaches a device",
			"C8": "its outputs (speed, pause, seek) steer its own clock: nothing reaches a device",
		},
		start: func(t tb, o startOpts) *rig {
			var rec bytes.Buffer
			w := gzip.NewWriter(&rec)
			_, _ = w.Write([]byte(`{"t0": 1000, "step": 60, "n": 3, "series": {"SW1_Port1.InBps": [100, 200, 300]}}`))
			_ = w.Close()
			l := &fsLink{fs: fstest.MapFS{"rec.json.gz": {Data: rec.Bytes()}}, name: "rec.json.gz"}
			if o.cut {
				l.cut()
			}
			m, err := replay.ParseManifest([]byte("history: rec.json.gz\ntags:\n  - name: In\n    type: SwitchPort\n    series: SW1_Port1\n"))
			if err != nil {
				t.Fatalf("manifest: %v", err)
			}
			d, err := replay.New(m, l, replay.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
			if err != nil {
				t.Fatalf("replay.New: %v", err)
			}
			if o.install != nil {
				o.install(d)
			}
			ctx, cancel := context.WithCancel(context.Background())
			d.Start(ctx)
			stopOnCleanup(t, cancel, d.Stop)
			return &rig{
				drv: d, in: "In",
				polls: func() uint64 { return d.Health().Reads },
				link:  l,
			}
		},
	})
}

// fsLink is a file system holding one recording, which cut takes away:
// every look at it while cut is refused. Nothing to freeze: a read of an
// in-memory file cannot hang (cannotHold), so C6 checks Stop alone.
type fsLink struct {
	fs   fstest.MapFS
	name string

	mu       sync.Mutex
	down     bool
	refusals atomic.Int64
}

var (
	_ link      = (*fsLink)(nil)
	_ fs.StatFS = (*fsLink)(nil)
)

func (l *fsLink) gone(name string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.down && name == l.name {
		l.refusals.Add(1)
		return true
	}
	return false
}

func (l *fsLink) Open(name string) (fs.File, error) {
	if l.gone(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return l.fs.Open(name)
}

func (l *fsLink) Stat(name string) (fs.FileInfo, error) {
	if l.gone(name) {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
	}
	return l.fs.Stat(name)
}

func (l *fsLink) cut()           { l.mu.Lock(); l.down = true; l.mu.Unlock() }
func (l *fsLink) heal()          { l.mu.Lock(); l.down = false; l.mu.Unlock() }
func (l *fsLink) freeze()        {}
func (l *fsLink) thaw()          {}
func (l *fsLink) refused() int64 { return l.refusals.Load() }
func (l *fsLink) held() int64    { return 0 }
func (l *fsLink) cannotHold()    {}
