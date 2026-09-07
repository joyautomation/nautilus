package acceptance

// Alarm assertions and the two operator verbs.
//
// This is a deliberate exception to the harness's "no new keys — write an
// ST expression" stance (see spec.go's doc comment). That pressure valve
// works because everything else a test could want to assert lives in the
// tag store and is reachable from ST. Alarm state is NOT in the tag store:
// acknowledgement, shelf and the journal live in the alarm engine, which
// the scan loop does not know about and no ST expression can see. So
// `alarms:` is a sibling of `expect:` rather than a tag matcher inside it
// — `expect:` is a mapping of TAG to matcher, and `expect: {alarms: …}`
// would decode as an assertion about a tag literally named "alarms".
//
// The surface is one assertion key and three verbs, and it is meant to
// stay that way.

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/runtime"
)

// AlarmExpect is a step's `alarms:` key: what the alarm engine must say.
//
// Every field is optional and an absent one asserts nothing. The two set
// fields (Active, Shelved) are EXACT sets, order-insensitive — `active:
// []` means "nothing is annunciating", which is the assertion an on-delay
// test needs before the delay elapses and would be unwritable if an empty
// list meant "don't check".
type AlarmExpect struct {
	// Active is the exact set of ids in an active or unack-RTN state.
	Active []string `yaml:"active"`
	// Unacked is how many alarms are waiting on an operator.
	Unacked *int `yaml:"unacked"`
	// Shelved is the exact set of shelved ids.
	Shelved []string `yaml:"shelved"`
	// State asserts one id's state token: normal, unack-active,
	// ack-active, unack-rtn, shelved, suppressed.
	State map[string]string `yaml:"state"`
	// Priority is the active count by priority, as Summary reports it.
	Priority map[string]int `yaml:"priority"`
	// Journal is the exact sequence of event kinds this test produced, in
	// chronological order. suppress/unsuppress are excluded: they are
	// plumbing (a tag that has not arrived yet), not the operator story a
	// test is about.
	Journal []string `yaml:"journal"`
}

// AckStep is a step's `ack:` verb — an operator acknowledging, exactly as
// POST /api/alarms/ack would.
type AckStep struct {
	IDs []string `yaml:"ids"`
	All bool     `yaml:"all"`
	By  string   `yaml:"by"`
}

// ShelveStep is a step's `shelve:` verb. `for:` is a duration from the
// current VIRTUAL time, so a shelf expires when the test advances past it
// — there is no permanent shelf.
type ShelveStep struct {
	ID  string    `yaml:"id"`
	For *Duration `yaml:"for"`
	By  string    `yaml:"by"`
}

// UnshelveStep is a step's `unshelve:` verb: ending a shelf early, which
// restores the state the shelf interrupted.
type UnshelveStep struct {
	ID string `yaml:"id"`
	By string `yaml:"by"`
}

// AlarmBuilder builds the engine a test runs against, over the test's own
// runtime. internal/project.AlarmEngine is the one every caller passes:
// the manifest's own alarms, an in-memory journal, no notifiers.
//
// Taking a function rather than an engine is what keeps a FRESH engine per
// test — the harness's whole contract is that a test cannot leak state
// into the next one, and an acknowledgement is exactly the kind of state
// that would.
type AlarmBuilder func(*runtime.Runtime) (*alarm.Engine, error)

// Option tunes a run. Variadic and last, so every existing call site keeps
// working unchanged.
type Option func(*runOptions)

type runOptions struct {
	alarms AlarmBuilder
}

// WithAlarms gives a run an alarm engine, which is what makes the
// `alarms:` key and the ack/shelve verbs mean anything. Without it a test
// using them fails with that as the reason, rather than silently passing.
func WithAlarms(b AlarmBuilder) Option { return func(o *runOptions) { o.alarms = b } }

func collect(opts []Option) runOptions {
	var o runOptions
	for _, fn := range opts {
		if fn != nil {
			fn(&o)
		}
	}
	return o
}

// noEngine is the error a test using alarm keys gets when the project has
// none. It names the manifest key, because the mistake is almost always a
// test written ahead of the `alarms:` section rather than a harness
// problem.
func noEngine(what string) error {
	return fmt.Errorf("%s: this project declares no `alarms:` section, so there is no alarm "+
		"engine for it to be about", what)
}

// ── the verbs ──────────────────────────────────────────────────────────

func (r *testRun) ack(a *AckStep) error {
	if r.alarms == nil {
		return noEngine("ack")
	}
	ids := a.IDs
	if a.All {
		ids = nil // nil means everything, per Engine.Ack
	} else if len(ids) == 0 {
		return fmt.Errorf("ack: needs `ids:` or `all: true` — an empty list acks nothing, " +
			"which is never what a test meant")
	}
	if _, err := r.alarms.Ack(ids, a.By); err != nil {
		return fmt.Errorf("ack: %w", err)
	}
	return nil
}

func (r *testRun) shelve(s *ShelveStep) error {
	if r.alarms == nil {
		return noEngine("shelve")
	}
	if s.ID == "" {
		return fmt.Errorf("shelve: needs `id:`")
	}
	if s.For == nil || s.For.get() <= 0 {
		return fmt.Errorf("shelve %s: needs `for:` — a positive duration from now, since "+
			"there is no permanent shelf", s.ID)
	}
	// Virtual time, not wall time: the shelf must expire when the test
	// advances past it, which is the whole reason a shelf is testable here.
	until := r.now().Add(s.For.get())
	if err := r.alarms.Shelve(s.ID, until, s.By); err != nil {
		return fmt.Errorf("shelve: %w", err)
	}
	return nil
}

func (r *testRun) unshelve(u *UnshelveStep) error {
	if r.alarms == nil {
		return noEngine("unshelve")
	}
	if u.ID == "" {
		return fmt.Errorf("unshelve: needs `id:`")
	}
	if err := r.alarms.Unshelve(u.ID, u.By); err != nil {
		return fmt.Errorf("unshelve: %w", err)
	}
	return nil
}

// now is the harness's virtual clock as the engine sees it.
func (r *testRun) now() time.Time { return time.UnixMilli(r.rt.Tags().NowMs()) }

// ── the assertion ──────────────────────────────────────────────────────

// checkAlarms evaluates one `alarms:` block. Same contract as check():
// (ok, detail, err), where detail renders the first failing term.
func (r *testRun) checkAlarms(a *AlarmExpect) (bool, string, error) {
	if a == nil {
		return true, "", nil
	}
	if r.alarms == nil {
		return false, "", noEngine("alarms")
	}
	byID := map[string]alarm.Record{}
	for _, rec := range r.alarms.Records() {
		byID[rec.ID] = rec
	}
	sum := r.alarms.Summary()

	if a.Active != nil {
		var got []string
		for id, rec := range byID {
			// Active means "wanting attention": shelved is deliberately
			// not, or shelving would be a way to fail an active: check by
			// silencing the alarm rather than fixing it.
			if rec.State.Annunciating() && rec.State != alarm.Shelved {
				got = append(got, id)
			}
		}
		if ok, detail := sameSet("active", a.Active, got); !ok {
			return false, detail, nil
		}
	}
	if a.Shelved != nil {
		var got []string
		for id, rec := range byID {
			if rec.State == alarm.Shelved {
				got = append(got, id)
			}
		}
		if ok, detail := sameSet("shelved", a.Shelved, got); !ok {
			return false, detail, nil
		}
	}
	if a.Unacked != nil && sum.Unacked != *a.Unacked {
		return false, fmt.Sprintf("alarms.unacked = %d, want %d", sum.Unacked, *a.Unacked), nil
	}
	for _, id := range sortedStrings(a.State) {
		want := a.State[id]
		if _, err := alarm.ParseState(want); err != nil {
			return false, "", fmt.Errorf("alarms.state[%s]: %w", id, err)
		}
		rec, ok := byID[id]
		if !ok {
			return false, "", fmt.Errorf("alarms.state: no alarm %q is defined (ids: %s)",
				id, strings.Join(sortedRecords(byID), ", "))
		}
		if rec.State.String() != want {
			return false, fmt.Sprintf("alarms.state[%s] = %s, want %s", id, rec.State, want), nil
		}
	}
	for _, p := range sortedInts(a.Priority) {
		if got := sum.ByPriority[p]; got != a.Priority[p] {
			return false, fmt.Sprintf("alarms.priority[%s] = %d, want %d", p, got, a.Priority[p]), nil
		}
	}
	if a.Journal != nil {
		got, err := r.journalKinds()
		if err != nil {
			return false, "", err
		}
		if !equalStrings(got, a.Journal) {
			return false, fmt.Sprintf("alarms.journal = [%s], want [%s]",
				strings.Join(got, " "), strings.Join(a.Journal, " ")), nil
		}
	}
	return true, "", nil
}

// journalKinds is every event this test produced, oldest first, minus the
// suppress/unsuppress pair. The engine is fresh per test, so "this test's
// events" and "every event in the journal" are the same set.
func (r *testRun) journalKinds() ([]string, error) {
	events, err := r.alarms.Journal(time.Time{}, time.Time{}, alarm.Filter{Limit: 10000})
	if err != nil {
		return nil, fmt.Errorf("alarms.journal: %w", err)
	}
	out := make([]string, 0, len(events))
	// Query returns newest first; a journal assertion reads chronologically.
	for i := len(events) - 1; i >= 0; i-- {
		switch events[i].Kind {
		case alarm.KindSuppress, alarm.KindUnsuppress:
			continue
		}
		out = append(out, events[i].Kind)
	}
	return out, nil
}

// sameSet compares two id sets order-insensitively and renders the
// difference as what is missing and what is extra — the two questions a
// failing set assertion is actually asking.
func sameSet(what string, want, got []string) (bool, string) {
	w, g := append([]string(nil), want...), append([]string(nil), got...)
	sort.Strings(w)
	sort.Strings(g)
	if equalStrings(w, g) {
		return true, ""
	}
	inG := map[string]bool{}
	for _, s := range g {
		inG[s] = true
	}
	inW := map[string]bool{}
	for _, s := range w {
		inW[s] = true
	}
	var missing, extra []string
	for _, s := range w {
		if !inG[s] {
			missing = append(missing, s)
		}
	}
	for _, s := range g {
		if !inW[s] {
			extra = append(extra, s)
		}
	}
	msg := fmt.Sprintf("alarms.%s = [%s], want [%s]", what, strings.Join(g, " "), strings.Join(w, " "))
	if len(missing) > 0 {
		msg += "\n  missing: " + strings.Join(missing, " ")
	}
	if len(extra) > 0 {
		msg += "\n  unexpected: " + strings.Join(extra, " ")
	}
	return false, msg
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sortedStrings(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedInts(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedRecords(m map[string]alarm.Record) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
