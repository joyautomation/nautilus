// Package facade makes a running Siemens S7-1500 answer the nautilus
// runtime's HTTP API, so the VS Code extension, the dashboard and any HMI
// on the hmi kit treat it as a nautilus controller: live values, hover,
// the Live Values panel, set-value and the ladder overlay
// (docs/design/siemens-ide.md §4). It is `naut siemens serve`, the Siemens
// twin of logix/facade, and it reaches the CPU over the PLC Web API from
// pure Go: no Siemens software is involved.
//
// The pieces are the ones the Logix facade uses, with the Web API in place
// of EtherNet/IP:
//
//   - The tag set is discovered by browsing named roots: the global tags DB
//     as plain names ("Level"), a program's instance DB as that program's
//     variables ("Main.count"). These are the names siemens/writer gives
//     the same values, so the overlay on the nautilus source resolves.
//   - A Driver polls every bound symbol with ReadMany into a snapshot; the
//     runtime runs no logic and only carries the snapshot to the server.
//   - A write goes to the CPU (server.Options.TagWriter), not to the store;
//     the next poll brings it back, which is the proof it landed.
//   - Force is refused (501): the Web API writes but does not force, and
//     the opt-in override layer is a later item (D4).
//
// This package is an application layer (the context map lets it compose
// the runtime and the server); the Web API's names stop at siemens/webapi.
package facade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/server"
	"github.com/joyautomation/nautilus/siemens/webapi"
)

// Root is one place in the CPU to bind tags from.
type Root struct {
	// DB is the data block's name, unquoted: "naut_Tags", "naut_Main_DB".
	DB string
	// Prefix is the nautilus scope its members appear under: "" for the
	// global tags DB, "Main" for the instance DB of program Main.
	Prefix string
}

// Options configure a facade over one CPU.
type Options struct {
	// Client reaches the CPU's Web API (siemens/webapi, already
	// configured with the user and the certificate pin).
	Client *webapi.Client
	// Host names the CPU in the driver status; it is not dialled here.
	Host string
	// Roots are browsed for tags. Default: the global DB "naut_Tags".
	Roots []Root
	// Known, when set, says which nautilus names the facade binds: a
	// member the browse finds that Known refuses is left out without a
	// word (the writer's own bookkeeping, such as a timer's "<t>_last"
	// static). Nil binds everything the browse finds.
	Known func(name string) bool
	// Reload, when set, is asked for the roots and Known again before
	// every re-discovery, so a project edited since serve started binds
	// its new names. An error keeps the last ones.
	Reload func() ([]Root, func(string) bool, error)
	// Rediscover is how often the CPU is browsed again, so a download
	// that changed the program's data layout is picked up without a
	// restart. Default 15 s; negative turns the timer off (a write or a
	// read that finds a name gone or retyped still asks for one).
	Rediscover time.Duration
	// Settle is how long a re-discovery asked for by Kick waits first, so
	// a CPU in the middle of a download is not browsed half loaded.
	// Default 2 s.
	Settle time.Duration
	// Poll is the poll period. Default 250 ms.
	Poll   time.Duration
	Meta   map[string]runtime.TagMeta
	Server server.Options // its TagWriter and Drivers are replaced
	Log    *slog.Logger
}

// Binding is one nautilus tag bound to a CPU symbol.
type Binding struct {
	Name   string // nautilus name: "Level", "Main.count", "Main.dwell.Q"
	Symbol string // Web API symbol: "\"naut_Tags\".Level"
	Type   string // IEC type: BOOL, INT, REAL, …
	// Parent is the symbol browsed to find it, and Member its name there:
	// a write browses Parent again to check the CPU still has Member, of
	// the same type, before it sends anything.
	Parent, Member string
}

// Facade is a running Siemens-backed nautilus API. Its binding set (the
// driver, the runtime and the API server over them) is replaced as a
// whole when a re-discovery finds the CPU's data layout changed: the
// runtime's tag set is fixed when it is built.
type Facade struct {
	opts Options
	log  *slog.Logger

	mu      sync.Mutex
	cur     *binder
	roots   []Root
	known   func(string) bool
	lastErr string // the last write failure
	note    string // the last re-discovery's outcome, for the status panel

	kick    chan struct{} // asks Run for a re-discovery now
	rebinds int
}

// binder is one binding set and what serves it.
type binder struct {
	bindings map[string]Binding
	skipped  []string
	drv      *Driver
	rt       *runtime.Runtime
	srv      *server.Server
	handler  http.Handler
	ctx      context.Context // ends when the set is replaced
	cancel   context.CancelFunc
}

// emptyProgram is the runtime's program: the CPU runs the logic.
const emptyProgram = "PROGRAM Siemens\nEND_PROGRAM\n"

// New browses the roots and builds the facade. It does not start polling;
// Run does. The browse is the one step that must reach the CPU up front,
// because the tag set comes from it.
func New(ctx context.Context, o Options) (*Facade, error) {
	if o.Client == nil {
		return nil, errors.New("facade: a Web API client is required")
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if len(o.Roots) == 0 {
		o.Roots = []Root{{DB: "naut_Tags"}} // the writer's default block prefix
	}
	if o.Poll <= 0 {
		o.Poll = 250 * time.Millisecond
	}
	if o.Rediscover == 0 {
		o.Rediscover = 15 * time.Second
	}
	if o.Settle <= 0 {
		o.Settle = 2 * time.Second
	}
	f := &Facade{opts: o, log: o.Log, roots: o.Roots, known: o.Known, kick: make(chan struct{}, 1)}
	bs, skipped, err := f.discover(ctx, o.Roots, o.Known)
	if err != nil {
		return nil, err
	}
	b, err := f.build(bs, skipped)
	if err != nil {
		return nil, err
	}
	f.cur = b
	return f, nil
}

// discover browses roots and keeps what known allows.
func (f *Facade) discover(ctx context.Context, roots []Root, known func(string) bool) ([]Binding, []string, error) {
	bs, skipped, err := Discover(ctx, f.opts.Client, roots)
	if err != nil {
		return nil, nil, err
	}
	if known != nil {
		kept := bs[:0]
		for _, b := range bs {
			if known(b.Name) {
				kept = append(kept, b)
			}
		}
		bs = kept
	}
	if len(bs) == 0 {
		return nil, nil, fmt.Errorf("facade: no tag the facade can bind under %s", rootNames(roots))
	}
	return bs, skipped, nil
}

// build makes the driver, runtime and server over one binding set.
func (f *Facade) build(bs []Binding, skipped []string) (*binder, error) {
	b := &binder{bindings: map[string]Binding{}, skipped: skipped}
	names := make([]string, 0, len(bs))
	for _, x := range bs {
		b.bindings[x.Name] = x
		names = append(names, x.Name)
	}
	b.drv = NewDriver(f.opts.Client, bs, f.opts.Poll)
	b.drv.onGone = f.Kick
	rt, err := runtime.New(runtime.Options{Program: emptyProgram, Driver: b.drv, Inputs: names, Meta: f.opts.Meta})
	if err != nil {
		return nil, err
	}
	b.rt = rt
	sopts := f.opts.Server
	sopts.TagWriter = f.write
	sopts.Drivers = f.driverStatus
	b.srv = server.New(rt, sopts)
	mux := http.NewServeMux()
	mux.Handle("/", b.srv.Handler())
	mux.HandleFunc("GET /api/program", programGone)
	mux.HandleFunc("GET /api/program/history", programGone)
	mux.Handle("PUT /api/program", b.srv.Audited("program.edit", nil, programLocked))
	mux.Handle("POST /api/program/rollback", b.srv.Audited("program.rollback", nil, programLocked))
	mux.Handle("POST /api/program/activate", b.srv.Audited("program.activate", nil, programLocked))
	// The facade's own routes keep the server's Host rule (#819).
	b.handler = b.srv.GuardHost(mux)
	b.ctx, b.cancel = context.WithCancel(context.Background())
	return b, nil
}

func (f *Facade) current() *binder {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cur
}

// Kick asks Run for a re-discovery now (a write or a read found a name
// gone or retyped). It never blocks.
func (f *Facade) Kick() {
	select {
	case f.kick <- struct{}{}:
	default:
	}
}

// RootsFromWriter turns siemens/writer's Output.Roots (a nautilus scope →
// a quoted DB name: "" → `"naut_Tags"`, "Main" → `"naut_Main_DB"`) into the roots to
// browse, so serve binds exactly the names the writer generated. The order
// is stable: the tags DB first, then the programs by name.
func RootsFromWriter(roots map[string]string) []Root {
	var out []Root
	var progs []string
	for scope := range roots {
		if scope != "" {
			progs = append(progs, scope)
		}
	}
	sort.Strings(progs)
	if db, ok := roots[""]; ok {
		out = append(out, Root{DB: strings.Trim(db, `"`)})
	}
	for _, p := range progs {
		out = append(out, Root{DB: strings.Trim(roots[p], `"`), Prefix: p})
	}
	return out
}

func rootNames(rs []Root) string {
	var ns []string
	for _, r := range rs {
		ns = append(ns, `"`+r.DB+`"`)
	}
	return strings.Join(ns, ", ")
}

// Bindings lists the bound tags, sorted by name.
func (f *Facade) Bindings() []Binding {
	b := f.current()
	out := make([]Binding, 0, len(b.bindings))
	for _, x := range b.bindings {
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Skipped lists what the browse found but could not bind, with why.
func (f *Facade) Skipped() []string { return f.current().skipped }

// Runtime is the runtime carrying the polled values (the current binding
// set's: a re-binding replaces it).
func (f *Facade) Runtime() *runtime.Runtime { return f.current().rt }

// Rebinds counts the re-bindings since New.
func (f *Facade) Rebinds() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rebinds
}

// Run polls the CPU and serves frames until ctx is cancelled, and browses
// the CPU again every Options.Rediscover (and whenever Kick asks): when
// the binding set it finds differs, the set is rebuilt. Open /api/stream
// connections end at a re-binding, so a client reconnects to the new set.
func (f *Facade) Run(ctx context.Context) {
	var tick <-chan time.Time
	if f.opts.Rediscover > 0 {
		t := time.NewTicker(f.opts.Rediscover)
		defer t.Stop()
		tick = t.C
	}
	b := f.current()
	done := runSet(b)
	for {
		select {
		case <-ctx.Done():
			b.cancel()
			<-done
			return
		case <-tick:
		case <-f.kick:
			select {
			case <-ctx.Done():
				b.cancel()
				<-done
				return
			case <-time.After(f.opts.Settle):
			}
			select { // the kicks that came while settling are this one
			case <-f.kick:
			default:
			}
		}
		bs, skipped, changed := f.rediscover(ctx, b)
		if !changed {
			continue
		}
		// Stop the running set and wait for it (its server's Run returns
		// after the audit upkeep flushed) before the new one is built, so
		// two servers never share the journal.
		b.cancel()
		<-done
		nb, err := f.build(bs, skipped)
		if err != nil {
			f.setNote("re-binding failed, the last binding set is rebuilt: " + err.Error())
			if nb, err = f.build(sortedBindings(b.bindings), b.skipped); err != nil {
				f.setNote("re-binding failed twice, serve stops: " + err.Error())
				return
			}
		}
		added, removed := diffBindings(b.bindings, nb.bindings)
		f.mu.Lock()
		f.cur = nb
		f.rebinds++
		f.note = fmt.Sprintf("the CPU's program changed: re-bound %d tags (%d new, %d gone)", len(nb.bindings), added, removed)
		f.mu.Unlock()
		f.log.Info("siemens facade: re-bound", "tags", len(nb.bindings), "new", added, "gone", removed)
		b = nb
		done = runSet(b)
	}
}

// runSet runs one binding set; done closes once all of it has stopped
// (the server's Run returns after its audit upkeep has flushed).
func runSet(b *binder) <-chan struct{} {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); b.drv.Run(b.ctx) }()
	go func() { defer wg.Done(); b.rt.Run(b.ctx) }()
	go func() { defer wg.Done(); b.srv.Run(b.ctx) }()
	go func() { wg.Wait(); close(done) }()
	return done
}

// rediscover browses the CPU again and reports whether the binding set
// differs from b's. A failed or empty browse (the CPU in STOP during a
// download, say) keeps the running set.
func (f *Facade) rediscover(ctx context.Context, b *binder) ([]Binding, []string, bool) {
	f.mu.Lock()
	roots, known := f.roots, f.known
	f.mu.Unlock()
	if f.opts.Reload != nil {
		if r, k, err := f.opts.Reload(); err != nil {
			f.setNote("re-discovery kept the last binding plan: " + err.Error())
		} else {
			roots, known = r, k
			f.mu.Lock()
			f.roots, f.known = r, k
			f.mu.Unlock()
		}
	}
	bctx, cancel := context.WithTimeout(ctx, time.Minute)
	bs, skipped, err := f.discover(bctx, roots, known)
	cancel()
	if err != nil {
		f.setNote("re-discovery failed, the last binding set is kept: " + err.Error())
		return nil, nil, false
	}
	f.mu.Lock()
	if strings.HasPrefix(f.note, "re-") && !strings.HasPrefix(f.note, "re-bound") {
		f.note = "" // a failure the browse just got past
	}
	f.mu.Unlock()
	return bs, skipped, !sameBindings(b.bindings, bs)
}

func (f *Facade) setNote(s string) {
	f.mu.Lock()
	same := f.note == s
	f.note = s
	f.mu.Unlock()
	if !same {
		f.log.Warn("siemens facade: " + s)
	}
}

// sameBindings reports whether bs is exactly the set held.
func sameBindings(held map[string]Binding, bs []Binding) bool {
	if len(held) != len(bs) {
		return false
	}
	for _, b := range bs {
		if h, ok := held[b.Name]; !ok || h.Symbol != b.Symbol || h.Type != b.Type {
			return false
		}
	}
	return true
}

func sortedBindings(m map[string]Binding) []Binding {
	out := make([]Binding, 0, len(m))
	for _, b := range m {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func diffBindings(old, now map[string]Binding) (added, removed int) {
	for n := range now {
		if _, ok := old[n]; !ok {
			added++
		}
	}
	for n := range old {
		if _, ok := now[n]; !ok {
			removed++
		}
	}
	return added, removed
}

// Handler is the nautilus API, with the program endpoints answered for a
// CPU whose program nautilus does not warm-swap. It serves whichever
// binding set is current; a request still open when the set is replaced
// (an /api/stream) is ended, so the client reconnects to the new one.
func (f *Facade) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := f.current()
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		stop := context.AfterFunc(b.ctx, cancel)
		defer stop()
		b.handler.ServeHTTP(w, r.WithContext(ctx))
	})
}

// The messages are what the extension shows.
const (
	msgNoSource = "this is a Siemens CPU: the program it runs was generated from the nautilus project " +
		"and downloaded through TIA Portal; open the project's sources instead"
	msgNoEdit = "this is a Siemens CPU, which nautilus does not warm-swap: change the nautilus " +
		"sources and download them with naut siemens deploy"
)

func programGone(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotFound, msgNoSource)
}

func programLocked(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusForbidden, msgNoEdit)
}

func writeJSON(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	b, _ := json.Marshal(map[string]string{"error": msg})
	w.Write(append(b, '\n'))
}

// ── writes ──────────────────────────────────────────────────────────────

// badRequest is a write that could never succeed, as opposed to one the
// CPU failed; the server answers it 400.
type badRequest string

func (e badRequest) Error() string   { return string(e) }
func (e badRequest) HTTPStatus() int { return http.StatusBadRequest }

// write sends one value to the CPU. The value is checked against the tag's
// type here, so a mistyped write is the caller's 400, not a CPU error.
//
// Before anything is sent, the binding's parent is browsed again (the same
// children browse discovery uses) to check the CPU still has the member,
// of the same type: a download since serve bound it may have removed or
// retyped it. Gone, unreadable or no longer bindable is refused; retyped
// is checked against and sent as the CPU's type now, never the old one.
// Either way a re-discovery is asked for.
func (f *Facade) write(name string, value any) error {
	b, ok := f.current().bindings[name]
	if !ok {
		return badRequest(fmt.Sprintf("%s is not a tag of this CPU (the facade binds %d)", name, len(f.current().bindings)))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t, err := f.typeNow(ctx, b)
	if err != nil {
		return err
	}
	v, err := convertWrite(t, value)
	if err != nil {
		return badRequest(fmt.Sprintf("%s: %v", name, err))
	}
	err = f.opts.Client.Write(ctx, b.Symbol, v)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err != nil {
		switch webapi.Code(err) {
		case webapi.CodeAddressIsReadOnly, webapi.CodeInvalidParams, webapi.CodeAddressDoesNotExist, webapi.CodePermissionDenied:
			// The caller asked for something the CPU will never do: a
			// 400, and not a sign the write path is failing.
			return badRequest(fmt.Sprintf("%s: %v", name, err))
		}
		f.lastErr = fmt.Sprintf("write %s: %v", b.Symbol, err)
		return err
	}
	f.lastErr = ""
	return nil
}

// typeNow is the IEC type the CPU holds b's member as now, browsed from
// its parent. It fails closed: a browse that does not answer, a member
// that is gone, or one the facade cannot bind any more refuses the write.
func (f *Facade) typeNow(ctx context.Context, b Binding) (string, error) {
	if b.Parent == "" {
		return "", badRequest(fmt.Sprintf("%s: the facade cannot check this tag on the CPU before writing it", b.Name))
	}
	vs, err := f.opts.Client.Browse(ctx, b.Parent, false)
	if err != nil {
		if webapi.Code(err) == webapi.CodeAddressDoesNotExist {
			f.Kick()
			return "", badRequest(f.changedMsg(b.Name, "is no longer on the CPU"))
		}
		return "", fmt.Errorf("%s: not written: the facade could not check it on the CPU first: %w", b.Name, err)
	}
	for _, v := range vs {
		if v.Name != b.Member {
			continue
		}
		t, ok := types[strings.ToLower(v.Datatype)]
		switch {
		case v.HasChildren || strings.EqualFold(v.Datatype, "struct") || len(v.ArrayDimensions) > 0 || !ok:
			f.Kick()
			return "", badRequest(f.changedMsg(b.Name, fmt.Sprintf("is %s on the CPU now, which the facade does not write", v.Datatype)))
		case t != b.Type:
			f.Kick() // written as what the CPU holds now; the set is re-bound
		}
		return t, nil
	}
	f.Kick()
	return "", badRequest(f.changedMsg(b.Name, "is no longer on the CPU"))
}

// changedMsg says a bound name changed on the CPU, and how serve catches
// up: by itself at the next re-discovery, or by a restart.
func (f *Facade) changedMsg(name, what string) string {
	fix := "restart naut siemens serve to bind the CPU's program as it is now"
	if f.opts.Rediscover > 0 {
		fix = fmt.Sprintf("serve binds the CPU's program as it is now by itself within %s (or restart it)", f.opts.Rediscover)
	}
	return fmt.Sprintf("%s: not written: %s: the CPU's program changed since serve bound it (a download?); %s", name, what, fix)
}

func (f *Facade) lastWriteError() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastErr
}

// driverStatus reports the poll, plus the last write's failure: a write
// path that fails while polls succeed would otherwise stay invisible until
// someone tried to set a value.
func (f *Facade) driverStatus() []server.DriverStatus {
	h := f.current().drv.Health()
	s := server.DriverStatus{
		Kind:      "s7-webapi",
		Name:      f.opts.Host,
		Detail:    fmt.Sprintf("%s · PLC Web API · Siemens facade", f.opts.Host),
		LastError: h.LastError,
		SinceMs:   h.SinceMs,
	}
	switch {
	case h.Connected:
		s.State = "connected"
		s.Message = fmt.Sprintf("Polling %d tags", h.Tags)
		if h.Failing > 0 {
			s.State = "degraded"
			s.Message = fmt.Sprintf("Polling %d tags, %d failing", h.Tags, h.Failing)
		}
	case h.LastError != "":
		s.State = "error"
		s.Message = "Web API unreachable — retrying"
	default:
		s.State = "connecting"
		s.Message = "Connecting to the CPU"
	}
	f.mu.Lock()
	note := f.note
	f.mu.Unlock()
	if note != "" && s.State == "connected" {
		s.Message += " · " + note
	}
	if werr := f.lastWriteError(); werr != "" && s.State == "connected" {
		s.State = "degraded"
		s.Message = "Polling, but the last write failed"
		s.LastError = werr
	}
	return []server.DriverStatus{s}
}
