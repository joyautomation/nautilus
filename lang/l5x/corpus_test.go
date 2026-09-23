package l5x

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/st"
)

// The committed fixtures are small and generic on purpose. The reader was
// developed against a much larger corpus — around 60 real exports, 30 MB,
// most of it client work that cannot be committed — and the shapes that
// corpus taught (IEC keywords as UDT member names, expression operands,
// five-deep branches, hidden bit hosts) are covered by the fixtures above.
//
// This test runs the whole reader over a directory of real exports so the
// corpus stays usable without ever entering the repo:
//
//	NAUTILUS_L5X_CORPUS=~/some/dir go test ./lang/l5x/
//
// It asserts the three things that must hold for every file: it parses,
// every rung parses, and the generated ST compiles.
func TestCorpus(t *testing.T) {
	dir := os.Getenv("NAUTILUS_L5X_CORPUS")
	if dir == "" {
		t.Skip("set NAUTILUS_L5X_CORPUS to a directory of .L5X exports")
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var files, routines, rungs int
	for _, e := range ents {
		if !strings.EqualFold(filepath.Ext(e.Name()), ".L5X") {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			f, err := ParseFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			files++

			src, _, err := Types(f, TypesOptions{})
			if err != nil {
				t.Fatalf("types: %v", err)
			}
			if src != "" {
				prog, err := st.Parse(src)
				if err != nil {
					t.Fatalf("generated ST does not parse: %v", err)
				}
				if _, err := st.Lower(prog); err != nil {
					t.Fatalf("generated ST does not compile: %v", err)
				}
			}
			if _, err := TagsYAML(f, TagsOptions{Scope: "*"}); err != nil {
				t.Fatalf("tags: %v", err)
			}

			for _, p := range f.Controller.Programs {
				for _, r := range p.Routines {
					routines++
					rungs += len(r.Rungs)
					for _, rg := range r.Rungs {
						if _, err := ParseRung(rg.Text); err != nil {
							t.Errorf("%s/%s rung %d: %v\n  %s", p.Name, r.Name, rg.Number, err, rg.Text)
						}
					}
				}
			}
			if _, err := Ladder(f, LadderOptions{AOIs: true}); err != nil && !strings.Contains(err.Error(), "no RLL routines") {
				t.Fatalf("ladder: %v", err)
			}
		})
	}
	t.Logf("%d files, %d routines, %d rungs", files, routines, rungs)
}
