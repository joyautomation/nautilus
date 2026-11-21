//go:build !js

package project

import (
	"io/fs"
	"path"
)

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
	paths := []string{name}
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
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			continue // a named file that is not there: the form shows the row, not the file
		}
		st.Files[p] = string(b)
		if _, ok := over[p]; ok {
			st.Differs = append(st.Differs, p)
		}
	}
	return st, nil
}
