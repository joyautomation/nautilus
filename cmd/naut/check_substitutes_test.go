package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #707: `naut check` holds a substitutes: rule's value to its tag's type as
// a write is held, counts what is substitutable, and refuses a rule that
// matches no input — each at the exit code a CI gate reads.
func TestCheckSubstitutes(t *testing.T) {
	const prog = "PROGRAM p\nVAR_EXTERNAL Level : REAL; Out : REAL; Level__Substituted : BOOL; END_VAR\nOut := Level;\nEND_PROGRAM\n"
	for _, c := range []struct {
		name, rule string
		code       int
		want       string
	}{
		{"a good rule", "{match: Level, substitute: 90.0}", 0, "1 substitutable inputs"},
		{"a value the type cannot hold", `{match: Level, substitute: "high"}`, 1, "substitutes.rules[0]: the substitute for Level"},
		{"a member path on a scalar tag", "{match: Level.Foo, substitute: 1.0}", 1, "substitutes.rules[0]: the substitute for Level.Foo"},
		{"a rule shadowed by an earlier glob", "{match: \"Lev*\", substitute: 1.0}\n    - {match: Level, substitute: 2.0}", 1, "shadowed by rules[0]"},
		{"a rule that matches nothing", "{match: Nope, substitute: 1.0}", 1, `match "Nope" matches no input tag`},
	} {
		dir := t.TempDir()
		manifest := "name: subs\ntasks:\n  - program: p.st\n    scan: 100ms\ntags:\n" +
			"  - {name: Level, role: input, type: REAL, init: 0.0}\n  - {name: Out, role: output, init: 0.0}\n" +
			"substitutes:\n  rules:\n    - " + c.rule + "\n"
		for n, body := range map[string]string{"nautilus.yaml": manifest, "p.st": prog} {
			if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		out, code := capture(t, func() int { return runCheck([]string{dir}) })
		if code != c.code || !strings.Contains(out, c.want) {
			t.Errorf("%s: naut check = %d\n%s\nwant %d and %q", c.name, code, out, c.code, c.want)
		}
	}
}
