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
}

// Facade is a running Siemens-backed nautilus API.
type Facade struct {
	opts     Options
	log      *slog.Logger
	drv      *Driver
	rt       *runtime.Runtime
	srv      *server.Server
	bindings map[string]Binding
	skipped  []string

	mu      sync.Mutex
	lastErr string // the last write failure
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
	bs, skipped, err := Discover(ctx, o.Client, o.Roots)
	if err != nil {
		return nil, err
	}
	if len(bs) == 0 {
		return nil, fmt.Errorf("facade: no tag the facade can bind under %s", rootNames(o.Roots))
	}
	f := &Facade{opts: o, log: o.Log, bindings: map[string]Binding{}, skipped: skipped}
	names := make([]string, 0, len(bs))
	for _, b := range bs {
		f.bindings[b.Name] = b
		names = append(names, b.Name)
	}
	f.drv = NewDriver(o.Client, bs, o.Poll)
	rt, err := runtime.New(runtime.Options{Program: emptyProgram, Driver: f.drv, Inputs: names, Meta: o.Meta})
	if err != nil {
		return nil, err
	}
	f.rt = rt
	sopts := o.Server
	sopts.TagWriter = f.write
	sopts.Drivers = f.driverStatus
	f.srv = server.New(rt, sopts)
	return f, nil
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
	out := make([]Binding, 0, len(f.bindings))
	for _, b := range f.bindings {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Skipped lists what the browse found but could not bind, with why.
func (f *Facade) Skipped() []string { return f.skipped }

// Runtime is the runtime carrying the polled values.
func (f *Facade) Runtime() *runtime.Runtime { return f.rt }

// Run polls the CPU and serves frames until ctx is cancelled.
func (f *Facade) Run(ctx context.Context) {
	go f.drv.Run(ctx)
	go f.rt.Run(ctx)
	f.srv.Run(ctx)
}

// Handler is the nautilus API, with the program endpoints answered for a
// CPU whose program nautilus does not warm-swap.
func (f *Facade) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", f.srv.Handler())
	mux.HandleFunc("GET /api/program", programGone)
	mux.HandleFunc("GET /api/program/history", programGone)
	mux.Handle("PUT /api/program", f.srv.Audited("program.edit", nil, programLocked))
	mux.Handle("POST /api/program/rollback", f.srv.Audited("program.rollback", nil, programLocked))
	mux.Handle("POST /api/program/activate", f.srv.Audited("program.activate", nil, programLocked))
	// The facade's own routes keep the server's Host rule (#819).
	return f.srv.GuardHost(mux)
}

// The messages are what the extension shows.
const (
	msgNoSource = "this is a Siemens CPU: the program it runs was generated from the nautilus project " +
		"and downloaded through TIA Portal; open the project's sources instead"
	msgNoEdit = "this is a Siemens CPU, which nautilus does not warm-swap: change the nautilus " +
		"sources and download them through the Siemens target (the deploy command is not built yet; see issue #789)"
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
func (f *Facade) write(name string, value any) error {
	b, ok := f.bindings[name]
	if !ok {
		return badRequest(fmt.Sprintf("%s is not a tag of this CPU (the facade binds %d)", name, len(f.bindings)))
	}
	v, err := convertWrite(b.Type, value)
	if err != nil {
		return badRequest(fmt.Sprintf("%s: %v", name, err))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
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

func (f *Facade) lastWriteError() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastErr
}

// driverStatus reports the poll, plus the last write's failure: a write
// path that fails while polls succeed would otherwise stay invisible until
// someone tried to set a value.
func (f *Facade) driverStatus() []server.DriverStatus {
	h := f.drv.Health()
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
	if werr := f.lastWriteError(); werr != "" && s.State == "connected" {
		s.State = "degraded"
		s.Message = "Polling, but the last write failed"
		s.LastError = werr
	}
	return []server.DriverStatus{s}
}
