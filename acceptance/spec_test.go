package acceptance_test

// The format's own diagnostics. A test file is authored by hand, so what
// it says when you get it wrong is part of the product surface.

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/acceptance"
)

func loadSrc(t *testing.T, src string) (*acceptance.Suite, error) {
	t.Helper()
	fsys := fstest.MapFS{"x_test.yaml": &fstest.MapFile{Data: []byte(src)}}
	return acceptance.LoadSuite(fsys, "x_test.yaml")
}

func TestSpecRejectsMistakes(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want string
	}{{
		name: "unknown key",
		src:  "tests:\n  - name: a\n    scanz: 1\n    expect: {X: true}\n",
		want: "scanz",
	}, {
		name: "two time keys",
		src:  "tests:\n  - name: a\n    scans: 1\n    advance: 2s\n    expect: {X: true}\n",
		want: "one of scans/advance/until",
	}, {
		name: "hold without until",
		src:  "tests:\n  - name: a\n    advance: 2s\n    hold: 1s\n    expect: {X: true}\n",
		want: "only means something with `until`",
	}, {
		name: "until without expect",
		src:  "tests:\n  - name: a\n    until: 2s\n",
		want: "needs an `expect`",
	}, {
		name: "steps and inline both",
		src:  "tests:\n  - name: a\n    scans: 1\n    steps:\n      - scans: 1\n        expect: {X: true}\n",
		want: "not both",
	}, {
		name: "no name",
		src:  "tests:\n  - scans: 1\n    expect: {X: true}\n",
		want: "needs a name",
	}, {
		name: "duplicate name",
		src:  "tests:\n  - {name: a, scans: 1, expect: {X: true}}\n  - {name: a, scans: 1, expect: {X: true}}\n",
		want: "duplicate test name",
	}, {
		name: "empty matcher",
		src:  "tests:\n  - name: a\n    scans: 1\n    expect: {X: {}}\n",
		want: "empty matcher",
	}, {
		name: "two comparisons in one matcher",
		src:  "tests:\n  - name: a\n    scans: 1\n    expect: {X: {gt: 1, lt: 5}}\n",
		want: "one comparison",
	}, {
		name: "tol without near",
		src:  "tests:\n  - name: a\n    scans: 1\n    expect: {X: {gt: 1, tol: 0.5}}\n",
		want: "`tol` belongs with `near`",
	}, {
		name: "bad duration",
		src:  "tests:\n  - name: a\n    advance: soon\n    expect: {X: true}\n",
		want: "bad duration",
	}, {
		name: "no tests",
		src:  "tolerance: 0.5\n",
		want: "no tests declared",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadSrc(t, tc.src)
			if err == nil {
				t.Fatal("want an error, got none")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// Both expectation shapes, and the mixed sequence, parse into terms.
func TestExpectShapes(t *testing.T) {
	s, err := loadSrc(t, `
tests:
  - name: mapping
    scans: 1
    expect: { A: true, B: { near: 1.0, tol: 0.1 } }
  - name: expression
    scans: 1
    expect: ABS(A - B) < 0.5
  - name: mixed
    scans: 1
    expect:
      - { A: true }
      - A > B
`)
	if err != nil {
		t.Fatal(err)
	}
	want := []int{2, 1, 2}
	for i, tc := range s.Tests {
		if got := len(tc.Expect.Terms); got != want[i] {
			t.Errorf("test %q: %d terms, want %d", tc.Name, got, want[i])
		}
	}
	if s.Tests[1].Expect.Terms[0].Expr == "" {
		t.Error("a scalar expectation should parse as an ST expression")
	}
	if s.Tests[2].Expect.Terms[0].Tag != "A" {
		t.Error("a mapping inside a sequence should parse as a tag matcher")
	}
}

// The single-step shorthand and the explicit steps form describe the same
// thing, so a reader can use whichever suits the test.
func TestShorthandIsOneStep(t *testing.T) {
	s, err := loadSrc(t, "tests:\n  - name: a\n    given: {X: 1.0}\n    advance: 2s\n    expect: {Y: true}\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Tests[0].Steps) != 0 {
		t.Fatal("shorthand should not populate Steps directly")
	}
	if s.Tests[0].Advance == nil || s.Tests[0].Expect == nil {
		t.Fatal("shorthand keys should be readable on the test")
	}
}
