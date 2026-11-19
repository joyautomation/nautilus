//go:build !js

package project

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/joyautomation/nautilus/server"
)

// Hook adapts io to the server's live I/O routes (server.IOHook, #706).
// snapshot resolves a commit to its file tree (vcs.SnapshotFS over the
// controller's history); base is the project the controller runs, which an
// upload's files are laid over.
func (io *IOConfig) Hook(snapshot func(sha string) (fs.FS, bool), base fs.FS) *server.IOHook {
	return &server.IOHook{
		Stage: func(sha string, files map[string][]byte, by string) (any, error) {
			if sha != "" {
				fsys, ok := fs.FS(nil), false
				if snapshot != nil {
					fsys, ok = snapshot(sha)
				}
				if !ok {
					return nil, fmt.Errorf("%w: %s (build with `naut build` inside a git repo, or run from one)", server.ErrIONoSnapshot, sha)
				}
				return io.Stage(fsys, sha, by), nil
			}
			return io.Stage(OverlayFS(base, files), "upload", by), nil
		},
		Validate: func(cand any) (bool, []string, any) {
			v := io.Validate(cand.(*IOCandidate))
			seen := map[string]bool{}
			var drivers []string
			for _, ch := range v.Changes {
				if !seen[ch.Driver] {
					seen[ch.Driver] = true
					drivers = append(drivers, ch.Driver)
				}
			}
			return v.Valid, drivers, v
		},
		Apply: func(ctx context.Context, cand any, by string, revertIfSilent time.Duration) (any, error) {
			res, err := io.ApplyWith(ctx, cand.(*IOCandidate), by, ApplyOptions{RevertIfSilent: revertIfSilent})
			switch {
			case err == nil:
				return res, nil
			case errors.Is(err, ErrApplyBusy), errors.Is(err, ErrIOStale):
				return nil, fmt.Errorf("%w: %w", server.ErrIOConflict, err)
			case errors.Is(err, ErrRollbackFailed):
				return nil, fmt.Errorf("%w: %w", server.ErrIORollbackFailed, err)
			case errors.Is(err, ErrNotKept):
				return nil, fmt.Errorf("%w: %w", server.ErrIONotKept, err)
			}
			return nil, err
		},
		Running: func() any { return io.Running() },
		Differs: func() []server.IODiffer {
			var out []server.IODiffer
			for _, d := range io.Differs() {
				out = append(out, server.IODiffer(d))
			}
			return out
		},
		Fault: io.Fault,
		Discarded: func() []server.IODiscard {
			var out []server.IODiscard
			for _, d := range io.Discarded() {
				out = append(out, server.IODiscard(d))
			}
			return out
		},
		NotKept: io.NotKept,
		Files: func() server.IOFiles {
			f := io.Files()
			out := server.IOFiles{Leading: f.Leading, Conflicts: f.Conflicts, NonIO: f.NonIO, Units: []server.IODiffer{}, Files: []server.IOFile{}}
			for _, u := range f.Units {
				out.Units = append(out.Units, server.IODiffer(u))
			}
			for _, x := range f.Files {
				out.Files = append(out.Files, server.IOFile(x))
			}
			return out
		},
		Watch: func() *server.IOWatch {
			if w := io.Watch(); w != nil {
				sw := server.IOWatch(*w)
				return &sw
			}
			return nil
		},
		CancelRevert: func(by string) (*server.IOWatch, error) {
			w, err := io.CancelRevert(by)
			if err != nil {
				return nil, err
			}
			sw := server.IOWatch(*w)
			return &sw, nil
		},
	}
}
