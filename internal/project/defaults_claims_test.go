//go:build !js

package project

import "testing"

// RED-012: the retain and lease names a manifest does not set default from
// the project name ("pair" here), and a name the manifest sets wins.
func TestRetainAndLeaseNameDefaults(t *testing.T) {
	p, err := Load(pairFS("retain: {}\nredundancy: {}\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	cm, file := p.RetainNames()
	if cm != "pair-retain" || file != "retain.json" {
		t.Errorf("defaults = configmap %q, file %q; want pair-retain, retain.json", cm, file)
	}
	if got := p.LeaseName(); got != "pair" {
		t.Errorf("default lease = %q, want the project name pair", got)
	}

	p, err = Load(pairFS("retain:\n  file: keep.json\n  configmap: kept\nredundancy:\n  lease: pair-a\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	cm, file = p.RetainNames()
	if cm != "kept" || file != "keep.json" || p.LeaseName() != "pair-a" {
		t.Errorf("set names = %q %q %q, want kept keep.json pair-a", cm, file, p.LeaseName())
	}
}
