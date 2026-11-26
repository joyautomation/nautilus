package devprofile

import (
	"strings"
	"testing"
)

const lintBase = `
profile: x
title: X
protocol: modbus
docs:
  - { id: m, title: manual, url: "https://example.invalid/m.pdf" }
points:
`

func lintOf(t *testing.T, rows string) []string {
	t.Helper()
	p, err := ParseProfile([]byte(lintBase+rows), "x.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range p.Lint() {
		out = append(out, f.String())
	}
	return out
}

func wantOne(t *testing.T, got []string, sub string) {
	t.Helper()
	if sub == "" {
		if len(got) != 0 {
			t.Fatalf("want a clean lint, got %v", got)
		}
		return
	}
	n := 0
	for _, g := range got {
		if strings.Contains(g, sub) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want exactly one finding containing %q, got %v", sub, got)
	}
}

func TestLint(t *testing.T) {
	for _, tc := range []struct{ name, rows, want string }{
		{"clean", `  - { name: A, type: UINT, table: holding, address: 1, doc: "m p. 1" }
`, ""},
		{"two on one address", `  - { name: A, type: UINT, table: holding, address: 1, doc: "m p. 1" }
  - { name: B, type: UINT, table: holding, address: 1, doc: "m p. 2" }
`, "holding address 1 is also A"},
		{"a 32-bit point covers the next address", `  - { name: A, type: REAL, table: holding, address: 1, doc: "m p. 1" }
  - { name: B, type: UINT, table: holding, address: 2, doc: "m p. 2" }
`, "holding address 2 is also A"},
		{"same address, other table", `  - { name: A, type: UINT, table: holding, address: 1, doc: "m p. 1" }
  - { name: B, type: UINT, table: input, address: 1, doc: "m p. 2" }
`, ""},
		{"a bit beside its register", `  - { name: W, type: WORD, table: holding, address: 1, doc: "m p. 1" }
  - { name: B0, type: BOOL, table: holding, address: 1, format: "bit:0", doc: "m p. 1" }
  - { name: B1, type: BOOL, table: holding, address: 1, format: "bit:1", doc: "m p. 1" }
`, ""},
		{"one bit twice", `  - { name: B0, type: BOOL, table: holding, address: 1, format: "bit:0", doc: "m p. 1" }
  - { name: B0b, type: BOOL, table: holding, address: 1, format: "bit:0", doc: "m p. 1" }
`, "holding address 1 is also B0"},
		{"citation without a place", `  - { name: A, type: UINT, table: holding, address: 1, doc: "m" }
`, "names the document but not the page"},
		{"unverified rows are counted", `  - { name: A, type: UINT, table: holding, address: 1, doc: "m p. 1" }
  - { name: B, type: UINT, table: holding, address: 2, unverified: true }
`, "1 row(s) are unverified"},
	} {
		t.Run(tc.name, func(t *testing.T) { wantOne(t, lintOf(t, tc.rows), tc.want) })
	}
}

func TestLintDocs(t *testing.T) {
	src := strings.Replace(lintBase, `docs:
`, `docs:
  - { id: unused, title: Never cited, url: "https://example.invalid/u.pdf" }
  - { id: nowhere, title: No way to find it }
`, 1) + `  - { name: A, type: UINT, table: holding, address: 1, doc: "m p. 1" }
  - { name: B, type: UINT, table: holding, address: 2, doc: "nowhere p. 1" }
`
	p, err := ParseProfile([]byte(src), "x.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range p.Lint() {
		got = append(got, f.String())
	}
	wantOne(t, got, `document "unused" is listed under docs but nothing cites it`)
	wantOne(t, got, `document "nowhere" has neither a url`)
}

func TestLintOPCUANodeTwice(t *testing.T) {
	src := `
profile: x
title: X
protocol: opcua
opcua: { namespaces: ["urn:x"] }
docs:
  - { id: m, title: manual, url: "https://example.invalid/m.pdf" }
points:
  - { name: A, type: UINT, node-id: "ns=1;s=A", path: "X/A", doc: "m p. 1" }
  - { name: B, type: UINT, node-id: "ns=1;s=A", path: "X/A", doc: "m p. 1" }
`
	p, err := ParseProfile([]byte(src), "x.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range p.Lint() {
		got = append(got, f.String())
	}
	wantOne(t, got, "node ns=1;s=A is also A")
}

// The built-in library must lint with no errors: it is what lint is
// teaching a profile author to match.
func TestBuiltinLibraryLintsWithoutErrors(t *testing.T) {
	ps, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		for _, f := range p.Lint() {
			if f.Error {
				t.Errorf("%s: %s", p.ID, f)
			}
		}
	}
}
