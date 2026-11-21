//go:build !js

package project

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// The config screens' kit patches manifest text in the browser (#711,
// hmi/src/lib/config/yamlpatch.ts). hmi/tests/config-fixtures.json holds
// each case's edited text; the kit's own test pins that its patcher writes
// exactly that text, and this one that the Go loader reads it back as the
// value the person meant: a string such as on, yes, 0x10 or 1e3 stays a
// string, a number, a bool and a duration stay what they are.
func TestKitPatchedManifestsReadBackAsIntended(t *testing.T) {
	raw, err := os.ReadFile("../../hmi/tests/config-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Cases []struct {
			Name  string `json:"name"`
			After string `json:"after"`
			Want  struct {
				Row            int      `json:"row"`
				Field          string   `json:"field"`
				Value          string   `json:"value"`
				Slot           int      `json:"slot"`
				ScanRate       string   `json:"scanRate"`
				Primary        bool     `json:"primary"`
				RebirthOnStart bool     `json:"rebirthOnStart"`
				GroupIDs       []string `json:"groupIDs"`
			} `json:"want"`
			Before string `json:"before"`
			Live   bool   `json:"live"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Cases) < 10 {
		t.Fatalf("only %d fixture cases", len(fx.Cases))
	}
	for _, c := range fx.Cases {
		if c.Live {
			continue // below, against a running controller
		}
		t.Run(c.Name, func(t *testing.T) {
			m, err := ReadManifest(fstest.MapFS{"nautilus.yaml": {Data: []byte(c.After)}}, "")
			if err != nil {
				t.Fatalf("the Go loader refuses the kit's text: %v\n%s", err, c.After)
			}
			if len(m.Drivers) != 2 {
				t.Fatalf("drivers = %d, want 2", len(m.Drivers))
			}
			if c.Want.Field == "ClientID" {
				if got := m.Drivers[c.Want.Row].ClientID; got != c.Want.Value {
					t.Fatalf("client-id reads back %q, want %q", got, c.Want.Value)
				}
				return
			}
			eip, sp := m.Drivers[0], m.Drivers[1]
			wantRate, err := time.ParseDuration(c.Want.ScanRate)
			if err != nil {
				t.Fatal(err)
			}
			if eip.Slot != c.Want.Slot || time.Duration(eip.ScanRate) != wantRate ||
				sp.Primary == nil || *sp.Primary != c.Want.Primary ||
				sp.RebirthOnStart == nil || *sp.RebirthOnStart != c.Want.RebirthOnStart ||
				!reflect.DeepEqual(sp.GroupIDs, c.Want.GroupIDs) {
				t.Fatalf("typed scalars read back as slot %d rate %v primary %v rebirth %v groups %v; want %+v", eip.Slot, eip.ScanRate, sp.Primary, sp.RebirthOnStart, sp.GroupIDs, c.Want)
			}
		})
	}
}

// The same text, staged on a running controller: the overlay the kit emits
// for an edit of the modbus driver's scan rate is what the staged routes
// take, and validates as that one change (#711 slice 2).
func TestKitPatchedOverlayStagesAndValidates(t *testing.T) {
	raw, err := os.ReadFile("../../hmi/tests/config-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Cases []struct {
			Name   string `json:"name"`
			File   string `json:"file"`
			Before string `json:"before"`
			After  string `json:"after"`
			Live   bool   `json:"live"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, c := range fx.Cases {
		if !c.Live {
			continue
		}
		n++
		s1, s2, s3 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 9)
		files := ioFiles(slavePort(s1), slavePort(s2), "")
		// The fixture's ports (41001-41003) stand in for the slaves'.
		ports := func(s string) string {
			return strings.NewReplacer("41001", fmt.Sprint(slavePort(s1)), "41002", fmt.Sprint(slavePort(s2)), "41003", fmt.Sprint(slavePort(s3))).Replace(s)
		}
		if got := string(files[c.File].Data); got != ports(c.Before) {
			t.Fatalf("the fixture's before text drifted from ioFiles' %s:\n%s\nwant\n%s", c.File, ports(c.Before), got)
		}
		r := startIORig(t, files, false)
		cand := r.io.Stage(OverlayFS(files, map[string][]byte{c.File: []byte(ports(c.After))}), "upload", "tester")
		v := r.io.Validate(cand)
		if !v.Valid || len(v.Changes) == 0 {
			t.Fatalf("%s: the kit's overlay does not validate as a change: %+v", c.Name, v)
		}
		for _, ch := range v.Changes {
			if ch.Driver == "" {
				t.Fatalf("a change names no driver: %+v", ch)
			}
		}
	}
	if n == 0 {
		t.Fatal("no live fixture case")
	}
}
