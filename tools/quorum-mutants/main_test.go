package main

import (
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const (
	pass  = `{"Action":"pass","Package":"p","Test":"TestA"}`
	failA = `{"Action":"fail","Package":"p","Test":"TestA"}`
	failB = `{"Action":"fail","Package":"p","Test":"TestB"}`
	subA  = `{"Action":"fail","Package":"p","Test":"TestA/case"}`
	pkgF  = `{"Action":"fail","Package":"p"}`
)

func stream(lines ...string) []byte { return []byte(strings.Join(lines, "\n") + "\n") }

// The verdict table: a mutation is caught only when every expected test
// went red and nothing was broken; a build or setup failure, or a package
// failing with no test failing, is never a catch.
func TestVerdict(t *testing.T) {
	m := mutation{name: "x", expect: []string{"TestA", "TestB"}}
	for _, c := range []struct {
		name   string
		out    []byte
		caught bool
		why    string
	}{
		{"both red", stream(subA, failA, failB, pkgF), true, "caught"},
		{"one survived", stream(failA, `{"Action":"pass","Package":"p","Test":"TestB"}`, pkgF), false, "SURVIVED: still green: TestB"},
		{"nothing red", stream(pass), false, "SURVIVED"},
		{"build failed", stream(`{"Action":"output","Package":"p","Output":"FAIL\tp [build failed]\n"}`, pkgF), false, "BROKEN"},
		{"build failed, plain text", stream("# p", "./x.go:1: undefined: y", "FAIL\tp [build failed]"), false, "BROKEN"},
		{"setup failed (unknown QUORUM_MUT panics in init)", stream(`{"Action":"output","Package":"p","Output":"FAIL\tp [setup failed]\n"}`, pkgF), false, "BROKEN"},
		{"package red, no test red (a panic)", stream(pkgF), false, "BROKEN"},
		{"a test panicked: caught, but said so", stream(`{"Action":"output","Package":"p","Test":"TestA","Output":"panic: index out of range\n"}`, failA, failB, pkgF), true, "caught (2 tests red); a test PANICKED"},
	} {
		got, why := verdict(m, parse(c.out))
		if got != c.caught || !strings.HasPrefix(why, c.why) {
			t.Errorf("%s: verdict %v %q, want %v %q…", c.name, got, why, c.caught, c.why)
		}
	}
}

func TestBaseline(t *testing.T) {
	if ok, why := baseline(parse(stream(pass))); !ok {
		t.Fatalf("green baseline judged %q", why)
	}
	if ok, _ := baseline(parse(stream(failA, pkgF))); ok {
		t.Fatal("a red baseline judged green")
	}
	if ok, _ := baseline(parse(stream("FAIL\tp [build failed]"))); ok {
		t.Fatal("a baseline that didn't build judged green")
	}
}

// A red baseline says why: the failing test's report lines, not the log
// lines it printed along the way, and only the failing test's (#1120).
func TestARedBaselineSaysWhy(t *testing.T) {
	out := func(test, line string) string {
		return `{"Action":"output","Package":"p","Test":"` + test + `","Output":` + strconv.Quote(line) + `}`
	}
	r := parse(stream(
		out("TestA", "=== RUN   TestA\n"),
		out("TestA", "2026/11/22 22:57:00 INFO redundancy: this controller leads\n"),
		out("TestA", "    loopback_test.go:794: b did not hold its lease steadily\n"),
		out("TestA", "--- FAIL: TestA (3.59s)\n"),
		failA,
		out("TestB", "    other_test.go:1: a passing test's log\n"),
		pass,
		pkgF,
	))
	why := r.why()
	if !strings.Contains(why, "loopback_test.go:794: b did not hold its lease steadily") || !strings.Contains(why, "--- FAIL: TestA") {
		t.Fatalf("a red baseline's why: %q, want TestA's report lines", why)
	}
	if strings.Contains(why, "INFO") || strings.Contains(why, "other_test.go") {
		t.Fatalf("a red baseline's why carries a log line or another test's: %q", why)
	}
	long := []string{}
	for i := range 3 * tailLines {
		long = append(long, out("TestB", "    x_test.go:"+strconv.Itoa(i)+": line\n"))
	}
	if n := len(parse(stream(append(long, failB, pkgF)...)).said["TestB"]); n != tailLines {
		t.Fatalf("kept %d report lines, want the last %d", n, tailLines)
	}
}

// Every mutation the quorum package defines has a row, and every row names
// a defined mutation (quorum/mut_on.go's list, read as text so this test
// needn't build with the tag).
func TestTableMatchesThePackage(t *testing.T) {
	src, err := readFile("../../quorum/mut_on.go")
	if err != nil {
		t.Fatal(err)
	}
	var defined []string
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, `"`) && strings.Contains(line, `",`) {
			defined = append(defined, line[1:strings.Index(line[1:], `"`)+1])
		}
	}
	var rows []string
	for _, m := range table {
		rows = append(rows, m.name)
		if len(m.expect) == 0 {
			t.Errorf("%s names no test", m.name)
		}
	}
	slices.Sort(defined)
	slices.Sort(rows)
	if !slices.Equal(defined, rows) {
		t.Fatalf("mut_on.go defines %v\nthe table has %v", defined, rows)
	}
}

func readFile(p string) (string, error) {
	b, err := os.ReadFile(p)
	return string(b), err
}

// Every row runs only its own tests (#1096): a row without rowPkgs once
// ran the whole quorum tree, 100 s or more, and the nightly timed out.
func TestEveryRowRunsOnlyItsTests(t *testing.T) {
	for _, m := range table {
		a := m.args()
		if len(a) < 3 || a[0] != "-run" {
			t.Fatalf("%s runs %v, want -run first", m.name, a)
		}
		if want := "^(" + strings.Join(m.expect, "|") + ")$"; a[1] != want {
			t.Fatalf("%s: -run %q, want %q", m.name, a[1], want)
		}
		if !slices.Equal(a[2:], m.pkgs()) {
			t.Fatalf("%s: packages %v, want %v", m.name, a[2:], m.pkgs())
		}
	}
}

// The shards split the table: every row in exactly one shard, for any
// leg count.
func TestShardsCoverTheTableOnce(t *testing.T) {
	for n := 1; n <= 9; n++ {
		seen := map[string]int{}
		for k := range n {
			for _, m := range shard(table, k, n) {
				seen[m.name]++
			}
		}
		for _, m := range table {
			if seen[m.name] != 1 {
				t.Fatalf("n=%d: %s in %d shards", n, m.name, seen[m.name])
			}
		}
		if len(seen) != len(table) {
			t.Fatalf("n=%d: %d rows, table has %d", n, len(seen), len(table))
		}
	}
}

// The baselines run every judged row's tests in its packages, green
// first, so a test already red is never counted as a catch; one run per
// package set.
func TestBaselinesCoverEveryRowsTests(t *testing.T) {
	rows := shard(table, 1, 3)
	runs := baselines(rows)
	sets := map[string]bool{}
	for _, a := range runs {
		k := strings.Join(a[2:], " ")
		if sets[k] {
			t.Fatalf("two baseline runs for %s", k)
		}
		sets[k] = true
	}
	for _, m := range rows {
		found := false
		for _, a := range runs {
			if !slices.Equal(a[2:], m.pkgs()) {
				continue
			}
			names := strings.Split(strings.TrimSuffix(strings.TrimPrefix(a[1], "^("), ")$"), "|")
			for _, e := range m.expect {
				if !slices.Contains(names, e) {
					t.Fatalf("%s: the baseline for %v lacks %s", m.name, m.pkgs(), e)
				}
			}
			found = true
		}
		if !found {
			t.Fatalf("%s: no baseline runs %v", m.name, m.pkgs())
		}
	}
}

func TestParseShard(t *testing.T) {
	for s, ok := range map[string]bool{"0/1": true, "3/8": true, "7/8": true, "8/8": false, "-1/8": false, "0/0": false, "x": false} {
		if _, _, err := parseShard(s); (err == nil) != ok {
			t.Errorf("parseShard(%q): err %v, want ok %v", s, err, ok)
		}
	}
}
