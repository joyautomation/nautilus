package main

// `naut check` and multi-driver manifests: a tag owned by two drivers is
// a LOAD-time refusal, and check is where an author meets it first — so the
// message must land in check's output naming both drivers and the tag, not
// wait for `naut run` on the controller.

import (
	"strings"
	"testing"
)

func multiDriverProject(tagA, tagB string) map[string]string {
	return map[string]string{
		"nautilus.yaml": `
tasks:
  - program: program.st
drivers:
  - type: sparkplug-host
    broker: tcp://mqtt.invalid:1883
    host-id: central-a
    group-id: GA
    manifest: a.yaml
  - type: sparkplug-host
    broker: tcp://mqtt.invalid:1883
    host-id: central-b
    group-id: GB
    manifest: b.yaml
`,
		"program.st": `PROGRAM Main
VAR X : REAL; END_VAR
X := 1.0;
END_PROGRAM`,
		"a.yaml": "group: GA\nnodes:\n    - edgenode: W1\ntags:\n    - { name: " + tagA +
			", node: W1, device: \"\", metric: M, type: Double, arraylen: 0, writable: false }\n",
		"b.yaml": "group: GB\nnodes:\n    - edgenode: W9\ntags:\n    - { name: " + tagB +
			", node: W9, device: \"\", metric: M, type: Double, arraylen: 0, writable: false }\n",
	}
}

func TestCheckReportsDuplicateTagAcrossDrivers(t *testing.T) {
	out, code := checkIn(t, multiDriverProject("Shared_Level", "Shared_Level"))
	if code != 1 {
		t.Fatalf("exit = %d, want 1 — a tag owned by two drivers cannot run\n%s", code, out)
	}
	for _, want := range []string{"Shared_Level", "sparkplug-host", "sparkplug-host-2"} {
		if !strings.Contains(out, want) {
			t.Errorf("check output does not name %q:\n%s", want, out)
		}
	}
}

func TestCheckAcceptsAMultiDriverManifest(t *testing.T) {
	out, code := checkIn(t, multiDriverProject("W1_Level", "W9_Level"))
	if code != 0 {
		t.Fatalf("exit = %d, want 0 — disjoint drivers are the supported shape\n%s", code, out)
	}
}
