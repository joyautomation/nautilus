package hmibuild

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The kits depend on each other through npm ranges (hmi-3d needs
// @joyautomation/nautilus-hmi), and each range must admit the sibling's
// version in this tree. Nothing else checks it: the packs and the examples
// reach the kits by file: links, so a stale range builds here and only
// bites an npm project, which then gets a second, older copy of the kit
// nested under the one that asked for it. hmi-3d 0.4.0 was nearly released
// asking for "^0.8.0 || ^0.9.0" beside hmi 0.10.0.
func TestKitDependencyRangesAdmitTheSiblingVersions(t *testing.T) {
	root := repo(t)
	kits := map[string]string{} // package name → its version here
	dirs := []string{"hmi", "hmi-3d"}
	manifests := map[string]map[string]any{}
	for _, d := range dirs {
		raw, err := os.ReadFile(filepath.Join(root, d, "package.json"))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("%s/package.json: %v", d, err)
		}
		manifests[d] = m
		kits[m["name"].(string)] = m["version"].(string)
	}
	checked := 0
	for _, d := range dirs {
		for _, field := range []string{"dependencies", "peerDependencies"} {
			deps, _ := manifests[d][field].(map[string]any)
			for name, r := range deps {
				version, sibling := kits[name]
				if !sibling {
					continue
				}
				checked++
				ok, err := caretRangeAdmits(r.(string), version)
				if err != nil {
					t.Errorf("%s/package.json %s %s: %v", d, field, name, err)
					continue
				}
				if !ok {
					t.Errorf("%s/package.json %s asks for %s %q, which does not admit the %s in this tree: "+
						"an npm project would nest an older copy of it", d, field, name, r, version)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no kit depends on another: the check found nothing to check")
	}
}

// caretRangeAdmits reports whether version satisfies a range made of
// caret terms joined by "||" ("^0.8.0 || ^0.9.0"), npm's rule for each:
// the leftmost non-zero part is fixed, the rest may rise. Any other syntax
// is an error, so the check fails rather than passes on what it can't read.
func caretRangeAdmits(rng, version string) (bool, error) {
	v, err := parseVersion(version)
	if err != nil {
		return false, err
	}
	for _, term := range strings.Split(rng, "||") {
		term = strings.TrimSpace(term)
		if !strings.HasPrefix(term, "^") {
			return false, fmt.Errorf("range term %q: only ^X.Y.Z terms are understood", term)
		}
		lo, err := parseVersion(term[1:])
		if err != nil {
			return false, err
		}
		if !atLeast(v, lo) {
			continue
		}
		switch {
		case lo[0] > 0:
			if v[0] == lo[0] {
				return true, nil
			}
		case lo[1] > 0:
			if v[0] == 0 && v[1] == lo[1] {
				return true, nil
			}
		default:
			if v == lo {
				return true, nil
			}
		}
	}
	return false, nil
}

func parseVersion(s string) ([3]int, error) {
	var v [3]int
	parts := strings.Split(strings.TrimSpace(s), ".")
	if len(parts) != 3 {
		return v, fmt.Errorf("version %q: want X.Y.Z", s)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return v, fmt.Errorf("version %q: %v", s, err)
		}
		v[i] = n
	}
	return v, nil
}

func atLeast(v, lo [3]int) bool {
	for i := range v {
		if v[i] != lo[i] {
			return v[i] > lo[i]
		}
	}
	return true
}

func TestCaretRangeAdmits(t *testing.T) {
	for _, c := range []struct {
		rng, v string
		want   bool
	}{
		{"^0.9.0", "0.9.0", true},
		{"^0.9.0", "0.9.3", true},
		{"^0.9.0", "0.10.0", false},
		{"^0.8.0 || ^0.9.0", "0.10.0", false},
		{"^0.8.0 || ^0.9.0 || ^0.10.0", "0.10.0", true},
		{"^0.9.2", "0.9.1", false},
		{"^1.2.0", "1.9.0", true},
		{"^1.2.0", "2.0.0", false},
		{"^0.0.3", "0.0.4", false},
	} {
		got, err := caretRangeAdmits(c.rng, c.v)
		if err != nil || got != c.want {
			t.Errorf("caretRangeAdmits(%q, %q) = %v, %v; want %v", c.rng, c.v, got, err, c.want)
		}
	}
	if _, err := caretRangeAdmits(">=0.9.0", "0.9.0"); err == nil {
		t.Error("a range it can't read must be an error, not a pass")
	}
}
