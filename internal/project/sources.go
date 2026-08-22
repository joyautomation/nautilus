package project

import (
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/joyautomation/nautilus/internal/stproject"
	"github.com/joyautomation/nautilus/runtime"
)

// Sources returns every task's composed program source — libraries joined
// ahead of the program file, exactly as Load hands them to runtime.New —
// keyed by the task names Load would assign (runtime.MainTaskName for the
// first task, then the manifest's name or the program file's basename).
//
// It exists so program activation composes the past the way boot composes
// the present: pointed at a snapshot FS of the project at some commit, it
// yields per-task sources the server can warm-swap 1:1 against the running
// resource. Only sources are read — no driver construction, no compile —
// so it also works on manifests whose commit predates a driver the running
// binary has.
func Sources(fsys fs.FS, name string) (map[string]string, error) {
	m, err := ReadManifest(fsys, name)
	if err != nil {
		return nil, err
	}
	if len(m.Tasks) == 0 {
		return nil, fmt.Errorf("%s: at least one task (a program file) is required", ManifestName)
	}
	libs, err := libraries(fsys)
	if err != nil {
		return nil, err
	}
	compose := func(src string) string {
		if len(libs) > 0 {
			return stproject.Join(libs, src)
		}
		return src
	}

	out := make(map[string]string, len(m.Tasks))
	for i, t := range m.Tasks {
		if t.Program == "" {
			return nil, fmt.Errorf("%s: every task needs a program file", ManifestName)
		}
		src, err := fs.ReadFile(fsys, path.Clean(t.Program))
		if err != nil {
			return nil, fmt.Errorf("task program %q: %w", t.Program, err)
		}
		if !hasProgramDecl(src) {
			return nil, fmt.Errorf("%s has no PROGRAM declaration", t.Program)
		}
		taskName := runtime.MainTaskName
		if i > 0 {
			taskName = t.Name
			if taskName == "" {
				taskName = strings.TrimSuffix(path.Base(t.Program), path.Ext(t.Program))
			}
		}
		out[taskName] = compose(string(src))
	}
	return out, nil
}
