package node

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// ProbeFile is the file the start-up probe writes in a state directory
// (ProbeStateDir): named apart from every state, members, key and pins
// file, and from their temporaries, and removed after.
const ProbeFile = "write-probe.naut"

// probeWrites is how many writes the probe times.
const probeWrites = 3

// ProbeStateDir times probeWrites writes of a small file in dir, each made
// the way the voter state is (writeAtomic: written, fsynced, renamed, the
// directory fsynced), and returns the slowest (#1436). The file is
// removed after, and a leftover a crash left is removed first. write, when
// not nil, replaces the write (Options.StateWrite: tests, slow-disk
// builds).
func ProbeStateDir(dir string, write func(*os.File, []byte) (int, error)) (time.Duration, error) {
	path := filepath.Join(dir, ProbeFile)
	defer removeProbe(path)
	removeProbe(path)
	b := make([]byte, 256) // about a voter state's size
	var slowest time.Duration
	for i := range probeWrites {
		b[0] = byte(i)
		start := time.Now()
		if err := writeAtomic(path, b, write); err != nil {
			return slowest, err
		}
		slowest = max(slowest, time.Since(start))
	}
	return slowest, nil
}

func removeProbe(path string) {
	for _, p := range []string{path, path + ".tmp"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			slog.Default().Debug("redundancy: the write probe's file can't be removed", "file", p, "err", err)
		}
	}
}

// ProbeVerdict judges a probe's slowest write at lease: an error past the
// lease's write budget (quorum.Config.WriteBudget: a campaign can't win on
// writes that slow), a warning past half of it, info under. It never
// stops a start: a disk can be slow for a moment, and a controller that
// won't start is no better than one that warns.
func ProbeVerdict(took, lease time.Duration) (slog.Level, string) {
	budget := writeBudget(lease)
	ms := took.Round(100 * time.Microsecond)
	switch {
	case took > budget:
		return slog.LevelError, fmt.Sprintf("the state directory's disk is too slow for this lease: a voter state write measured %v, and a %v lease needs under %v. "+
			"With writes this slow the pair can be left with no leader (it fails safe: never two). Use a faster disk for the state directory, or a longer lease", ms, lease, budget)
	case writeIsSlow(took, lease):
		return slog.LevelWarn, fmt.Sprintf("the state directory's disk is slow for this lease: a voter state write measured %v, and a %v lease needs under %v; "+
			"a slower moment can leave the pair with no leader (it fails safe: never two)", ms, lease, budget)
	}
	return slog.LevelInfo, fmt.Sprintf("the state directory's disk: a voter state write measured %v (a %v lease needs under %v)", ms, lease, budget)
}

// MinLeaseFor is the shortest lease whose write budget took fits within
// (the witness's start-up report: it serves pairs of any lease up to its
// own).
func MinLeaseFor(took time.Duration) time.Duration {
	const ref = time.Second
	return time.Duration(float64(took) * float64(ref) / float64(writeBudget(ref))).Round(time.Millisecond)
}

// ProbeAndLog runs the start-up probe in dir and logs its verdict at lease
// with log (prefix "redundancy" or "witness"). A probe that can't write
// logs that and goes on: the voter's own writes will say more.
func ProbeAndLog(log *slog.Logger, prefix, dir string, lease time.Duration, write func(*os.File, []byte) (int, error)) {
	took, err := ProbeStateDir(dir, write)
	if err != nil {
		log.Warn(prefix+": the state directory's write probe failed", "dir", dir, "err", err)
		return
	}
	level, msg := ProbeVerdict(took, lease)
	if mut("probe_error_as_info") {
		level = slog.LevelInfo
	}
	log.Log(context.Background(), level, prefix+": "+msg, "dir", dir, "measured", took, "min-lease", MinLeaseFor(took))
}
