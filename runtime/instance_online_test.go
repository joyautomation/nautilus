package runtime

import (
	"strings"
	"testing"
)

// onlineRig is a tank on two connections (A and B), whose combined
// companion T1__Online the runtime computes (InstanceDef.Online).
func onlineRig(t *testing.T, program string, extra ...TagDef) (*Runtime, *instDriver) {
	t.Helper()
	def := tankDef()
	def.Online, def.OnlineOf = "T1__Online", []string{"T1_a__Online", "T1_b__Online"}
	tags := append([]TagDef{
		InstanceTag("T1", "Tank", def),
		Typed("T1_a__Online", RoleInput, "BOOL", Comms()),
		Typed("T1_b__Online", RoleInput, "BOOL", Comms()),
		Typed("T1__Online", RoleInput, "BOOL"),
	}, extra...)
	r, d := newTankRuntime(t, func(o *Options) {
		o.Tags = tags
		if program != "" {
			o.Program = program
		}
	})
	return r, d
}

// The combined companion is the AND of the connection companions, written
// right after each delivery: TRUE only while every one is TRUE, FALSE when
// one is FALSE or never delivered. Before any delivery it reads FALSE
// (it is seeded, so a program reading it runs). A force on a connection's
// companion counts, as the store holds it.
func TestInstanceOnlineIsTheANDOfItsConnections(t *testing.T) {
	r, d := onlineRig(t, "PROGRAM Main\nVAR_EXTERNAL T1 : Tank; T1__Online : BOOL; END_VAR\nVAR seen : BOOL; END_VAR\nseen := T1__Online;\nEND_PROGRAM")
	if v, err := r.Tags().ReadGlobal("T1__Online"); err != nil || v.B {
		t.Fatalf("before any scan T1__Online = %v, %v; want a seeded FALSE", v, err)
	}
	online := func() bool { t.Helper(); r.Scan(); return r.Tags().Bool("T1__Online") }
	d.set("T1_a__Online", true)
	if online() {
		t.Error("TRUE with B never delivered")
	}
	d.set("T1_b__Online", true)
	if !online() {
		t.Error("FALSE with both connections answering")
	}
	d.set("T1_b__Online", false)
	if online() {
		t.Error("TRUE with B down")
	}
	d.set("T1_b__Online", true)
	if err := r.Tags().Force("T1_a__Online", false); err != nil {
		t.Fatal(err)
	}
	if online() {
		t.Error("TRUE with A forced FALSE")
	}
	r.Tags().Unforce("T1_a__Online")
	if !online() {
		t.Error("FALSE once the force is gone and both answer")
	}
}

// Nothing writes the combined companion from outside: it is an input.
func TestInstanceOnlineRefusesAnOutsideWrite(t *testing.T) {
	r, d := onlineRig(t, "")
	d.set("T1_a__Online", true)
	d.set("T1_b__Online", true)
	r.Scan()
	if err := writeErr(r, "T1__Online", false); err == nil {
		t.Error("an outside write to the combined companion must be refused")
	}
}

// The combined companion must be a declared input of type BOOL, named by
// one instance, with the companions it reads.
func TestInstanceOnlineDeclarationRefusals(t *testing.T) {
	inst := func(online string, of ...string) TagDef {
		def := tankDef()
		def.Online, def.OnlineOf = online, of
		return InstanceTag("T1", "Tank", def)
	}
	for name, tc := range map[string]struct {
		tags []TagDef
		want string
	}{
		"undeclared":              {[]TagDef{inst("T1__Online", "A"), Typed("A", RoleInput, "BOOL", Comms())}, "its combined companion is no declared tag"},
		"not an input":            {[]TagDef{inst("T1__Online", "A"), Typed("A", RoleInput, "BOOL", Comms()), Typed("T1__Online", RoleState, "BOOL")}, "is not an input of type BOOL"},
		"not a BOOL":              {[]TagDef{inst("T1__Online", "A"), Typed("A", RoleInput, "BOOL", Comms()), Typed("T1__Online", RoleInput, "DINT")}, "is not an input of type BOOL"},
		"no companions":           {[]TagDef{inst("T1__Online"), Typed("T1__Online", RoleInput, "BOOL")}, "names no connection companions"},
		"an undeclared companion": {[]TagDef{inst("T1__Online", "T1_a__Online"), Typed("T1__Online", RoleInput, "BOOL")}, "reads T1_a__Online, which is no declared tag"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New(Options{
				Program:   "PROGRAM Main\nVAR_EXTERNAL T1 : Tank; END_VAR\nEND_PROGRAM",
				Libraries: []string{tankLib}, Driver: &instDriver{},
				Tags: tc.tags,
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("New = %v, want it to say %q", err, tc.want)
			}
		})
	}
}
