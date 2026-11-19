package io

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Every Quality has its own token, the token parses back, and only Good is
// good. A value this build does not know renders and parses as Bad, never
// Good (#707: a quality added without updating a mapping fails closed).
func TestEveryQualityHasAToken(t *testing.T) {
	seen := map[string]bool{}
	for _, q := range AllQualities() {
		s := q.String()
		if seen[s] {
			t.Errorf("%d shares the token %q", q, s)
		}
		seen[s] = true
		if p, ok := ParseQuality(s); !ok || p != q {
			t.Errorf("%q parses to %v, %v; want %d", s, p, ok, q)
		}
		if q.IsGood() != (q == Good) {
			t.Errorf("%q: IsGood %v", s, q.IsGood())
		}
	}
	unknown := Quality(len(AllQualities()))
	if s := unknown.String(); s == "good" {
		t.Errorf("an unknown quality renders %q: it must not read as good", s)
	}
	if q, ok := ParseQuality("excellent"); ok || q.IsGood() {
		t.Errorf("an unknown token parses to %v, %v: it must not read as good", q, ok)
	}
}

// AllQualities is every constant: the one after the last is unknown.
func TestAllQualitiesIsComplete(t *testing.T) {
	all := AllQualities()
	for i, q := range all {
		if int(q) != i {
			t.Fatalf("AllQualities()[%d] = %d: keep it in declaration order", i, q)
		}
	}
	if s := Quality(len(all)).String(); s != "bad" {
		t.Fatalf("Quality(%d) renders %q: a constant was added without a token, or AllQualities misses it", len(all), s)
	}
}

// The HMI kit's Quality union (hmi/src/lib/types.ts) carries exactly the
// tokens Go sends, so a value added here cannot reach a screen that has no
// case for it.
func TestTheHMIKitQualityUnionMatches(t *testing.T) {
	src, err := os.ReadFile("../hmi/src/lib/types.ts")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`export type Quality = ([^;]+);`).FindSubmatch(src)
	if m == nil {
		t.Fatal("no `export type Quality = …;` in hmi/src/lib/types.ts")
	}
	var ts []string
	for _, part := range strings.Split(string(m[1]), "|") {
		ts = append(ts, strings.Trim(strings.TrimSpace(part), `'"`))
	}
	var gotok []string
	for _, q := range AllQualities() {
		gotok = append(gotok, q.String())
	}
	slices.Sort(ts)
	slices.Sort(gotok)
	if !slices.Equal(ts, gotok) {
		t.Errorf("the HMI kit's Quality union is %v, Go sends %v", ts, gotok)
	}
}
