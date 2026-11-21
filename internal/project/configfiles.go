//go:build !js

package project

import (
	"io/fs"
	"path"
	"path/filepath"
	"strings"
)

// configFilePath is a path the config screens may be served: a clean
// relative slash path inside the project, of a YAML data file, with no
// hidden component (the rule naut pull --io applies to what it writes).
func configFilePath(p string) bool {
	if p == "" || path.Clean(p) != p || !filepath.IsLocal(filepath.FromSlash(p)) {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}
	switch strings.ToLower(path.Ext(p)) {
	case ".yaml", ".yml":
		return true
	}
	return false
}

// viaSymlink reports whether any component of p is a symbolic link in
// fsys: fs.ReadDir entries describe a link, not what it points at.
func viaSymlink(fsys fs.FS, p string) bool {
	dir := "."
	for _, part := range strings.Split(p, "/") {
		entries, err := fs.ReadDir(fsys, dir)
		if err != nil {
			return true
		}
		found := false
		for _, e := range entries {
			if e.Name() == part {
				if e.Type()&fs.ModeSymlink != 0 {
					return true
				}
				found = true
				break
			}
		}
		if !found {
			return true
		}
		dir = path.Join(dir, part)
	}
	return false
}

// ConfigFilesState is the manifest as the config screens edit it: the
// manifest file and the files its drivers name, as what runs now (an I/O
// apply's files laid over what the controller booted from).
type ConfigFilesState struct {
	Leading  bool
	Manifest string
	Files    map[string]string
	// Differs is the paths whose text is an apply's, not the boot copy's.
	Differs []string
}

// ConfigFiles reads the manifest (and each driver's own manifest file) from
// the boot project with the running I/O configuration's files laid over it.
func (io *IOConfig) ConfigFiles() (ConfigFilesState, error) {
	name := io.manifest
	if name == "" {
		name = ManifestName
	}
	st := ConfigFilesState{Manifest: name, Leading: io.leads(), Files: map[string]string{}, Differs: []string{}}
	over := map[string][]byte{}
	io.mu.Lock()
	for _, k := range sortedKeys(io.running) {
		for p, b := range io.running[k].files {
			over[p] = []byte(b)
		}
	}
	io.mu.Unlock()
	fsys := OverlayFS(io.bootFS, over)
	m, err := ReadManifest(fsys, name)
	if err != nil {
		return st, err
	}
	paths := []string{name} // the manifest itself, whatever its name
	configs, err := driverConfigs(m)
	if err != nil {
		return st, err
	}
	for _, c := range configs {
		if c.Manifest != "" {
			paths = append(paths, path.Clean(c.Manifest))
		}
	}
	for _, p := range paths {
		if _, done := st.Files[p]; done {
			continue
		}
		_, applied := over[p]
		b, ok := readConfigFile(fsys, p, applied)
		if !ok {
			continue // not there, or not a file the screens may serve
		}
		st.Files[p] = b
		if _, ok := over[p]; ok {
			st.Differs = append(st.Differs, p)
		}
	}
	return st, nil
}

// readConfigFile reads p from fsys for the config screens, or says it may
// not be served: not a YAML data file inside the project, or reached
// through a symbolic link (unless an apply supplied the bytes: applied).
func readConfigFile(fsys fs.FS, p string, applied bool) (string, bool) {
	if !configFilePath(p) || (!applied && viaSymlink(fsys, p)) {
		return "", false
	}
	b, err := fs.ReadFile(fsys, p)
	if err != nil {
		return "", false
	}
	return string(b), true
}
