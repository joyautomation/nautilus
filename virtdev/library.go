package virtdev

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// builtin is the profile library that ships in naut: profiles/<id>.yaml,
// with any simulator source beside it.
//
//go:embed profiles
var builtin embed.FS

// Builtin lists the built-in profiles, sorted by id.
func Builtin() ([]*Profile, error) {
	ents, err := fs.ReadDir(builtin, "profiles")
	if err != nil {
		return nil, err
	}
	var out []*Profile
	for _, e := range ents {
		if e.IsDir() || path.Ext(e.Name()) != ".yaml" {
			continue
		}
		p, err := loadBuiltin(strings.TrimSuffix(e.Name(), ".yaml"))
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func loadBuiltin(id string) (*Profile, error) {
	name := path.Join("profiles", id+".yaml")
	raw, err := fs.ReadFile(builtin, name)
	if err != nil {
		return nil, err
	}
	p, err := ParseProfile(raw, "built-in profile "+id)
	if err != nil {
		return nil, err
	}
	if p.ID != id {
		return nil, fmt.Errorf("built-in profile %s.yaml declares profile %q", id, p.ID)
	}
	return p, nil
}

// ResolveProfile finds a profile by what devices.yaml wrote: a path ending
// in .yaml (relative to dir) is a project's own profile; anything else is
// a built-in id.
func ResolveProfile(dir, ref string) (*Profile, error) {
	if strings.HasSuffix(ref, ".yaml") || strings.HasSuffix(ref, ".yml") {
		file := ref
		if !filepath.IsAbs(file) {
			file = filepath.Join(dir, file)
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		p, err := ParseProfile(raw, ref)
		if err != nil {
			return nil, err
		}
		p.dir = filepath.Dir(file)
		return p, nil
	}
	p, err := loadBuiltin(ref)
	if err != nil {
		if errorsIsNotExist(err) {
			return nil, fmt.Errorf("no built-in profile %q (naut devices profiles lists them; a project's own profile is a path ending in .yaml)", ref)
		}
		return nil, err
	}
	return p, nil
}

// simulatorSource reads a profile's simulator ST.
func (p *Profile) simulatorSource() (string, error) {
	s := p.Simulator
	if p.dir == "" {
		raw, err := fs.ReadFile(builtin, path.Join("profiles", s.Source))
		return string(raw), err
	}
	raw, err := os.ReadFile(filepath.Join(p.dir, s.Source))
	return string(raw), err
}

func errorsIsNotExist(err error) bool {
	return err != nil && (os.IsNotExist(err) || strings.Contains(err.Error(), "file does not exist"))
}
