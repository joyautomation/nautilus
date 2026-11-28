package modbus

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

const sampleManifest = `
sources:
  - id: FTIR_I
    host: 192.168.20.51
    port: 502
    unitid: 1
    wordorder: little
    timeout: 3s
    retrymin: 1s
    retrymax: 60s
    enable: CFG.FtirEnabled
    maxblock: 64
  - id: RGN_PID_A
    host: 192.168.20.10
    unitid: 2
tags:
  - {name: FTIR_I_CO, source: FTIR_I, table: holding, address: 14, format: float32}
  - {name: RGN_PID_A_Tpv, source: RGN_PID_A, table: holding, address: 0, format: int32, scale: 1}
  - {name: RGN_PID_A_Tsp, source: RGN_PID_A, table: holding, address: 262, format: int32, writable: true, rewrite: 2.5s}
  - {name: SC_PM_V01, source: RGN_PID_A, table: holding, address: 8, format: int16, writable: true, rewrite: 2.5s, writeonly: true}
  - {name: PUMP_Run, source: RGN_PID_A, table: coil, address: 3, format: bool, writable: true}
`

func TestParseManifestSample(t *testing.T) {
	m, err := ParseManifest([]byte(sampleManifest))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if len(m.Sources) != 2 || len(m.Tags) != 5 {
		t.Fatalf("got %d sources, %d tags", len(m.Sources), len(m.Tags))
	}
	s := m.Sources[0]
	if s.ID != "FTIR_I" || s.Host != "192.168.20.51" || s.UnitID != 1 ||
		s.WordOrder != "little" || s.Enable != "CFG.FtirEnabled" || s.MaxBlock != 64 {
		t.Errorf("source = %+v", s)
	}
	if s.Timeout != 3*time.Second || s.RetryMin != time.Second || s.RetryMax != time.Minute {
		t.Errorf("durations = %v %v %v", s.Timeout, s.RetryMin, s.RetryMax)
	}
	if got := s.Addr(); got != "192.168.20.51:502" {
		t.Errorf("Addr = %q", got)
	}
	if got := m.Sources[1].Addr(); got != "192.168.20.10:502" {
		t.Errorf("default port Addr = %q", got)
	}
	tsp := m.Tags[2]
	if !tsp.Writable || tsp.Rewrite != 2500*time.Millisecond || tsp.Address != 262 {
		t.Errorf("Tsp = %+v", tsp)
	}
	if !m.Tags[3].WriteOnly {
		t.Errorf("SC_PM_V01 should be writeonly: %+v", m.Tags[3])
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// Marshal → parse → deep-equal: the YAML shape survives a round trip, with
// durations as "2.5s" strings rather than nanosecond integers.
func TestManifestRoundTrip(t *testing.T) {
	m, err := ParseManifest([]byte(sampleManifest))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	out, err := yaml.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if s := string(out); !strings.Contains(s, "rewrite: 2.5s") || !strings.Contains(s, "timeout: 3s") {
		t.Errorf("marshaled durations not human-readable:\n%s", s)
	}
	back, err := ParseManifest(out)
	if err != nil {
		t.Fatalf("reparse: %v\n%s", err, out)
	}
	if !reflect.DeepEqual(m, back) {
		t.Errorf("round trip changed the manifest:\n%+v\n%+v", m, back)
	}
}

// KnownFields survives into the custom unmarshalers: a typo anywhere is an
// error, not a silently dropped setting.
func TestParseManifestRejectsUnknownKeys(t *testing.T) {
	cases := []struct {
		name, src, want string
	}{
		{"top level", "sourcess: []\n", "sourcess"},
		{"source key", "sources:\n  - id: A\n    host: h\n    wordorderr: big\n", "wordorderr"},
		{"source did-you-mean", "sources:\n  - id: A\n    host: h\n    wordorderr: big\n", "did you mean wordorder?"},
		{"tag key", "sources: [{id: A, host: h}]\ntags:\n  - {name: T, source: A, table: holding, address: 0, format: int16, writeable: true}\n", "writeable"},
		{"tag did-you-mean", "sources: [{id: A, host: h}]\ntags:\n  - {name: T, source: A, table: holding, address: 0, format: int16, writeable: true}\n", "did you mean writable?"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseManifest([]byte(tc.src))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v does not mention %q", err, tc.want)
			}
		})
	}
}

func TestParseManifestBadDuration(t *testing.T) {
	src := "sources:\n  - id: A\n    host: h\n    timeout: 3\n"
	_, err := ParseManifest([]byte(src))
	if err == nil || !strings.Contains(err.Error(), "bad duration") {
		t.Fatalf("want bad-duration error, got %v", err)
	}
}

func TestParseManifestEmpty(t *testing.T) {
	if _, err := ParseManifest(nil); err == nil {
		t.Fatal("want error for empty manifest")
	}
}

func TestValidateErrors(t *testing.T) {
	src := func(tags string) string {
		return "sources: [{id: A, host: h}, {id: B, host: h2}]\ntags:\n" + tags
	}
	cases := []struct {
		name, yaml, want string
	}{
		{
			"duplicate source id",
			"sources: [{id: A, host: h}, {id: A, host: h2}]\n",
			`duplicate source id "A"`,
		},
		{
			"missing host",
			"sources: [{id: A}]\n",
			"source A: missing host",
		},
		{
			"host carries a port",
			"sources: [{id: A, host: \"127.0.0.1:5020\"}]\n",
			"use port:",
		},
		{
			"bad word order",
			"sources: [{id: A, host: h, wordorder: middle}]\n",
			`wordorder "middle"`,
		},
		{
			"retry inversion",
			"sources: [{id: A, host: h, retrymin: 10s, retrymax: 1s}]\n",
			"retrymin 10s exceeds retrymax 1s",
		},
		{
			"unknown source with suggestion",
			src("  - {name: T, source: AA, table: holding, address: 0, format: int16}\n"),
			`unknown source "AA" (did you mean A?)`,
		},
		{
			"unknown table with suggestion",
			src("  - {name: T, source: A, table: holdings, address: 0, format: int16}\n"),
			`unknown table "holdings" (did you mean holding?)`,
		},
		{
			"bit out of range",
			src("  - {name: T, source: A, table: holding, address: 0, format: 'bit:16'}\n"),
			"bit number must be 0..15",
		},
		{
			"register format on coil table",
			src("  - {name: T, source: A, table: coil, address: 0, format: float32}\n"),
			"format float32 on coil table",
		},
		{
			"writable input table",
			src("  - {name: T, source: A, table: input, address: 0, format: int16, writable: true}\n"),
			"writable on read-only input table",
		},
		{
			"writable discrete table",
			src("  - {name: T, source: A, table: discrete, address: 0, writable: true}\n"),
			"writable on read-only discrete table",
		},
		{
			"rewrite without writable",
			src("  - {name: T, source: A, table: holding, address: 0, format: int16, rewrite: 1s}\n"),
			"rewrite without writable",
		},
		{
			"writeonly without writable",
			src("  - {name: T, source: A, table: holding, address: 0, format: int16, writeonly: true}\n"),
			"writeonly without writable",
		},
		{
			"duplicate tag names",
			src("  - {name: T, source: A, table: holding, address: 0, format: int16}\n" +
				"  - {name: T, source: B, table: holding, address: 4, format: int16}\n"),
			`duplicate tag name "T"`,
		},
		{
			"overlap different formats",
			src("  - {name: T1, source: A, table: holding, address: 0, format: float32}\n" +
				"  - {name: T2, source: A, table: holding, address: 1, format: int16}\n"),
			"overlap with different layouts",
		},
		{
			"address space overflow",
			src("  - {name: T, source: A, table: holding, address: 65535, format: float32}\n"),
			"exceeds the 16-bit address space",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := ParseManifest([]byte(tc.yaml))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			err = m.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// Coherent overlaps are legal: bit:N flags beside the uint16 status word
// they live in, on the same address; and different sources or tables never
// collide.
func TestValidateAllowsCoherentOverlap(t *testing.T) {
	src := `
sources: [{id: A, host: h}, {id: B, host: h2}]
tags:
  - {name: Status, source: A, table: holding, address: 5, format: uint16}
  - {name: Running, source: A, table: holding, address: 5, format: 'bit:0'}
  - {name: Faulted, source: A, table: holding, address: 5, format: 'bit:1'}
  - {name: Other, source: B, table: holding, address: 5, format: float32}
  - {name: InputTwin, source: A, table: input, address: 5, format: float32}
`
	m, err := ParseManifest([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// A coil binding may leave format empty — one coil is self-evidently a bool.
func TestCoilFormatDefaultsToBool(t *testing.T) {
	src := "sources: [{id: A, host: h}]\ntags:\n  - {name: T, source: A, table: coil, address: 0}\n"
	m, err := ParseManifest([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// Validate reports every problem at once, not just the first.
func TestValidateJoinsErrors(t *testing.T) {
	src := `
sources: [{id: A, host: h}]
tags:
  - {name: T1, source: X, table: holding, address: 0, format: int16}
  - {name: T2, source: A, table: nope, address: 0, format: int16}
`
	m, err := ParseManifest([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	verr := m.Validate()
	if verr == nil {
		t.Fatal("want errors")
	}
	for _, want := range []string{`unknown source "X"`, `unknown table "nope"`} {
		if !strings.Contains(verr.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, verr)
		}
	}
}
