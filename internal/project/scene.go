package project

import (
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/internal/scene"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// SceneTagInfo is what the scene checker and generator need about every
// declared tag: its UDT, if any, resolved the way alarmTagInfo resolves it
// — from the seeded value first, then the compiled TYPE table.
func (p *Project) SceneTagInfo(rt *runtime.Runtime) []scene.TagInfo {
	types := rt.Types()
	tags := rt.Tags()
	out := make([]scene.TagInfo, 0, len(p.Runtime.Tags))
	for _, d := range p.Runtime.Tags {
		info := scene.TagInfo{Name: d.Name, TypeName: d.Type, Desc: d.Meta.Desc}
		if v, err := tags.ReadGlobal(d.Name); err == nil && v.Kind == ir.TypeStruct {
			info.Struct = v.Struct
		} else if t, ok := types[d.Type]; ok && d.Type != "" && t.Kind == ir.TypeStruct {
			info.Struct = t.Struct
		}
		if info.TypeName == "" && info.Struct != nil {
			info.TypeName = info.Struct.Name
		}
		out = append(out, info)
	}
	return out
}

// SceneFiles lists the *.scene.json files at the project root, sorted —
// the convention hmi-3d's README sets (next to nautilus.yaml and the
// *.mimic.json, one `../` from the SvelteKit app).
func SceneFiles(fsys fs.FS) ([]string, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".scene.json") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// assetExists resolves a scene's asset path (a URL path from the app
// root, docs/design/spatial-hmi.md §3c) the way the app will serve it:
// next to the scene file at the project root, then under the HMI app's
// `static/` (the sibling of the manifest's `server.hmi` build directory,
// the SvelteKit layout every example uses), then in that build.
func (p *Project) assetExists(fsys fs.FS) func(string) bool {
	roots := []string{"."}
	if p.HMIDir != "" {
		build := path.Clean(p.HMIDir)
		roots = append(roots, path.Join(path.Dir(build), "static"), build)
	}
	return func(asset string) bool {
		for _, r := range roots {
			if st, err := fs.Stat(fsys, path.Join(r, asset)); err == nil && !st.IsDir() {
				return true
			}
		}
		return false
	}
}

// SceneReport is one checked scene file.
type SceneReport struct {
	File  string
	Nodes int
	Pipes int
}

// CheckScenes is `naut check`'s scene pass: every *.scene.json at the root
// is parsed and held to the project's tags and UDTs (scene.Check). Errors
// and warnings carry the file and the JSON path. Offline, like alarms.
func (p *Project) CheckScenes(fsys fs.FS, rt *runtime.Runtime) (reports []SceneReport, errs, warns []string, err error) {
	files, err := SceneFiles(fsys)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(files) == 0 {
		return nil, nil, nil, nil
	}
	tags := p.SceneTagInfo(rt)
	for _, f := range files {
		data, rerr := fs.ReadFile(fsys, f)
		if rerr != nil {
			errs = append(errs, fmt.Sprintf("scene %s: %s", f, rerr))
			continue
		}
		doc, perr := scene.Parse(data)
		if perr != nil {
			errs = append(errs, fmt.Sprintf("scene %s: %s", f, perr))
			continue
		}
		e, w := scene.Check(doc, tags)
		e = append(e, scene.CheckAssets(doc, p.assetExists(fsys))...)
		for _, m := range e {
			errs = append(errs, fmt.Sprintf("scene %s %s", f, m))
		}
		for _, m := range w {
			warns = append(warns, fmt.Sprintf("scene %s %s", f, m))
		}
		reports = append(reports, SceneReport{File: f, Nodes: len(doc.Nodes), Pipes: len(doc.Pipes)})
	}
	return reports, errs, warns, nil
}
