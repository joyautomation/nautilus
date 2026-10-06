package seed

import "testing"

// PouName follows `naut new`'s pascalCase (cmd/naut/new.go; its table is
// TestPascalCase there): a blank main.fbd seeds PROGRAM Main (#209).
func TestPouNameMatchesNautNew(t *testing.T) {
	for in, want := range map[string]string{
		"main":        "Main",
		"water-plant": "WaterPlant",
		"tank":        "Tank",
		"my_plc_2":    "MyPlc2",
		"3rd-line":    "P3rdLine",
		"heater_2":    "Heater2",
		"Heater":      "Heater",
		"MotorCtl":    "MotorCtl",
		"":            "Main",
		"--":          "Main",
		"1bad":        "P1bad",
	} {
		if got := PouName(in); got != want {
			t.Errorf("PouName(%q) = %q, want %q", in, got, want)
		}
	}
}
