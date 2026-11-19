//go:build !js

package project

// What naut pull --io reads (#706 PR 5b): the project files a live I/O
// apply differs in, and the per-driver differs flag on the status rows.

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func rowsDiffer(r *ioRig) bool {
	for _, s := range r.p.DriverStatus(nil, nil)() {
		if s.Differs {
			return true
		}
	}
	return false
}

// After an upload apply, Files hands back the bytes that run and the hash
// of the file the controller booted from, and the driver's row says
// differs; a restart whose manifest already says the same clears both.
func TestIOFilesAfterAnApply(t *testing.T) {
	s1, s2, s3 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 9)
	files := ioFiles(slavePort(s1), slavePort(s2), "")
	r := startKeepRig(t, files, keepOpts{store: &memStore{}})
	r.leading(t)
	if f := r.io.Files(); len(f.Units) != 0 || len(f.Files) != 0 || !f.Leading {
		t.Fatalf("before any apply Files = %+v, want leading and empty", f)
	}
	if rowsDiffer(r.ioRig) {
		t.Fatal("a driver row differs before any apply")
	}
	r.applyMove(t, files, slavePort(s3), slavePort(s2), ApplyOptions{})
	f := r.io.Files()
	if len(f.Units) != 1 || f.Units[0].Device != "DEV_A" || f.Units[0].AppliedFrom != "upload" {
		t.Fatalf("units = %+v, want DEV_A from upload", f.Units)
	}
	want := modbusYAML(slavePort(s3), slavePort(s2), "", "")
	boot := sha256.Sum256(files["modbus.yaml"].Data)
	if len(f.Files) != 1 || f.Files[0].Path != "modbus.yaml" || f.Files[0].Running != want || f.Files[0].Booted != hex.EncodeToString(boot[:]) {
		t.Fatalf("files = %+v, want modbus.yaml: the bytes that run, booted from %x", f.Files, boot)
	}
	if f.NonIO || len(f.Conflicts) != 0 {
		t.Fatalf("nonIO %v conflicts %v, want neither", f.NonIO, f.Conflicts)
	}
	eventually(t, "A_In from the moved device", func() bool { return r.value("A_In") == "9" })
	if !rowsDiffer(r.ioRig) {
		t.Fatal("no driver row differs after an apply")
	}
	r.stop()

	// The applied files are deployed: the build says what runs.
	deployed := withFile(files, "modbus.yaml", want)
	r2 := startKeepRig(t, deployed, keepOpts{store: &memStore{}})
	r2.leading(t)
	if f := r2.io.Files(); len(f.Units) != 0 || len(f.Files) != 0 {
		t.Fatalf("after deploying the change Files = %+v, want empty", f)
	}
	if rowsDiffer(r2.ioRig) {
		t.Fatal("a row still differs once the manifest caught up")
	}
}

// A standby is not the leader whose configuration is in force; an apply
// that also changes non-I/O sections says so.
func TestIOFilesStandbyAndNonIO(t *testing.T) {
	s1, s2, s3 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 9)
	files := ioFiles(slavePort(s1), slavePort(s2), "")
	coord := &keepCoord{}
	coord.lead.Store(true)
	coord.term.Store(1)
	r := startKeepRig(t, files, keepOpts{store: &memStore{}, coord: coord, noFollow: true})
	r.leading(t)
	cand := withFile(files, "modbus.yaml", modbusYAML(slavePort(s3), slavePort(s2), "", ""))
	cand = withFile(cand, "nautilus.yaml", strings.Replace(string(cand["nautilus.yaml"].Data), "name: io\n", "name: io2\n", 1))
	c := r.io.Stage(cand, "upload", "tester")
	if v := r.io.Validate(c); !v.Valid {
		t.Fatalf("validate: %v %+v", failed(v), v.Checks)
	}
	if _, err := r.io.ApplyWith(r.ctx, c, "tester", ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	f := r.io.Files()
	if !f.NonIO || !f.Leading {
		t.Fatalf("nonIO %v leading %v, want a manifest that also changes the name, on the leader", f.NonIO, f.Leading)
	}
	coord.lead.Store(false)
	if f := r.io.Files(); f.Leading {
		t.Fatal("a standby reported leading")
	}
}

// Two differing units holding one path with different contents are
// reported, not merged.
func TestIOFilesConflict(t *testing.T) {
	s1, s2 := newSlave(t, 4), newSlave(t, 7)
	files := ioFiles(slavePort(s1), slavePort(s2), "")
	r := startKeepRig(t, files, keepOpts{store: &memStore{}})
	r.leading(t)
	r.io.mu.Lock()
	for k, u := range r.io.running {
		u.hash += "x"
		u.files = map[string]string{"modbus.yaml": "from " + k}
		r.io.running[k] = u
	}
	r.io.mu.Unlock()
	if f := r.io.Files(); len(f.Conflicts) != 1 || f.Conflicts[0] != "modbus.yaml" {
		t.Fatalf("conflicts = %v, want modbus.yaml", f.Conflicts)
	}
}

// Several drivers compose in a Multi: only the driver whose unit differs
// badges its row.
func TestIODiffersFlagsOnlyTheDriverThatDiffers(t *testing.T) {
	s1, s2, s3 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 9)
	ua := uaServer(t, 1.5)
	files := ioFiles(slavePort(s1), slavePort(s2), ua)
	r := startKeepRig(t, files, keepOpts{store: &memStore{}})
	r.leading(t)
	r.applyMove(t, files, slavePort(s3), slavePort(s2), ApplyOptions{})
	var flagged, total int
	for _, s := range r.p.DriverStatus(nil, nil)() {
		total++
		if s.Differs {
			flagged++
			if s.Kind != "modbus" {
				t.Fatalf("the %s row differs, want only the modbus one", s.Kind)
			}
		}
	}
	if total < 2 || flagged != 1 {
		t.Fatalf("%d of %d rows differ, want exactly the modbus row of several", flagged, total)
	}
}
