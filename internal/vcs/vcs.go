// Package vcs captures a project's git provenance — every commit that
// touched the project directory, each with its diff and a content-addressed
// snapshot of the project's files — into a server.ProgramHistory the
// controller can carry. Capture runs where git exists (a dev checkout,
// the CI job `naut build` runs in); the deployed artifact answers
// GET /api/program/history from the embedded result with no git binary,
// no .git dir, and no network, which is what a distroless container on an
// air-gapped plant network has to work with.
//
// Snapshots are stored git-style: ls-tree hands us each file's blob object
// id, blobs dedupe on that id, and a source file unchanged across fifty
// commits costs its bytes once. That full-content capture (not just diffs,
// which is all a Versions page needs) is what makes a commit activatable —
// POST /api/program/activate rebuilds the project at any captured sha via
// SnapshotFS and warm-swaps the result.
package vcs

import (
	"fmt"
	"os/exec"
	"strings"
	"testing/fstest"
	"unicode/utf8"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/server"
)

// DefaultDepth bounds how many commits Capture walks. It is a cap on
// embedded size, not a typical value — `git log -- .` only counts commits
// touching the project directory, usually a handful even in a monorepo.
const DefaultDepth = 200

// maxBlob is the per-file snapshot ceiling. Project sources are text and
// small; anything bigger (or non-UTF-8) is skipped from snapshots — its
// commits stay reviewable via diff but stop being activatable if the file
// was one the manifest loads.
const maxBlob = 512 * 1024

// Capture reads dir's git history. It returns (nil, nil) when the story is
// simply "no history here" — git not installed, dir not in a repo, no
// commits yet — so callers can degrade gracefully; a real capture failure
// mid-walk returns an error.
func Capture(dir string, depth int) (*server.ProgramHistory, error) {
	if depth <= 0 {
		depth = DefaultDepth
	}
	git := func(args ...string) (string, error) {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
				return "", fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(string(ee.Stderr)))
			}
			return "", fmt.Errorf("git %s: %w", args[0], err)
		}
		return string(out), nil
	}

	head, err := git("rev-parse", "HEAD")
	if err != nil {
		// Covers all three "no history" cases: exec.ErrNotFound (no git),
		// "not a git repository", and "unknown revision" (empty repo).
		return nil, nil
	}
	h := &server.ProgramHistory{
		Built: strings.TrimSpace(head),
		Blobs: map[string]string{},
	}
	if status, err := git("status", "--porcelain", "--", "."); err == nil && strings.TrimSpace(status) != "" {
		h.BuiltDirty = true
	}

	log, err := git("log", "-n", fmt.Sprint(depth),
		"--format=%H%x00%h%x00%an%x00%aI%x00%s", "--", ".")
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		parts := strings.SplitN(line, "\x00", 5)
		if len(parts) != 5 {
			continue
		}
		c := server.ProgramCommit{
			Sha: parts[0], Short: parts[1], Author: parts[2],
			Date: parts[3], Subject: parts[4],
			Files: map[string]string{},
		}
		diff, err := git("show", "--format=", "--no-color", c.Sha, "--", ".")
		if err != nil {
			return nil, err
		}
		c.Diff = diff

		// The project's tree at this commit. ls-tree is subdirectory-aware:
		// run with -C dir it lists only entries under dir, with paths
		// relative to dir — project-relative whether the project is the
		// repo root or a monorepo subdirectory.
		tree, err := git("ls-tree", "-r", c.Sha)
		if err != nil {
			// The project directory may not exist at an old commit that
			// only touched a since-moved path; diff-only is fine.
			c.Files = nil
			h.Commits = append(h.Commits, c)
			continue
		}
		for _, entry := range strings.Split(strings.TrimSpace(tree), "\n") {
			// <mode> SP <type> SP <objid> TAB <path>
			meta, path, ok := strings.Cut(entry, "\t")
			f := strings.Fields(meta)
			if !ok || len(f) != 3 || f[1] != "blob" || skipPath(path) {
				continue
			}
			id := f[2]
			if _, have := h.Blobs[id]; !have {
				content, err := git("cat-file", "blob", id)
				if err != nil {
					return nil, err
				}
				if len(content) > maxBlob || !utf8.ValidString(content) {
					continue
				}
				h.Blobs[id] = content
			}
			c.Files[path] = id
		}
		h.Commits = append(h.Commits, c)
	}
	return h, nil
}

// skipPath mirrors the build's archive walk: snapshots hold what a built
// binary ships — no dotfiles, no acceptance suites. (Diffs still show test
// changes; suites just aren't part of what activation would run.)
func skipPath(path string) bool {
	if strings.HasSuffix(path, acceptance.SuffixTest) {
		return true
	}
	for _, seg := range strings.Split(path, "/") {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}

// SnapshotFS reconstructs the project's file tree at one captured commit,
// as an fs.FS the project loader accepts — project.Sources over this is
// exactly the boot composition, run against the past. ok is false when the
// sha isn't in the history or was captured diff-only.
func SnapshotFS(h *server.ProgramHistory, sha string) (fstest.MapFS, bool) {
	c := h.Find(sha)
	if c == nil || len(c.Files) == 0 {
		return nil, false
	}
	fsys := fstest.MapFS{}
	for path, id := range c.Files {
		content, ok := h.Blobs[id]
		if !ok {
			continue
		}
		fsys[path] = &fstest.MapFile{Data: []byte(content)}
	}
	return fsys, true
}
