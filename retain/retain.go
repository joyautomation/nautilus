// Package retain is a controller's retained memory: operator setpoints
// survive restarts, failovers, and redeploys; online-edited programs survive
// restarts and failovers of the same build (a deploy of a different build
// replaces them). In a
// cluster the backing store is a ConfigMap, read and written through
// internal/k8sapi (the same pattern the leader package uses for its Lease);
// standalone it's a JSON file next to the project.
package retain

import "github.com/joyautomation/nautilus/internal/k8sapi"

// State is everything a controller retains across power cycles.
type State struct {
	// Tags are retained tag values (operator setpoints), JSON-native scalars.
	Tags map[string]any `json:"tags,omitempty"`
	// Programs are online-edited IEC sources keyed by task name, so an
	// edit made through the runtime API survives a pod restart until
	// `naut pull` lands it in git.
	Programs map[string]string `json:"programs,omitempty"`
	// ProgramBases are, per task in Programs, the hash of the program the
	// deployed binary embedded when the edit was saved — the build the edit
	// was made against. A load reapplies an edit only when the running
	// binary embeds that same program (a restart or failover of the same
	// build); a different build is a deploy, and a deploy replaces the edit.
	// A separate map rather than a field of Programs' values so a store
	// written before bases existed still decodes, and an older binary
	// reading a newer store ignores it.
	ProgramBases map[string]string `json:"programBases,omitempty"`
	// DiscardedPrograms keeps, per task, the source of the last online edit
	// a deploy replaced — never applied, only kept so an edit that was not
	// pulled into git before the deploy can still be recovered from the
	// store. Each discard for a task overwrites the previous one.
	DiscardedPrograms map[string]string `json:"discardedPrograms,omitempty"`
	// Alarms is operator alarm state — acknowledgement and shelf — keyed by
	// alarm definition id. Retained for the same reason setpoints are: an
	// ack is a decision a person made, and a restart or a standby takeover
	// must not resurrect hundreds of acked alarms as unacked. Active and
	// return-to-normal are deliberately absent; they re-derive from the
	// field on the next scan.
	Alarms map[string]AlarmRetain `json:"alarms,omitempty"`
	// Locals are the VAR RETAIN variables of each task's program, keyed by
	// task name, then by variable name, in the plain JSON form Tags uses (a
	// function-block instance as a mapping of its variables). A store
	// written before locals were retained has none, and every local starts
	// at its initial value, as it always did.
	Locals map[string]map[string]any `json:"locals,omitempty"`
	// IO is the live I/O configuration applied over /api/io and not yet in
	// the deployed manifest (#706); see IOState.
	IO *IOState `json:"io,omitempty"`
	// Substitutes are the active substitute values (#707) and the
	// bookkeeping a restart needs to honour them: a substitute is bounded
	// operator intent meant to run the plant through a bad sensor, so a
	// restart must not put the bad reading back into the logic mid-run.
	// Nil until the first substitute; a store written before substitutes
	// existed loads unchanged.
	Substitutes *SubstituteState `json:"substitutes,omitempty"`
}

// SubstituteState is what a controller keeps of its substitutes.
type SubstituteState struct {
	// Entries are the active substitutes.
	Entries []SubstituteRetain `json:"entries,omitempty"`
	// HighID is the highest entry ID ever issued, ended entries included,
	// so a restart does not issue an ID again: a stale stop naming an ended
	// entry must not end a newer one. Every API change is saved before it
	// is answered, so every ID an operator saw is covered. Rolling back to
	// a binary from before substitutes drops this whole block at its next
	// save (it does not know the field): HighID starts again from zero
	// after such a rollback and forward, and so do the entries.
	HighID uint64 `json:"highId,omitempty"`
	// LastSeenMs is the wall clock (epoch ms, rounded down to 30 s) the
	// last time the controller saved while a substitute was active. A
	// restart whose clock reads earlier went backwards across the
	// restart, and its substitutes expire (#707).
	LastSeenMs int64 `json:"lastSeenMs,omitempty"`
}

// SubstituteRetain is one active substitute.
type SubstituteRetain struct {
	ID uint64 `json:"id"`
	// Name is the address: a tag, or a member path of one.
	Name string `json:"name"`
	// Value is the substitute in plain JSON form, as Tags holds values.
	Value     any    `json:"value"`
	By        string `json:"by,omitempty"`
	StartMs   int64  `json:"startMs"`
	UntilMs   int64  `json:"untilMs"`
	GrantedMs int64  `json:"grantedMs"`
	// DeadlineMs is when the entry ends, re-anchored on the wall clock at
	// the save from the running controller's monotonic remainder (rounded
	// down to a second). A load takes the earlier of it and UntilMs, so a
	// wall clock stepped back while the entry ran does not stretch it
	// after a restart. Zero when the saving controller was not timing it.
	DeadlineMs int64  `json:"deadlineMs,omitempty"`
	AckOnStart bool   `json:"ackOnStart,omitempty"`
	Acked      bool   `json:"acked,omitempty"`
	RequestID  string `json:"requestId,omitempty"`
}

// AlarmRetain is one alarm's retained operator state. Every field is
// omitempty, so a store written before alarms existed loads unchanged and
// the overwhelming majority of alarms — the normal ones — cost nothing.
type AlarmRetain struct {
	Acked        bool   `json:"acked,omitempty"`
	AckBy        string `json:"ackBy,omitempty"`
	AckMs        int64  `json:"ackMs,omitempty"`
	ShelfUntilMs int64  `json:"shelfUntilMs,omitempty"`
	ShelfBy      string `json:"shelfBy,omitempty"`
}

// AlarmRetainer is the alarm engine, as far as the retain saver and loader
// are concerned. Declaring it here rather than importing the alarm package
// keeps the dependency pointing one way: alarm knows about retain, retain
// knows only about a shape.
type AlarmRetainer interface {
	RetainedAlarms() map[string]AlarmRetain
	RestoreAlarms(map[string]AlarmRetain)
}

// Store persists and recalls State. Implementations must treat "nothing has
// been saved yet" as success: Load on a store with no prior Save returns a
// zero State and a nil error, not a not-found error, so callers can Load
// unconditionally on startup.
type Store interface {
	Load() (State, error)
	Save(State) error
	Kind() string // "file" | "configmap", for logs
}

// New picks the store for the environment: a ConfigMap when running in a
// cluster, falling back to the file store if the service account can't be
// read (e.g. the pod's token isn't mounted yet, or RBAC is misconfigured —
// better to retain to a file than to not retain at all); the file store
// outside a cluster.
func New(configMapName, filePath string) Store {
	if k8sapi.InCluster() {
		if c, err := k8sapi.New(); err == nil {
			return NewConfigMap(c, configMapName)
		}
	}
	return &fileStore{path: filePath}
}
