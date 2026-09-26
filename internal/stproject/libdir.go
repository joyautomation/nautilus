package stproject

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// LibDir is the project subdirectory whose IEC files are all libraries.
//
// A project's library set is every PROGRAM-less `.st`, `.ld` or `.fbd` file
// in the project root PLUS every such file under lib/, at any depth. A file
// under lib/ that declares a PROGRAM is an error where it matters (`naut
// check`, `run`, `build`, `test`): programs belong in the root, named by a
// task. Every other subdirectory (hmi/, tags/, deploy/, node_modules/…) is
// still ignored, so a project without lib/ composes exactly as it always has.
const LibDir = "lib"

// InLibDir reports whether a project-relative, slash-separated path lies
// under lib/.
func InLibDir(rel string) bool {
	return strings.HasPrefix(path.Clean(rel), LibDir+"/")
}

// LibraryPaths lists a project's library CANDIDATES — root-level and lib/
// `.st`, `.ld` and `.fbd` files — as project-relative slash paths, split
// into the two composition tiers (ST first, then graphical) and sorted by
// path within each tier, so composition is deterministic and a rebuild diffs
// clean. Whether a candidate is a library (no PROGRAM) is the caller's call:
// a root file with a PROGRAM is a program, one under lib/ is an error.
//
// For a project with no lib/ the result is exactly the old root-only list,
// in the old order.
func LibraryPaths(fsys fs.FS) (stPaths, gPaths []string, err error) {
	add := func(rel string) {
		switch {
		case strings.EqualFold(path.Ext(rel), ".st"):
			stPaths = append(stPaths, rel)
		case IsGraphicalLibrary(rel):
			gPaths = append(gPaths, rel)
		}
	}
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, nil, err
	}
	hasLib := false
	for _, e := range entries {
		if e.IsDir() {
			hasLib = hasLib || e.Name() == LibDir
			continue
		}
		add(e.Name())
	}
	if hasLib {
		err = fs.WalkDir(fsys, LibDir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if p != LibDir && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
					return fs.SkipDir
				}
				return nil
			}
			add(p)
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, nil, err
		}
	}
	sort.Strings(stPaths)
	sort.Strings(gPaths)
	return stPaths, gPaths, nil
}

// ProjectRoot is the directory whose library set a file compiles against:
// the file's own directory, unless the file lives under the lib/ directory of
// a manifest project — then it is the project root (lib/'s parent), so a
// block in lib/ sees the root's libraries and the rest of lib/ exactly as a
// root program sees them.
//
// The project is the nearest ancestor holding a nautilus.yaml — the same
// question the language server's manifest lookup answers. Without one, a
// file compiles against its own directory, as it always has.
func ProjectRoot(file string) string {
	abs, err := filepath.Abs(file)
	if err != nil {
		return filepath.Dir(file)
	}
	dir := filepath.Dir(abs)
	for d, i := dir, 0; i < 32; i++ { // bounded: a symlink cycle must not hang
		if st, err := os.Stat(filepath.Join(d, "nautilus.yaml")); err == nil && !st.IsDir() {
			if rel, err := filepath.Rel(d, abs); err == nil && InLibDir(filepath.ToSlash(rel)) {
				return d
			}
			return dir
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return dir
}
