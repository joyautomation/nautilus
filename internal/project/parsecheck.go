package project

import (
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/joyautomation/nautilus/internal/stproject"
	"github.com/joyautomation/nautilus/lang/st"
)

// checkSourcesParse parses every plain-ST source file on its own and
// reports the first failure as path:line:col, as `naut check` does. Without
// it a file that does not parse contributes no TYPEs, and the first thing
// to notice is a tag typed with one of them: "this project declares no
// TYPE", naming the wrong file (#427). Graphical sources are skipped: their
// transpiler reports its own errors.
func checkSourcesParse(fsys fs.FS, m Manifest) error {
	parse := func(name, src string) error {
		if _, err := st.Parse(src); err != nil {
			pos, ok := st.ParseErrorPos(err)
			if !ok {
				pos = st.Pos{Line: 1, Col: 1}
			}
			return fmt.Errorf("%s:%d:%d: %w", name, pos.Line, pos.Col, err)
		}
		return nil
	}
	libs, err := stproject.Libraries(fsys, nil)
	if err != nil {
		return err
	}
	for _, l := range libs {
		if strings.EqualFold(path.Ext(l.Path), ".st") {
			if err := parse(l.Path, l.ST); err != nil {
				return err
			}
		}
	}
	for _, t := range m.Tasks {
		if t.Program == "" || !strings.EqualFold(path.Ext(t.Program), ".st") {
			continue
		}
		raw, err := fs.ReadFile(fsys, path.Clean(t.Program))
		if err != nil {
			continue // readProgram reports it
		}
		if err := parse(path.Clean(t.Program), string(raw)); err != nil {
			return err
		}
	}
	return nil
}
