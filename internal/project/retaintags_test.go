package project

import (
	"strings"
	"testing"
)

// A manifest tag's retain: true and a library's VAR_GLOBAL RETAIN both mark
// the tag for the retain store; a plain VAR_GLOBAL and an unmarked state tag
// do not (brief 002 F-05).
func TestRetainMarksTags(t *testing.T) {
	proj, err := Load(mapProject(map[string]string{
		"nautilus.yaml": "tasks:\n  - program: p.st\ntags:\n" +
			"  - { name: RunHours, role: state, init: 0.0, retain: true }\n" +
			"  - { name: Scratch, role: state, init: 0.0 }\n",
		"gvl.st": "VAR_GLOBAL RETAIN\n  Starts : DINT;\nEND_VAR\nVAR_GLOBAL\n  Lead : INT;\nEND_VAR\n",
		"p.st":   "PROGRAM P\nVAR_EXTERNAL RunHours : REAL; Starts : DINT; Lead : INT; Scratch : REAL; END_VAR\nRunHours := RunHours + 1.0;\nEND_PROGRAM\n",
	}), "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, d := range proj.Runtime.Tags {
		got[d.Name] = d.Retain
	}
	for name, want := range map[string]bool{"RunHours": true, "Starts": true, "Scratch": false, "Lead": false} {
		if got[name] != want {
			t.Errorf("%s: retain %v, want %v", name, got[name], want)
		}
	}
}

// retain: true on an input is refused when the controller is built.
func TestRetainOnAnInputIsRefused(t *testing.T) {
	err := newRuntimeErr(t, map[string]string{
		"nautilus.yaml": "tasks:\n  - program: p.st\ntags:\n  - { name: Level, role: input, retain: true }\n",
		"p.st":          "PROGRAM P\nEND_PROGRAM\n",
	})
	if err == nil || !strings.Contains(err.Error(), "never retained") {
		t.Fatalf("retain on an input: %v, want a refusal", err)
	}
}

// A global a library declares VAR_GLOBAL RETAIN stays retained when the
// manifest declares the tag as well: the manifest's line cannot un-retain it.
func TestGVLRetainHoldsForAManifestDeclaredGlobal(t *testing.T) {
	proj, err := Load(mapProject(map[string]string{
		"nautilus.yaml": "tasks:\n  - program: p.st\ntags:\n  - { name: Starts, role: state, init: 0 }\n",
		"gvl.st":        "VAR_GLOBAL RETAIN\n  Starts : DINT;\nEND_VAR\n",
		"p.st":          "PROGRAM P\nVAR_EXTERNAL Starts : DINT; END_VAR\nStarts := Starts + 1;\nEND_PROGRAM\n",
	}), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range proj.Runtime.Tags {
		if d.Name == "Starts" && !d.Retain {
			t.Error("Starts is VAR_GLOBAL RETAIN but not retained")
		}
	}
}
