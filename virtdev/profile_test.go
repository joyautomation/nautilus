package virtdev

import (
	"testing"

	"github.com/joyautomation/nautilus/devprofile"
	"github.com/joyautomation/nautilus/runtime"
)

func TestBuiltinProfilesLoad(t *testing.T) {
	ps, err := devprofile.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) == 0 {
		t.Fatal("no built-in profiles")
	}
	for _, p := range ps {
		// Each built-in profile composes into a plant that compiles.
		set := &Set{Devices: []Instance{{Name: "D", Profile: p.ID}}}
		if err := set.resolve(); err != nil {
			t.Errorf("%s: %v", p.ID, err)
			continue
		}
		plant, err := NewPlant(set)
		if err != nil {
			t.Errorf("%s: plant: %v", p.ID, err)
			continue
		}
		// A profile with no simulator generates a main program with an
		// empty body; it must still compile.
		if _, err := runtime.New(plant.Options()); err != nil {
			t.Errorf("%s: plant does not compile: %v\n%s", p.ID, err, plant.Program())
		}
	}
}

func TestSetErrors(t *testing.T) {
	cases := map[string]string{
		"no devices":      "devices: []\n",
		"bad name":        "devices: [{ name: 1x, profile: waveshare-relay-16ch }]\n",
		"no profile":      "devices: [{ name: A }]\n",
		"unknown profile": "devices: [{ name: A, profile: nope }]\n",
		"twice":           "devices: [{ name: A, profile: waveshare-relay-16ch }, { name: a, profile: waveshare-relay-16ch }]\n",
		"same unit twice": "devices: [{ name: A, profile: waveshare-relay-16ch, endpoint: 10.0.0.1 }, { name: B, profile: waveshare-relay-16ch, endpoint: '10.0.0.1:502' }]\n",
		"init no point":   "devices: [{ name: A, profile: waveshare-relay-16ch, init: { Nope: true } }]\n",
	}
	for name, body := range cases {
		if _, err := ParseSet([]byte(body), t.TempDir()); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	// A device with a simulator takes init for its points and its hooks,
	// and nothing else.
	for body, ok := range map[string]bool{
		"devices: [{ name: A, profile: asi-275m, init: { Input3: true, Outputs: 5 } }]\n": true,
		"devices: [{ name: A, profile: asi-275m, init: { Input33: true } }]\n":            false,
	} {
		set, err := ParseSet([]byte(body), t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := NewPlant(set); (err == nil) != ok {
			t.Errorf("%s: plant error %v", body, err)
		}
	}
	// Two boards behind one address with different unit ids are fine.
	if _, err := ParseSet([]byte("devices: [{ name: A, profile: waveshare-relay-16ch, endpoint: 10.0.0.1 }, { name: B, profile: waveshare-relay-16ch, endpoint: 10.0.0.1, unit-id: 2 }]\n"), t.TempDir()); err != nil {
		t.Errorf("two units behind one endpoint: %v", err)
	}
}
