// Package facade makes a running Allen-Bradley Logix controller answer the
// nautilus runtime's HTTP API, so the VS Code extension, the dashboard and
// any HMI built on the hmi kit treat it as a nautilus controller: live
// values, hover, the Live Values panel, set-value, and the ladder preview's
// overlay on an .L5X routine — with no change on the client side.
//
// It is the online plane of docs/design/logix-target.md §4: everything goes
// over EtherNet/IP from pure Go, so no Rockwell software is involved and it
// runs anywhere `naut` does. The pieces are the ones `naut run` already
// uses — eip.Driver polling the controller, a runtime holding the tag
// store, the server package serving it — with three differences:
//
//   - The tag set is discovered, not configured. The controller is browsed
//     at startup and every user tag it can decode is bound (module I/O
//     only on request), named the way `naut eip import` names them.
//   - The runtime runs no logic. Its program is empty; it exists to carry
//     the polled values into the store the server reads.
//   - A write goes to the controller, not to the store (server.Options
//     TagWriter). The next poll brings it back, which is the proof it
//     landed.
//
// The program endpoints answer with what is true of a Logix controller:
// there is no nautilus source to return and nothing to warm-swap, so GET
// /api/program is 404 and the edit endpoints are 403, each naming the
// command that does the Logix version of the job.
package facade

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/eip"
	"github.com/joyautomation/nautilus/eip/codegen"
	"github.com/joyautomation/nautilus/eip/logix"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/server"
)

// Options configure a facade over one controller.
type Options struct {
	Host string
	Slot int
	Port int // EtherNet/IP TCP port; 0 = 44818
	// Tags are path.Match globs against device tag names ("Motor*",
	// "Program:MainProgram.*"). Empty binds every user tag except module
	// I/O, which is how `naut eip import` selects.
	Tags []string
	// Poll is the controller poll rate (default 250ms, the eip driver's).
	Poll time.Duration
	// Meta is tag documentation to serve on /api/meta, keyed by nautilus
	// tag name. A CIP browse cannot recover descriptions; an L5X can.
	Meta map[string]runtime.TagMeta
	// Server is passed through to server.New. Its TagWriter is replaced.
	Server server.Options
	Log    *slog.Logger
}

// Facade is a running Logix-backed nautilus API.
type Facade struct {
	opts    Options
	log     *slog.Logger
	drv     *eip.Driver
	rt      *runtime.Runtime
	srv     *server.Server
	w       *writer
	skipped []string
}

// emptyProgram is the runtime's program: the controller runs the logic.
const emptyProgram = "PROGRAM Logix\nEND_PROGRAM\n"

// New browses the controller and builds the facade. It does not start
// polling; Run does. The browse is the one step that must reach the
// controller up front, because the tag set comes from it.
func New(ctx context.Context, o Options) (*Facade, error) {
	if o.Host == "" {
		return nil, errors.New("facade: a controller host is required")
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	ctrl, err := dial(ctx, o)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", o.Host, err)
	}
	br, err := ctrl.Browse(ctx)
	_ = ctrl.Close()
	if err != nil {
		return nil, fmt.Errorf("browse %s: %w", o.Host, err)
	}
	gen, err := codegen.Generate(br, codegen.Options{Patterns: o.Tags, Host: o.Host, Slot: o.Slot})
	if err != nil {
		return nil, fmt.Errorf("browse %s: %w", o.Host, err)
	}
	m := gen.Manifest

	dopts := []eip.Option{eip.WithSlot(o.Slot), eip.WithLogger(o.Log)}
	if o.Port != 0 {
		dopts = append(dopts, eip.WithPort(o.Port))
	}
	if o.Poll > 0 {
		dopts = append(dopts, eip.WithScanRate(o.Poll))
	}
	drv, err := eip.New(o.Host, m, dopts...)
	if err != nil {
		return nil, err
	}
	rt, err := runtime.New(runtime.Options{
		Program: emptyProgram,
		Driver:  drv,
		Inputs:  drv.InputNames(),
		Meta:    o.Meta,
	})
	if err != nil {
		return nil, err
	}

	f := &Facade{opts: o, log: o.Log, drv: drv, rt: rt, skipped: gen.Skipped}
	f.w = newWriter(o, m)
	sopts := o.Server
	sopts.TagWriter = f.w.write
	sopts.Drivers = f.driverStatus
	f.srv = server.New(rt, sopts)
	return f, nil
}

func dial(ctx context.Context, o Options) (*logix.Controller, error) {
	lopts := []logix.Option{logix.WithSlot(o.Slot), logix.WithLogger(o.Log)}
	if o.Port != 0 {
		lopts = append(lopts, logix.WithPort(o.Port))
	}
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return logix.Dial(dctx, o.Host, lopts...)
}

// Tags is the number of controller tags bound.
func (f *Facade) Tags() int { return len(f.drv.InputNames()) }

// Skipped lists the tags the browse found but could not bind, with why.
func (f *Facade) Skipped() []string { return f.skipped }

// Run polls the controller and serves frames until ctx is cancelled.
func (f *Facade) Run(ctx context.Context) {
	f.drv.Start(ctx)
	defer f.drv.Stop()
	defer f.w.close()
	go f.rt.Run(ctx)
	f.srv.Run(ctx)
}

// Handler is the nautilus API, with the program endpoints answered for a
// controller whose program lives in Logix Designer.
func (f *Facade) Handler() http.Handler {
	api := f.srv.Handler()
	mux := http.NewServeMux()
	mux.Handle("/", api)
	mux.HandleFunc("GET /api/program", programGone)
	mux.HandleFunc("GET /api/program/history", programGone)
	mux.HandleFunc("PUT /api/program", programLocked)
	mux.HandleFunc("POST /api/program/rollback", programLocked)
	mux.HandleFunc("POST /api/program/activate", programLocked)
	return mux
}

// The messages are what the extension shows: it surfaces a refused
// download's error verbatim ("download rejected — …").
const (
	msgNoSource = "this is a Logix controller: its program lives in Logix Designer, " +
		"not as nautilus source. Compare it with the repo's L5X using `naut logix drift`."
	msgNoEdit = "this is a Logix controller, which nautilus does not warm-swap. " +
		"Edit rungs online with `naut logix push --comm-path`, or download a " +
		"project with `naut logix download`."
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
	fmt.Fprintf(w, "{\"error\":%q}\n", msg)
}

// driverStatus reports the poll connection, plus the write connection's
// last failure — a write path that is failing while the polls succeed would
// otherwise be invisible until someone tried to set a value.
func (f *Facade) driverStatus() []server.DriverStatus {
	h := f.drv.Health()
	s := server.DriverStatus{
		Kind:      "ethernet-ip",
		Name:      h.Host,
		Detail:    fmt.Sprintf("%s · slot %d · Logix facade", h.Host, h.Slot),
		LastError: h.LastError,
		SinceMs:   h.SinceMs,
	}
	switch {
	case h.Connected:
		s.State = "connected"
		s.Message = fmt.Sprintf("Polling %d tags", h.Tags)
	case h.LastError != "":
		s.State = "error"
		s.Message = "Connect failed — retrying"
	default:
		s.State = "connecting"
		s.Message = "Connecting to controller"
	}
	if werr := f.w.lastError(); werr != "" && s.State == "connected" {
		s.State = "degraded"
		s.Message = "Polling, but the last write failed"
		s.LastError = werr
	}
	return []server.DriverStatus{s}
}

// ── writes ──────────────────────────────────────────────────────────────

// writer sends tag writes to the controller over its own connection, so a
// write never waits behind a poll cycle and a slow write never stalls the
// polls. It dials lazily and redials after the connection breaks.
type writer struct {
	o     Options
	byTag map[string]eip.TagBinding // nautilus name → binding
	types map[string]eip.TypeDef

	mu   sync.Mutex
	ctrl *logix.Controller
	err  string
}

func newWriter(o Options, m eip.Manifest) *writer {
	w := &writer{o: o, byTag: map[string]eip.TagBinding{}, types: map[string]eip.TypeDef{}}
	for _, b := range m.Tags {
		w.byTag[b.Name] = b
	}
	for _, t := range m.Types {
		w.types[t.Name] = t
	}
	return w
}

// badRequest is a write that could never succeed, as opposed to one the
// controller failed.
type badRequest string

func (e badRequest) Error() string   { return string(e) }
func (e badRequest) HTTPStatus() int { return http.StatusBadRequest }

func (w *writer) write(name string, value any) error {
	if _, isObject := value.(map[string]any); isObject {
		return badRequest("a Logix write sets one value: name the member (" + name + ".Member)")
	}
	device, code, err := w.resolve(name)
	if err != nil {
		return err
	}
	data, err := logix.EncodeScalar(code, value)
	if err != nil {
		return badRequest(fmt.Sprintf("%s: %v", name, err))
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if w.ctrl == nil || w.ctrl.Broken() {
		if w.ctrl != nil {
			_ = w.ctrl.Close()
		}
		w.ctrl, err = dial(ctx, w.o)
		if err != nil {
			w.ctrl = nil
			w.err = err.Error()
			return fmt.Errorf("connect to %s: %w", w.o.Host, err)
		}
	}
	if err := w.ctrl.WriteTag(ctx, device, code, 1, data); err != nil {
		w.err = fmt.Sprintf("write %s: %v", device, err)
		var cipErr *logix.CIPError
		if errors.As(err, &cipErr) && cipErr.Permanent() {
			// The controller understood and refused: a constant, a
			// read-only tag, a type it will not take. Not a transport
			// failure, so it says nothing about the connection.
			return badRequest(fmt.Sprintf("the controller refused %s: %v", device, err))
		}
		return err
	}
	w.err = ""
	return nil
}

// resolve maps a nautilus tag name, optionally with a dotted member path
// and array indexes ("TRS.Header.Valid", "Recipe.Steps[3]"), to the device
// path and the elementary type the controller expects.
func (w *writer) resolve(name string) (string, uint16, error) {
	root, rest, _ := strings.Cut(name, ".")
	rootName, rootIdx := splitIndex(root)
	b, ok := w.byTag[rootName]
	if !ok {
		return "", 0, badRequest(fmt.Sprintf("no tag named %s on this controller", rootName))
	}
	device := b.Device + rootIdx
	typ := b.Type
	if b.ArrayLen > 0 && rootIdx == "" {
		return "", 0, badRequest(fmt.Sprintf("%s is an array: write one element (%s[0])", rootName, rootName))
	}
	for rest != "" {
		var seg string
		seg, rest, _ = strings.Cut(rest, ".")
		field, idx := splitIndex(seg)
		f, ok := fieldOf(w.types[typ], field)
		if !ok {
			return "", 0, badRequest(fmt.Sprintf("%s has no member %s", typ, field))
		}
		if f.ArrayLen > 0 && idx == "" {
			return "", 0, badRequest(fmt.Sprintf("%s is an array: write one element", field))
		}
		device += "." + f.Name + idx
		typ = f.Type
	}
	if typ == "STRING" {
		return "", 0, badRequest(name + " is a STRING, which cannot be set from here yet")
	}
	ti, ok := logix.TypeByName(typ)
	if !ok {
		return "", 0, badRequest(fmt.Sprintf("%s is a %s: write one of its members", name, typ))
	}
	return device, ti.Code, nil
}

// fieldOf finds a member case-insensitively — Logix names are.
func fieldOf(td eip.TypeDef, name string) (eip.FieldDef, bool) {
	for _, f := range td.Fields {
		if strings.EqualFold(f.Name, name) {
			return f, true
		}
	}
	return eip.FieldDef{}, false
}

// splitIndex splits "Steps[3]" into "Steps" and "[3]".
func splitIndex(seg string) (string, string) {
	if i := strings.IndexByte(seg, '['); i >= 0 {
		return seg[:i], seg[i:]
	}
	return seg, ""
}

func (w *writer) lastError() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

func (w *writer) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ctrl != nil {
		_ = w.ctrl.Close()
		w.ctrl = nil
	}
}
