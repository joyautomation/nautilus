package project

import (
	"testing"
	"testing/fstest"
	"time"
)

func staleFS(built, src time.Time, srcPath string) fstest.MapFS {
	return fstest.MapFS{
		"hmi/build/index.html":              {ModTime: built},
		"hmi/build/_app/app.js":             {ModTime: built.Add(-time.Second)},
		"hmi/src/routes/+page.svelte":       {ModTime: built.Add(-time.Hour)},
		"hmi/node_modules/x/index.js":       {ModTime: built.Add(time.Hour)},
		"hmi/.svelte-kit/generated/root.js": {ModTime: built.Add(time.Hour)},
		"plant.mimic.json":                  {ModTime: built.Add(-time.Hour)},
		"programs/main.st":                  {ModTime: built.Add(time.Hour)},
		srcPath:                             {ModTime: src},
	}
}

func TestStaleHMISource(t *testing.T) {
	t0 := time.Date(2026, 10, 27, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		name, src string
		at        time.Time
		want      string
	}{
		// A mimic edited after the build: the page imports it.
		{"mimic after build", "lift.mimic.json", t0.Add(time.Minute), "lift.mimic.json"},
		{"scene after build", "lift.scene.json", t0.Add(time.Minute), "lift.scene.json"},
		{"hmi source after build", "hmi/src/lib/Pump.svelte", t0.Add(time.Minute), "hmi/src/lib/Pump.svelte"},
		// Saved within the timestamp slack of the build: not newer.
		{"same instant", "lift.mimic.json", t0.Add(time.Second), ""},
		{"source older than build", "hmi/src/lib/Pump.svelte", t0.Add(-time.Minute), ""},
		// Not HMI sources: dependencies, generated files, programs.
		{"node_modules", "hmi/node_modules/y/index.js", t0.Add(time.Hour), ""},
		{"dot dir", "hmi/.svelte-kit/types/x.d.ts", t0.Add(time.Hour), ""},
		{"st program", "programs/other.st", t0.Add(time.Hour), ""},
		{"nested mimic outside the hmi", "docs/old.mimic.json", t0.Add(time.Hour), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := StaleHMISource(staleFS(t0, c.at, c.src), "hmi/build"); got != c.want {
				t.Fatalf("StaleHMISource = %q, want %q", got, c.want)
			}
		})
	}
}

func TestStaleHMISourceNothingToCompare(t *testing.T) {
	t0 := time.Date(2026, 10, 27, 8, 0, 0, 0, time.UTC)
	fsys := staleFS(t0, t0.Add(time.Hour), "lift.mimic.json")
	// Not built yet (HMIMissing's case), unset, or at the project root.
	for _, dir := range []string{"hmi/dist", "", "build"} {
		if got := StaleHMISource(fsys, dir); got != "" {
			t.Fatalf("StaleHMISource(%q) = %q, want \"\"", dir, got)
		}
	}
	// A built binary's embedded project carries no timestamps.
	zero := fstest.MapFS{"hmi/build/index.html": {}, "lift.mimic.json": {}}
	if got := StaleHMISource(zero, "hmi/build"); got != "" {
		t.Fatalf("embedded: %q, want \"\"", got)
	}
}
