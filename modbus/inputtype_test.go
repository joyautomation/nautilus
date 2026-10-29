package modbus

import "testing"

// InputType derives the IEC type from the binding's format the way decode
// delivers it: BOOL for bit tables, bool and bit:N; DINT for an unscaled
// integer; REAL for a float or anything scaled; BOOL for __Online.
func TestInputTypeFollowsTheFormat(t *testing.T) {
	m, err := ParseManifest([]byte(`sources:
  - id: Dev
    host: 192.0.2.1
tags:
  - {name: Count,  source: Dev, table: holding, address: 0, format: uint16}
  - {name: Scaled, source: Dev, table: holding, address: 1, format: int16, scale: 0.1}
  - {name: Flow,   source: Dev, table: input,   address: 0, format: float32}
  - {name: Bit,    source: Dev, table: holding, address: 2, format: "bit:3"}
  - {name: Coil,   source: Dev, table: coil,    address: 0}
`))
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(m)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"Count": "DINT", "Scaled": "REAL", "Flow": "REAL", "Bit": "BOOL", "Coil": "BOOL",
		"Dev__Online": "BOOL", "Nope": "",
	} {
		if got := d.InputType(name); got != want {
			t.Errorf("InputType(%s) = %q, want %q", name, got, want)
		}
	}
}
