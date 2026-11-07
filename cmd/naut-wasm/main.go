//go:build js && wasm

// naut-wasm is the nautilus playground: the compiler, the scan loop, the
// tag API, the diagram editors' graph/edit, the language server and the
// acceptance runner, compiled to WebAssembly and driven from JavaScript in
// a Web Worker. Nothing listens and nothing dials. The project lives in the
// in-memory file system the worker hands Go (globalThis.fs), so every path
// below is a path in it, e.g. "/project".
//
//	globalThis.nautilus = {
//	  load(root)               -> Promise<{ok, error?, notices[], ms}>
//	  stop()                   -> Promise<void>
//	  handle(m, url, hdrs, b)  -> Promise<{status, headers, body}>   the tag API
//	  stream(url, onChunk)     -> cancel()                           SSE, chunk by chunk
//	  stats()                  -> ScanStats | null
//	  graph(lang, src, at)     -> string    `naut <lang> graph` output (JSON)
//	  edit(lang, request)      -> string    {"out": ..., "code": n}, as `naut <lang> edit`
//	  lsp(onMessage)           -> send(json)   one language-server session
//	  test(root, runPattern?)  -> Promise<string>   `naut test --json` output
//	}
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall/js"
	"time"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/internal/diagram"
	"github.com/joyautomation/nautilus/internal/lsp"
	"github.com/joyautomation/nautilus/internal/project"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/leader"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/server"
)

// localGateLease matches naut run's: a hung scan closes the write gate
// about 5 to 8 s into the hang.
const localGateLease = 4 * time.Second

type running struct {
	cancel context.CancelFunc
	done   chan struct{}
	rt     *runtime.Runtime
	srv    *server.Server
}

var (
	mu  sync.Mutex
	cur *running
)

func main() {
	api := js.Global().Get("Object").New()
	api.Set("load", js.FuncOf(jsLoad))
	api.Set("stop", js.FuncOf(func(js.Value, []js.Value) any {
		return promise(func() any { stop(); return js.Undefined() })
	}))
	api.Set("handle", js.FuncOf(jsHandle))
	api.Set("stream", js.FuncOf(jsStream))
	api.Set("stats", js.FuncOf(jsStats))
	api.Set("graph", js.FuncOf(jsGraph))
	api.Set("edit", js.FuncOf(jsEdit))
	api.Set("lsp", js.FuncOf(jsLSP))
	api.Set("test", js.FuncOf(jsTest))
	api.Set("version", lsp.Version)
	js.Global().Set("nautilus", api)
	if cb := js.Global().Get("onNautilusReady"); cb.Type() == js.TypeFunction {
		cb.Invoke()
	}
	select {}
}

// promise runs fn on its own goroutine and resolves with what it returns:
// a js.FuncOf callback must not block, or the event loop deadlocks.
func promise(fn func() any) js.Value {
	return js.Global().Get("Promise").New(js.FuncOf(func(_ js.Value, p []js.Value) any {
		resolve := p[0]
		go func() { resolve.Invoke(fn()) }()
		return nil
	}))
}

func current() *running {
	mu.Lock()
	defer mu.Unlock()
	return cur
}

func stop() {
	mu.Lock()
	r := cur
	cur = nil
	mu.Unlock()
	if r != nil {
		r.cancel()
		<-r.done
	}
}

// ---- running a project ---------------------------------------------------

func jsLoad(_ js.Value, args []js.Value) any {
	root := args[0].String()
	return promise(func() any {
		t0 := time.Now()
		notices, err := load(root)
		out := map[string]any{"ok": err == nil, "notices": toAny(notices)}
		if err != nil {
			out["error"] = err.Error()
		}
		out["ms"] = float64(time.Since(t0).Microseconds()) / 1000
		return js.ValueOf(out)
	})
}

// load composes the project at root exactly as naut run does, minus what a
// browser tab cannot have: no listener, no retained-memory file, no
// redundancy, no OPC UA or Sparkplug (project.Notices says so).
func load(root string) ([]string, error) {
	proj, err := project.Load(os.DirFS(root), project.ManifestName)
	if err != nil {
		return nil, err
	}
	proj.Runtime.Retain = nil // a reload starts from init: values
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		return proj.Notices, fmt.Errorf("compile: %w", err)
	}
	// The write gate needs live scans, as on a standalone controller: a
	// hung program stops driving its outputs.
	gate := leader.NewLocal(proj.Name, localGateLease)
	if proj.Runtime.Driver != nil {
		nio.SetPlainGate(proj.Runtime.Driver, gate.IsLeader)
	}
	gate.SetHealth(rt.Liveness)

	alarms, err := proj.NewAlarms(rt)
	if err != nil {
		return proj.Notices, fmt.Errorf("alarms: %w", err)
	}
	sopts := proj.Server
	sopts.HMI = nil
	if alarms != nil {
		rt.SetAlarms(alarms.Engine)
		sopts.Alarms = alarms.Engine
		sopts.AlarmShelveTimes = alarms.ShelveTimes
	}
	srv := server.New(rt, sopts)

	stop()
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{cancel: cancel, done: make(chan struct{}), rt: rt, srv: srv}
	gateCtx, gateCancel := context.WithCancel(context.Background())
	gateDone := make(chan struct{})
	go func() { gate.Run(gateCtx); close(gateDone) }()
	go srv.Run(ctx)
	go func() {
		rt.Run(ctx)
		if alarms != nil {
			alarms.Close()
		}
		gateCancel()
		<-gateDone
		gate.Release()
		close(r.done)
	}()
	mu.Lock()
	cur = r
	mu.Unlock()
	return proj.Notices, nil
}

func jsStats(js.Value, []js.Value) any {
	r := current()
	if r == nil {
		return js.Null()
	}
	b, _ := json.Marshal(r.rt.Stats())
	return js.Global().Get("JSON").Call("parse", string(b))
}

// ---- the tag API -----------------------------------------------------------

func request(ctx context.Context, args []js.Value) *http.Request {
	method, url := args[0].String(), args[1].String()
	var body io.Reader
	if len(args) > 3 && args[3].Type() == js.TypeString {
		body = strings.NewReader(args[3].String())
	}
	req := httptest.NewRequest(method, url, body).WithContext(ctx)
	if len(args) > 2 && args[2].Type() == js.TypeObject {
		keys := js.Global().Get("Object").Call("keys", args[2])
		for i := 0; i < keys.Length(); i++ {
			k := keys.Index(i).String()
			req.Header.Set(k, args[2].Get(k).String())
		}
	}
	return req
}

func jsHandle(_ js.Value, args []js.Value) any {
	req := request(context.Background(), args)
	return promise(func() any {
		r := current()
		if r == nil {
			return js.ValueOf(map[string]any{"status": 503, "headers": map[string]any{}, "body": "no project is running"})
		}
		rec := httptest.NewRecorder()
		r.srv.Handler().ServeHTTP(rec, req)
		res := rec.Result()
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, res.Body)
		h := map[string]any{}
		for k, vs := range res.Header {
			if len(vs) > 0 {
				h[k] = vs[0]
			}
		}
		return js.ValueOf(map[string]any{"status": res.StatusCode, "headers": h, "body": buf.String()})
	})
}

// flushWriter is an http.ResponseWriter whose Flush hands what was written
// since the last one to JavaScript: the server's real SSE handler (deltas,
// filters, resyncs and all) streams through it unchanged.
type flushWriter struct {
	h      http.Header
	buf    bytes.Buffer
	status int
	emit   func(string)
}

func (w *flushWriter) Header() http.Header         { return w.h }
func (w *flushWriter) Write(b []byte) (int, error) { return w.buf.Write(b) }
func (w *flushWriter) WriteHeader(code int)        { w.status = code }
func (w *flushWriter) Flush() {
	if w.buf.Len() > 0 {
		s := w.buf.String()
		w.buf.Reset()
		w.emit(s)
	}
}

// jsStream serves a streaming request (GET /api/stream...) and calls
// onChunk(text) on every flush and onChunk(null) at its end. It returns the
// function that ends it.
func jsStream(_ js.Value, args []js.Value) any {
	onChunk := args[1]
	ctx, cancel := context.WithCancel(context.Background())
	req := request(ctx, []js.Value{js.ValueOf("GET"), args[0]})
	go func() {
		r := current()
		if r == nil {
			onChunk.Invoke(js.Null())
			return
		}
		w := &flushWriter{h: http.Header{}, emit: func(s string) { onChunk.Invoke(s) }}
		r.srv.Handler().ServeHTTP(w, req)
		w.Flush()
		onChunk.Invoke(js.Null())
	}()
	var fn js.Func
	fn = js.FuncOf(func(js.Value, []js.Value) any { cancel(); fn.Release(); return nil })
	return fn
}

// ---- diagram editors ---------------------------------------------------------

func jsGraph(_ js.Value, args []js.Value) any {
	v, err := diagram.Graph(args[0].String(), args[1].String(), args[2].String())
	if err != nil {
		v = map[string]string{"error": err.Error()}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func jsEdit(_ js.Value, args []js.Value) any {
	out, code := diagram.Edit(args[0].String(), []byte(args[1].String()))
	b, _ := json.Marshal(map[string]any{"out": out, "code": code})
	return string(b)
}

// ---- language server ---------------------------------------------------------

// jsLSP starts one `naut lsp` session over in-process pipes. JavaScript
// sends and receives bare JSON-RPC messages; the Content-Length framing is
// done here.
func jsLSP(_ js.Value, args []js.Value) any {
	onMessage := args[0]
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	queue := make(chan string, 256)
	go func() {
		_ = lsp.Serve(inR, outW)
		outW.Close()
	}()
	go func() { // one writer, so messages keep their order
		for msg := range queue {
			if _, err := io.WriteString(inW, "Content-Length: "+strconv.Itoa(len(msg))+"\r\n\r\n"+msg); err != nil {
				return
			}
		}
	}()
	go func() {
		rd := bufio.NewReader(outR)
		for {
			n := -1
			for {
				line, err := rd.ReadString('\n')
				if err != nil {
					return
				}
				line = strings.TrimSpace(line)
				if line == "" {
					break
				}
				if v, ok := strings.CutPrefix(line, "Content-Length:"); ok {
					n, _ = strconv.Atoi(strings.TrimSpace(v))
				}
			}
			if n < 0 {
				continue
			}
			body := make([]byte, n)
			if _, err := io.ReadFull(rd, body); err != nil {
				return
			}
			onMessage.Invoke(string(body))
		}
	}()
	return js.FuncOf(func(_ js.Value, a []js.Value) any {
		queue <- a[0].String()
		return nil
	})
}

// ---- acceptance tests --------------------------------------------------------

func jsTest(_ js.Value, args []js.Value) any {
	root := args[0].String()
	pattern := ""
	if len(args) > 1 && args[1].Type() == js.TypeString {
		pattern = args[1].String()
	}
	return promise(func() any {
		out, err := runTests(root, pattern)
		if err != nil {
			b, _ := json.Marshal(map[string]string{"error": err.Error()})
			return string(b)
		}
		return out
	})
}

// runTests is `naut test --json [-run pattern]` against the project at root:
// every *_test.yaml suite, each test on its own runtime in virtual time.
func runTests(root, pattern string) (string, error) {
	fsys := os.DirFS(root)
	proj, err := project.Load(fsys, project.ManifestName)
	if err != nil {
		return "", err
	}
	if _, err := runtime.New(proj.Runtime); err != nil {
		return "", fmt.Errorf("compile: %w", err)
	}
	paths, err := acceptance.DiscoverSuites(fsys)
	if err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return "", fmt.Errorf("no *%s files in this project", acceptance.SuffixTest)
	}
	var filter *regexp.Regexp
	if pattern != "" {
		if filter, err = regexp.Compile(pattern); err != nil {
			return "", fmt.Errorf("bad -run pattern: %w", err)
		}
	}
	var results []acceptance.Result
	for _, p := range paths {
		suite, err := acceptance.LoadSuite(fsys, p)
		if err != nil {
			return "", err
		}
		if filter != nil {
			kept := suite.Tests[:0]
			for _, t := range suite.Tests {
				if filter.MatchString(t.Name) {
					kept = append(kept, t)
				}
			}
			if len(kept) == 0 {
				continue
			}
			suite.Tests = kept
		}
		rs, err := acceptance.RunSuite(suite, proj.Runtime, acceptance.WithAlarms(proj.AlarmEngine))
		if err != nil {
			return "", err
		}
		results = append(results, rs...)
	}
	var buf bytes.Buffer
	acceptance.ReportJSON(&buf, results)
	return buf.String(), nil
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
