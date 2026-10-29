package host

import "testing"

// InputType names the IEC type each input delivers — the seed type the
// runtime falls back to for a manifest tag with no type: that no program
// declares (runtime.TagDef.DriverType): the generated ST's datatype mapping,
// the Template's TYPE, and the companions' BOOL/LINT.
func TestInputTypeNamesTheIECType(t *testing.T) {
	d, _ := newTestDriver(t, mustParse(t, sampleManifest), Config{})
	for name, want := range map[string]string{
		"W6_Well_Level":        "LREAL",
		"W6_Pump1":             "Motor",
		"W6_PLC1_Pump_Run":     "BOOL",
		"W6__Online":           "BOOL",
		"W6_PLC1__Online":      "BOOL",
		"W6__LastBirthMs":      "LINT",
		"W6_PLC1_Pump_SpeedSP": "", // writable: an output, not an input
		"Nope":                 "",
	} {
		if got := d.InputType(name); got != want {
			t.Errorf("InputType(%s) = %q, want %q", name, got, want)
		}
	}
}
