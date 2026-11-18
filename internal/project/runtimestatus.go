package project

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// The `type: runtime` status driver (#850): the runtime's own health as
// ordinary input tags, so an alarm, the historian or Sparkplug can see a
// task whose real-time priority was withdrawn, a task faulting every scan,
// or lateness piling up.
//
//	drivers:
//	  - type: memory
//	  - name: health
//	    type: runtime
//	    tags:
//	      - {name: Main__Demoted, task: main, status: demoted}
//
// The runtime hands the driver its Stats when it goes live
// (runtime.StatsReader); the driver polls them on its own goroutine, never
// from ReadInputs. Every binding reads false / 0 from construction, so an
// unbound or unstarted driver (naut check, naut test, the first scan) never
// leaves an input undelivered. A standby never scans, so these tags move
// only on the replica that leads: each reports its own runtime while it
// does. They are not the project's I/O: alone, the driver carries the
// memory loopback a project with no driver gets (statusOverMemory), and in
// a drivers: set it sits beside the field drivers.

// RuntimeStatusConfig is one binding of a `type: runtime` driver.
type RuntimeStatusConfig struct {
	Name   string `yaml:"name"`
	Task   string `yaml:"task"`   // "main", or another task's name
	Status string `yaml:"status"` // see runtimeStatuses
}

// runtimeStatuses: what each status reads, and the IEC type it delivers.
var runtimeStatuses = map[string]string{
	"demoted":      "BOOL",  // the task's real-time priority was withdrawn (until restart)
	"faulting":     "BOOL",  // its last scan raised a logic error
	"logic-errors": "UDINT", // logic errors since start
	"late":         "UDINT", // scans that woke later than the late threshold
	"overruns":     "UDINT", // scans whose execution exceeded the period
}

const defaultRuntimeStatusRate = time.Second

type runtimeStatus struct {
	binds []RuntimeStatusConfig
	rate  time.Duration

	mu    sync.Mutex
	stats func() runtime.ScanStats
	vals  nio.Values
	// missing names a bound task the stats do not carry (a check that
	// passed should make this impossible); its tags read Bad.
	missing map[string]bool

	cancel context.CancelFunc
	done   chan struct{}
}

var (
	_ nio.Driver          = (*runtimeStatus)(nil)
	_ runtime.StatsReader = (*runtimeStatus)(nil)
	_ nio.QualityReporter = (*runtimeStatus)(nil)
	_ interface{ Stop() } = (*runtimeStatus)(nil)
	_ inputTyper          = (*runtimeStatus)(nil)
)

// statusTagsOnlyOnRuntime refuses tags: on any other driver: the key is
// the status driver's, and another driver would silently ignore it.
func statusTagsOnlyOnRuntime(d DriverConfig) error {
	if len(d.Tags) > 0 && !strings.EqualFold(d.Type, "runtime") {
		t := d.Type
		if t == "" {
			t = "memory"
		}
		return fmt.Errorf("driver %s: tags: binds a type: runtime status driver's inputs; a %s driver's tags come from its manifest or the tag files", t, t)
	}
	return nil
}

func newRuntimeStatus(d DriverConfig) (*runtimeStatus, error) {
	if len(d.Tags) == 0 {
		return nil, fmt.Errorf("driver runtime: tags: is empty: bind at least one {name, task, status}")
	}
	var errs []string
	seen := map[string]bool{}
	for i, b := range d.Tags {
		switch {
		case b.Name == "":
			errs = append(errs, fmt.Sprintf("tags[%d]: name is required", i))
		case seen[strings.ToLower(b.Name)]:
			errs = append(errs, fmt.Sprintf("tags[%d]: %s is bound twice", i, b.Name))
		}
		seen[strings.ToLower(b.Name)] = true
		if b.Task == "" {
			errs = append(errs, fmt.Sprintf("tags[%d] (%s): task is required (main, or a task's name)", i, b.Name))
		}
		if _, ok := runtimeStatuses[b.Status]; !ok {
			errs = append(errs, fmt.Sprintf("tags[%d] (%s): status %q (want demoted, faulting, logic-errors, late or overruns)", i, b.Name, b.Status))
		}
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("driver runtime: %s", strings.Join(errs, "; "))
	}
	rate := time.Duration(d.ScanRate)
	if rate <= 0 {
		rate = defaultRuntimeStatusRate
	}
	r := &runtimeStatus{binds: d.Tags, rate: rate, vals: nio.Values{}}
	for _, b := range d.Tags {
		r.vals[b.Name] = zeroStatus(b.Status)
	}
	return r, nil
}

func zeroStatus(status string) any {
	if runtimeStatuses[status] == "BOOL" {
		return false
	}
	return int64(0)
}

// checkTasks refuses a binding to a task the project does not have; tasks
// are the resolved names (main first).
func (r *runtimeStatus) checkTasks(tasks []string) error {
	known := map[string]bool{}
	for _, t := range tasks {
		known[strings.ToLower(t)] = true
	}
	var errs []string
	for _, b := range r.binds {
		if !known[strings.ToLower(b.Task)] {
			errs = append(errs, fmt.Sprintf("%s: task %q (the project's tasks: %s)", b.Name, b.Task, strings.Join(tasks, ", ")))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("driver runtime: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (r *runtimeStatus) InputNames() []string {
	out := make([]string, len(r.binds))
	for i, b := range r.binds {
		out[i] = b.Name
	}
	return out
}

func (r *runtimeStatus) OutputNames() []string { return nil }

func (r *runtimeStatus) InputType(name string) string {
	for _, b := range r.binds {
		if strings.EqualFold(b.Name, name) {
			return runtimeStatuses[b.Status]
		}
	}
	return ""
}

// ReadStatsFrom is runtime.StatsReader; the next poll reads them.
func (r *runtimeStatus) ReadStatsFrom(stats func() runtime.ScanStats) {
	r.mu.Lock()
	r.stats = stats
	r.mu.Unlock()
}

func (r *runtimeStatus) Start(ctx context.Context) {
	ctx, r.cancel = context.WithCancel(ctx)
	r.done = make(chan struct{})
	go func() {
		defer close(r.done)
		t := time.NewTicker(r.rate)
		defer t.Stop()
		for {
			r.poll()
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

func (r *runtimeStatus) Stop() {
	if r.cancel != nil {
		r.cancel()
		<-r.done
	}
}

// poll reads the runtime's stats once (outside r.mu: Stats takes the
// runtime's own locks) and folds them into the values.
func (r *runtimeStatus) poll() {
	r.mu.Lock()
	stats := r.stats
	r.mu.Unlock()
	if stats == nil {
		return
	}
	vals, missing := statusValues(r.binds, stats())
	r.mu.Lock()
	r.vals, r.missing = vals, missing
	r.mu.Unlock()
}

// statusValues maps one ScanStats onto the bindings. A task the stats do
// not carry reads false / 0 and is named in missing.
func statusValues(binds []RuntimeStatusConfig, s runtime.ScanStats) (nio.Values, map[string]bool) {
	type task struct {
		demoted, faulting      bool
		errors, late, overruns uint64
	}
	tasks := map[string]task{
		runtime.MainTaskName: {s.Sched.Demoted, s.Faulting, s.LogicErrors, s.Lateness.Late, s.Lateness.Overruns},
	}
	for _, t := range s.Tasks {
		tasks[strings.ToLower(t.Name)] = task{t.Sched.Demoted, t.Faulting, t.LogicErrors, t.Lateness.Late, t.Lateness.Overruns}
	}
	vals := nio.Values{}
	var missing map[string]bool
	for _, b := range binds {
		t, ok := tasks[strings.ToLower(b.Task)]
		if !ok {
			if missing == nil {
				missing = map[string]bool{}
			}
			missing[b.Name] = true
			vals[b.Name] = zeroStatus(b.Status)
			continue
		}
		switch b.Status {
		case "demoted":
			vals[b.Name] = t.demoted
		case "faulting":
			vals[b.Name] = t.faulting
		case "logic-errors":
			vals[b.Name] = int64(t.errors)
		case "late":
			vals[b.Name] = int64(t.late)
		case "overruns":
			vals[b.Name] = int64(t.overruns)
		}
	}
	return vals, missing
}

func (r *runtimeStatus) ReadInputs() (nio.Values, error) {
	out := make(nio.Values, len(r.binds))
	return out, r.ReadInputsInto(out)
}

// ReadInputsInto is the scan's read with no allocation (io.BatchReader):
// every binding is always delivered, so dst never holds a stale key of
// ours to delete.
func (r *runtimeStatus) ReadInputsInto(dst nio.Values) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, v := range r.vals {
		dst[k] = v
	}
	return nil
}

func (r *runtimeStatus) WriteOutputs(vals nio.Values) error {
	if len(vals) > 0 {
		return fmt.Errorf("driver runtime: it has no outputs (asked to write %d)", len(vals))
	}
	return nil
}

// Quality: Good, the runtime being its own source, except a binding whose
// task the stats do not carry.
func (r *runtimeStatus) Quality() map[string]nio.Quality {
	r.mu.Lock()
	defer r.mu.Unlock()
	q := make(map[string]nio.Quality, len(r.binds))
	for _, b := range r.binds {
		if r.missing[b.Name] {
			q[b.Name] = nio.Bad
		} else {
			q[b.Name] = nio.Good
		}
	}
	return q
}

// pairRuntimeStatus is Load's step for every `type: runtime` driver in d:
// its tasks must exist (tasks: the resolved names, main first), and one
// that is the project's only driver gets the memory loopback a project
// with no driver has, since it carries no I/O of its own (io.Memory
// cannot join a drivers: set: it owns no tag names). A drivers: set of
// status drivers alone has no I/O at all and is refused.
func pairRuntimeStatus(d nio.Driver, tasks []string) (nio.Driver, error) {
	if rs, alone := d.(*runtimeStatus); alone {
		if err := rs.checkTasks(tasks); err != nil {
			return nil, err
		}
		return &statusOverMemory{Memory: nio.NewMemory(), status: rs}, nil
	}
	m, ok := d.(*nio.Multi)
	if !ok {
		return d, nil
	}
	io := false
	for _, c := range m.Children() {
		rs, ok := c.Driver.(*runtimeStatus)
		if !ok {
			io = true
			continue
		}
		if err := rs.checkTasks(tasks); err != nil {
			return nil, fmt.Errorf("drivers (%s): %w", c.Name, err)
		}
	}
	if !io {
		return nil, fmt.Errorf("drivers: only type: runtime drivers, so the project has no I/O; bind every status in one type: runtime driver (alone it keeps the memory loopback), or list it beside the field drivers")
	}
	return d, nil
}

// statusOverMemory is a lone `type: runtime` driver: its status inputs
// over the memory loopback, which carries every other tag (outputs read
// back as inputs, as with no driver at all).
type statusOverMemory struct {
	*nio.Memory
	status *runtimeStatus
}

func (s *statusOverMemory) ReadInputs() (nio.Values, error) {
	out := nio.Values{}
	return out, s.ReadInputsInto(out)
}

// ReadInputsInto shadows Memory's, so the status inputs are not skipped.
func (s *statusOverMemory) ReadInputsInto(dst nio.Values) error {
	if err := s.Memory.ReadInputsInto(dst); err != nil {
		return err
	}
	return s.status.ReadInputsInto(dst)
}

func (s *statusOverMemory) Quality() map[string]nio.Quality {
	q := s.Memory.Quality()
	for k, v := range s.status.Quality() {
		q[k] = v
	}
	return q
}

func (s *statusOverMemory) InputType(name string) string { return s.status.InputType(name) }
func (s *statusOverMemory) ReadStatsFrom(stats func() runtime.ScanStats) {
	s.status.ReadStatsFrom(stats)
}
func (s *statusOverMemory) Start(ctx context.Context) { s.status.Start(ctx) }
func (s *statusOverMemory) Stop()                     { s.status.Stop() }

// taskNames is the runtime's task names, main first.
func taskNames(o runtime.Options) []string {
	names := []string{runtime.MainTaskName}
	for _, t := range o.Tasks {
		names = append(names, t.Name)
	}
	return names
}
