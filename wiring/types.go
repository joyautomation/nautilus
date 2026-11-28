// Package wiring is the file format of struct wiring: the YAML that says,
// beside a plain IEC TYPE, where each member of an instance comes from
// (docs/design/one-struct-model.md, #1458).
//
// It is a leaf, a published language like devprofile: it decodes and checks
// the format and nothing else. The manifest composes it, so it knows no
// driver, runtime or tag store. The names a wiring uses for a driver's own
// columns (table, address, node, ...) pass through untouched in Addr: the
// driver's manifest checks them when wirebind hands them over (a later
// slice), so a misspelled address key is reported there, not here.
package wiring

// Pos is where something was declared, for an error that names it.
type Pos struct {
	File string
	Line int
}

// Kind is what a member's value comes from (design §2.1).
type Kind string

const (
	From   Kind = "from"   // read from a device; never writable
	To     Kind = "to"     // a command, sent on change
	Both   Kind = "both"   // a peer controller's own memory, read and written
	Memory Kind = "memory" // a value the program, operators and clients keep
	Expr   Kind = "expr"   // computed each scan; never writable
)

// Memory modes, the value of a `memory:` entry.
const (
	Retained = "retained"
	Initial  = "initial"
)

// PeerDrivers are the driver types whose members are the peer's own memory,
// so `both:` is allowed on them. Every other driver is a field device, which
// refuses it (design §2.1).
var PeerDrivers = []string{"eip", "sparkplug-host"}

// File is one wiring file, or the wiring part of nautilus.yaml.
type File struct {
	Wirings   []Wiring   `yaml:"wirings"`
	Instances []Instance `yaml:"instances"`
}

// Wiring says how the members of one type are wired.
type Wiring struct {
	// Name defaults to Type. An instance names a wiring only when its type
	// has more than one.
	Name string `yaml:"name"`
	Type string `yaml:"type"`
	// Version, when set, is the Sparkplug Template version the type
	// publishes; left out, it is a content hash (design §11.1).
	Version string            `yaml:"version"`
	Params  map[string]Param  `yaml:"params"`
	Sources map[string]Source `yaml:"sources"`
	// Fill makes one driver fill the type whole (EtherNet/IP, a Sparkplug
	// host): every member it brings is `from:` that source unless Members
	// says otherwise.
	Fill    *Fill             `yaml:"fill"`
	Members map[string]Member `yaml:"members"`
	Alarms  []AlarmRule       `yaml:"alarms"`

	Pos Pos `yaml:"-"`
}

// WiringName is the name an instance refers to.
func (w *Wiring) WiringName() string {
	if w.Name != "" {
		return w.Name
	}
	return w.Type
}

// Param is a value an instance gives. A scalar or null in YAML is the
// default (null: every instance must give it). A mapping adds `publish:`.
type Param struct {
	// Default is nil when the instance must give the param.
	Default any
	// Publish is the Sparkplug opt in or out (design §11.1); nil is the
	// default rule (published, except a param that feeds an endpoint).
	Publish *bool
}

// Required is true for a param with no default.
func (p Param) Required() bool { return p.Default == nil }

// Source is one named connection: which driver and the keys that make a
// connection of its own (endpoint, unit-id, ...).
type Source struct {
	// Driver names a `drivers:` entry, or a driver type when the project has
	// exactly one of it.
	Driver string
	// Keys are the rest, passed to the driver untouched.
	Keys map[string]any
}

// Fill is a type filled whole by a driver.
type Fill struct {
	From string
	// Member is the wrapped member's path when the type is wrapped rather
	// than extended (design §8).
	Member string
	Keys   map[string]any
}

// Member is one wiring entry: a kind and its keys.
type Member struct {
	Kind Kind
	// Source names the connection for from, to and both.
	Source string
	// MemoryMode is Retained or Initial for Kind Memory.
	MemoryMode string
	// Expr is the ST expression for Kind Expr.
	Expr string
	// Readback names the member that shows what the device did with a `to:`.
	Readback string
	// Event marks a `to:` whose first value after a gate reopen, a takeover
	// or a reconnect is a baseline, never sent (design §2.3).
	Event bool
	Unit  string
	Desc  string
	// Addr are the driver's own keys (table, address, format, ...).
	Addr map[string]any

	Line int
}

// AlarmRule is an alarm over a wiring member, with the fields a device
// profile's alarm takes.
type AlarmRule struct {
	Member   string `yaml:"member"`
	Name     string `yaml:"name"`
	Priority string `yaml:"priority"`
	OnDelay  string `yaml:"on-delay"`
	Class    string `yaml:"class"`
	Notes    string `yaml:"notes"`
}

// Instance is a tag of a wired type.
type Instance struct {
	Name   string `yaml:"name"`
	Type   string `yaml:"type"`
	Wiring string `yaml:"wiring"`
	Desc   string `yaml:"desc"`
	// Params are values for the wiring's params.
	Params map[string]any `yaml:"params"`
	// Set gives initial values to memory members by path, nested like a
	// tag's init:.
	Set map[string]any `yaml:"set"`
	// Wire overrides a member's address or source for this instance, never
	// its kind.
	Wire map[string]Member `yaml:"wire"`
	// Alarms tunes the wiring's alarms by member, or turns one off.
	Alarms map[string]Tuning `yaml:"alarms"`
	// OfflineAlarm tunes the comms-loss alarm of the connections the
	// instance owns, or turns it off.
	OfflineAlarm *Offline `yaml:"offline-alarm"`

	Pos Pos `yaml:"-"`
}

// Tuning is a per-instance change to one alarm: `false` turns it off, a
// mapping changes its fields.
type Tuning struct {
	Off      bool
	Priority string
	OnDelay  string
	Class    string
	Name     string
	Notes    string
}

// Offline tunes comms-loss alarms. Either the whole instance (`false`, or a
// mapping of priority and on-delay) or one connection by source name.
type Offline struct {
	Off      bool
	All      *Tuning
	BySource map[string]Tuning
}
