// Package retain is a controller's retained memory: operator setpoints
// survive restarts, failovers, and redeploys; online-edited programs survive
// restarts and failovers of the same build (a deploy of a different build
// replaces them). In a
// cluster the backing store is a ConfigMap, read and written through
// internal/k8sapi (the same pattern the leader package uses for its Lease);
// standalone it's a JSON file next to the project.
package retain

import (
	"time"

	"github.com/joyautomation/nautilus/internal/k8sapi"
)

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
	// AlarmFlood is the alarm flood in progress (#1249), so a restart
	// ends it on this controller, with its id and start, rather than
	// leaving a flood row with no end. Nil when there is none. A pair
	// carries it through its operator-intent log, never through this.
	AlarmFlood *AlarmFlood `json:"alarmFlood,omitempty"`
	// Locals are the VAR RETAIN variables of each task's program, keyed by
	// task name, then by variable name, in the plain JSON form Tags uses (a
	// function-block instance as a mapping of its variables). A store
	// written before locals were retained has none, and every local starts
	// at its initial value, as it always did.
	Locals map[string]map[string]any `json:"locals,omitempty"`
	// IO is the live I/O configuration applied over /api/io and not yet in
	// the deployed manifest (#706); see IOState.
	IO *IOState `json:"io,omitempty"`
	// ParamBases are, per scalar setpoint with a manifest init (a
	// "parameter", #718), the init the retained value was written over: the
	// repo's value when the plant and the repo last agreed. Drift is read
	// from it: the plant moved if the value differs from the init, the repo
	// moved if the init differs from the base. Absent in a store from
	// before parameters; each base then starts as the manifest's init.
	ParamBases map[string]any `json:"paramBases,omitempty"`
	// ParamEdits records, per parameter, who last wrote the retained value
	// from outside the logic and when (#718): the attribution a write-back
	// commit carries. Dropped for a parameter once it is back in step.
	ParamEdits map[string]ParamEdit `json:"paramEdits,omitempty"`
	// ParamCommitted is, per parameter, the value a write-back commit last
	// put in the repo. A plant-ahead value already committed (the running
	// manifest's init stays the old one until a redeploy) is not committed
	// again. Dropped once the parameter is back in step.
	ParamCommitted map[string]any `json:"paramCommitted,omitempty"`
	// Substitutes are the active substitute values (#707) and the
	// bookkeeping a restart needs to honour them: a substitute is bounded
	// operator intent meant to run the plant through a bad sensor, so a
	// restart must not put the bad reading back into the logic mid-run.
	// Nil until the first substitute; a store written before substitutes
	// existed loads unchanged.
	Substitutes *SubstituteState `json:"substitutes,omitempty"`
	// Push is the Web Push subscriptions (#722): a person's phones, so a
	// takeover pages the same ones. Each row's endpoint and keys are
	// sealed (AES-GCM under a key derived from the VAPID key, which is not
	// in the store): this file is mode 0644 and a ConfigMap is not a
	// Secret, so what it holds must be useless without the box's secrets
	// file. Nil until the first subscription.
	Push *PushState `json:"push,omitempty"`
	// Stream is where in a redundant pair's state stream this file stands
	// (#710 S1b): the leadership (term) and image (seq) a standby last
	// carried into it, or the term its own controller led when it last
	// saved as leader (Seq 0). A standby takes no image older than it, so
	// a file never goes back to an older leadership's values. Nil outside
	// a pair. Not carried from an image: the standby writes its own.
	Stream *StreamPos `json:"stream,omitempty"`
}

// StreamPos is a place in a pair's state stream: (term, seq) order.
type StreamPos struct {
	Term uint64 `json:"term"`
	Seq  uint64 `json:"seq,omitempty"`
	// At is when a standby carried the image into the file (#710
	// S2c-3: a hand-back says how old the file it leads on is). Zero in a
	// leader's own save, and in a file from before it.
	At time.Time `json:"at,omitzero"`
}

// Before reports whether p comes before (term, seq) in the stream's
// order. A nil p comes before everything.
func (p *StreamPos) Before(term, seq uint64) bool {
	return p == nil || p.Term < term || (p.Term == term && p.Seq < seq)
}

// PushState is the retained Web Push subscriptions.
type PushState struct {
	Rows []PushRow `json:"rows,omitempty"`
}

// PushRow is one subscription: its owner and where it points in the
// clear (nothing secret), the secrets sealed.
type PushRow struct {
	ID        string    `json:"id"`
	Principal string    `json:"principal"`
	Host      string    `json:"host"`
	At        time.Time `json:"at"`
	Groups    []string  `json:"groups,omitempty"`
	Seen      time.Time `json:"seen,omitzero"`
	// Key fingerprints the VAPID key the row is sealed under (not the
	// key): a replica holding another key keeps the row and sends none.
	Key    string `json:"key"`
	Cred   string `json:"cred,omitempty"`
	Sealed string `json:"sealed"`
}

// ParamEdit is one parameter's last external write.
type ParamEdit struct {
	By   string `json:"by"`
	AtMs int64  `json:"atMs"`
	// Val is the value that write left. A value that differs later was set
	// by something else (the program), and the record then says so.
	Val any `json:"val,omitempty"`
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
	// Occurrence, ActiveMs and WaitMs name the occurrence an alarm whose
	// condition was present was on (#715): its ID, its activation and
	// when it began waiting on a person (0: it was not). Honoured only if
	// the field re-asserts the condition (alarm.Engine.RestoreAlarms); an
	// ack with an occurrence applies to that occurrence alone.
	Occurrence string `json:"occurrence,omitempty"`
	ActiveMs   int64  `json:"activeMs,omitempty"`
	WaitMs     int64  `json:"waitMs,omitempty"`

	// OOS* is the out-of-service window of the alarm's equipment (#715
	// PR 5), on each alarm it covers, so the alarm funnel carries it
	// without a shape of its own: the equipment, when it ends, how much
	// of it was left when saved (a restore never leaves more than that: a
	// clock set back must not lengthen it), when and by whom it began, and
	// why.
	// An older binary drops these and shows the alarms (the safe side).
	OOSEquipment string `json:"oosEquipment,omitempty"`
	OOSUntilMs   int64  `json:"oosUntilMs,omitempty"`
	OOSLeftMs    int64  `json:"oosLeftMs,omitempty"`
	OOSStartMs   int64  `json:"oosStartMs,omitempty"`
	OOSBy        string `json:"oosBy,omitempty"`
	OOSReason    string `json:"oosReason,omitempty"`

	// Chatter* is the alarm's chattering episode (#1249), while it lasts:
	// when it began, its activations, and the episodes counted. LastMs is
	// the latest instant the carrier knows the episode was going (a
	// standalone controller: its last qualifying activation; a pair's
	// log: the takeover, as the log is current). A restore ends an
	// episode whose window passed before it, else continues it.
	ChatterSinceMs  int64 `json:"chatterSinceMs,omitempty"`
	ChatterLastMs   int64 `json:"chatterLastMs,omitempty"`
	ChatterActs     int   `json:"chatterActs,omitempty"`
	ChatterEpisodes int   `json:"chatterEpisodes,omitempty"`
}

// AlarmFlood is an alarm flood in progress (#1249), as a restart or a
// takeover carries it. The ring of activations is not carried: the
// controller that continues it counts from the restore, so Total and
// Peak read as at least the carried ones.
type AlarmFlood struct {
	ID      string `json:"id"`
	SinceMs int64  `json:"sinceMs"`
	// OverMs is the latest instant the carrier knows the count was over
	// the threshold (a standalone controller: as its ring had it; a
	// pair's log: the takeover). The flood ends one window after it
	// unless the count goes over again.
	OverMs    int64           `json:"overMs"`
	Total     int             `json:"total,omitempty"`
	Peak      int             `json:"peak,omitempty"`
	Saturated bool            `json:"saturated,omitempty"`
	FirstOut  []AlarmFloodAct `json:"firstOut,omitempty"`
}

// AlarmFloodAct is one of a flood's first alarms: its id, and when it
// activated.
type AlarmFloodAct struct {
	ID string `json:"id"`
	Ms int64  `json:"ms"`
}

// AlarmFloodRetainer is an alarm engine whose flood in progress a
// standalone controller's restart carries (State.AlarmFlood).
type AlarmFloodRetainer interface {
	RetainedFlood() *AlarmFlood
	RestoreFlood(*AlarmFlood)
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

// Carry is own, a pair standby's retained state, with what its leader's
// image carries (#710 S1, design §4.1) put in its place: the leader's
// retained tag values and the parameter bookkeeping read against them,
// its VAR RETAIN locals, its online-edited programs (each with the build
// it was made against, so a load still reapplies one only on that build),
// and its Web Push subscriptions (sealed; a box with another key keeps
// them and sends none, #722).
//
// The rest stays the standby's own:
//   - Alarms, AlarmFlood and Substitutes are operator intent a pair keeps
//     in its replicated log, which a takeover restores them from; taking
//     them from an image could undo a superseded log's drop.
//   - IO: live I/O applies are refused for a pair.
//
// Each carried field is taken whole, absent included: an image without
// online edits clears the standby's, as the leader's own store holds none.
// The result shares lead's maps and pointers: pass a lead decoded for this
// call, and do not change it afterwards.
func Carry(own, lead State) State {
	if mutRetain("stream_carries_log_intent") {
		own.Alarms, own.AlarmFlood, own.Substitutes = lead.Alarms, lead.AlarmFlood, lead.Substitutes
	}
	own.Tags = lead.Tags
	own.ParamBases, own.ParamEdits, own.ParamCommitted = lead.ParamBases, lead.ParamEdits, lead.ParamCommitted
	own.Locals = lead.Locals
	own.Programs, own.ProgramBases, own.DiscardedPrograms = lead.Programs, lead.ProgramBases, lead.DiscardedPrograms
	own.Push = lead.Push
	return own
}
