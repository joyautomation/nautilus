package project

import (
	"io/fs"
	"path"
	"strings"
	"time"
)

// hmiStaleSlack absorbs filesystem timestamp granularity: a source saved
// in the same instant the build finished is not "newer".
const hmiStaleSlack = 2 * time.Second

// StaleHMISource reports the newest HMI source that changed after the
// HMI at hmiDir (server.hmi, e.g. "hmi/build") was last built, or "" when
// the build is current, missing, or has no timestamps to compare (a
// built binary's embedded project). An HMI compiles the project's mimic
// and scene documents into its bundle, so an edit to one only reaches the
// served page through the HMI's own build — `naut run`, `naut build` and
// `naut check` warn rather than serve or ship the old one silently.
//
// The sources are the HMI's own project (hmiDir's parent, e.g. "hmi/"),
// less its dependencies, dot-directories (.svelte-kit) and the build
// itself, plus the project root's *.mimic.json and *.scene.json. An HMI
// at the project root (no parent to name) is not checked.
func StaleHMISource(fsys fs.FS, hmiDir string) string {
	if hmiDir == "" {
		return ""
	}
	hmiProject := path.Dir(hmiDir)
	if hmiProject == "." || hmiProject == "/" {
		return ""
	}
	built, ok := newestUnder(fsys, hmiDir)
	if !ok || built.IsZero() {
		return ""
	}
	src, srcPath := time.Time{}, ""
	consider := func(p string, t time.Time) {
		if t.After(src) {
			src, srcPath = t, p
		}
	}
	_ = fs.WalkDir(fsys, hmiProject, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != hmiProject && (p == hmiDir || d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".")) {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if info, err := d.Info(); err == nil {
			consider(p, info.ModTime())
		}
		return nil
	})
	if entries, err := fs.ReadDir(fsys, "."); err == nil {
		for _, e := range entries {
			n := e.Name()
			if e.IsDir() || !(strings.HasSuffix(n, ".mimic.json") || strings.HasSuffix(n, ".scene.json")) {
				continue
			}
			if info, err := e.Info(); err == nil {
				consider(n, info.ModTime())
			}
		}
	}
	if srcPath != "" && src.Sub(built) > hmiStaleSlack {
		return srcPath
	}
	return ""
}

// newestUnder is the latest file modification time under dir; ok is
// false when dir can't be walked.
func newestUnder(fsys fs.FS, dir string) (time.Time, bool) {
	var newest time.Time
	err := fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		return nil
	})
	return newest, err == nil
}
