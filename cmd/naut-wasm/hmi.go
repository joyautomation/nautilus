//go:build js

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"mime"
	"net/http"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"syscall/js"
	"time"

	"github.com/joyautomation/nautilus/internal/project"
)

// The project's operator screen in the playground: naut's own build
// (internal/hmibuild), as `naut run` does on a machine, run by the builder
// module (cmd/naut-wasm-hmi) the first time the screen is asked for, in the
// background, so the program never waits for it.
// The playground serves it in a tab of its own (sw.js, view/app/) through
// app() below; the dashboard keeps "/" here, so neither needs a prefix.
type hmiBuild struct {
	done  chan struct{}
	mu    sync.Mutex
	files map[string][]byte
	pages []string
	errs  []string // a failed build's messages
	took  time.Duration
	src   string

	root    string
	req     hmiRequest
	once    sync.Once
	started atomic.Bool
}

// startHMI sets up the project's screen build, if it has one. It does NOT
// build: the builder (esbuild, Svelte) is a second wasm module, naut-hmi.wasm
// (cmd/naut-wasm-hmi), 2 MB gzipped, which the page fetches only when a
// screen is first asked for (jsApp). A project whose screens are prebuilt
// and never opened in the "app" view never pays for it.
func startHMI(proj *project.Project, root string) *hmiBuild {
	src := ""
	switch {
	case proj.HMIApp != "":
		src = proj.HMIApp + "/"
	case proj.HMIScreens:
		src = "the project's mimics"
	default:
		return nil
	}
	b := &hmiBuild{done: make(chan struct{}), src: src}
	b.req = hmiRequest{Title: proj.Name}
	if proj.HMIApp != "" {
		b.req.App = path.Join(root, proj.HMIApp)
	}
	b.root = root
	return b
}

// hmiRequest is what the builder module is asked (cmd/naut-wasm-hmi).
type hmiRequest struct {
	App   string `json:"app"`
	Title string `json:"title"`
}

// start begins the build, once, in the background.
func (b *hmiBuild) start() {
	b.once.Do(func() {
		b.started.Store(true)
		go func() {
			defer close(b.done)
			res, err := buildScreen(b.root, b.req)
			b.mu.Lock()
			defer b.mu.Unlock()
			if err != nil {
				b.errs = []string{err.Error()}
				var me *hmiErrors
				if errors.As(err, &me) {
					b.errs = me.msgs
				}
				return
			}
			b.files, b.pages, b.took = res.files, res.pages, res.took
		}()
	})
}

type hmiErrors struct{ msgs []string }

func (e *hmiErrors) Error() string { return strings.Join(e.msgs, "\n") }

type hmiResult struct {
	files map[string][]byte
	pages []string
	took  time.Duration
}

// buildScreen asks the page for the builder (globalThis.nautilusHMIBuild,
// playground/src/worker.js: it fetches and starts naut-hmi.wasm in a worker
// the first time, and sends it a snapshot of the project's files). Called off
// the event loop (start's goroutine), so waiting here blocks nothing.
func buildScreen(root string, rq hmiRequest) (hmiResult, error) {
	get := js.Global().Get("nautilusHMIBuild")
	if get.Type() != js.TypeFunction {
		return hmiResult{}, &hmiErrors{[]string{"this page can't build screens (no nautilusHMIBuild)"}}
	}
	opts, _ := json.Marshal(rq)
	type outcome struct {
		res hmiResult
		err error
	}
	ch := make(chan outcome, 1)
	var ok, fail js.Func
	release := func() { ok.Release(); fail.Release() }
	ok = js.FuncOf(func(_ js.Value, a []js.Value) any {
		defer release()
		v := a[0]
		if !v.Get("ok").Bool() {
			var msgs []string
			m := v.Get("messages")
			for i := 0; i < m.Length(); i++ {
				msgs = append(msgs, m.Index(i).String())
			}
			ch <- outcome{err: &hmiErrors{msgs}}
			return nil
		}
		files := map[string][]byte{}
		keys := js.Global().Get("Object").Call("keys", v.Get("files"))
		for i := 0; i < keys.Length(); i++ {
			k := keys.Index(i).String()
			u8 := v.Get("files").Get(k)
			data := make([]byte, u8.Get("length").Int())
			js.CopyBytesToGo(data, u8)
			files[k] = data
		}
		var pages []string
		ps := v.Get("pages")
		for i := 0; i < ps.Length(); i++ {
			pages = append(pages, ps.Index(i).String())
		}
		ch <- outcome{res: hmiResult{files: files, pages: pages, took: time.Duration(v.Get("ms").Float()) * time.Millisecond}}
		return nil
	})
	fail = js.FuncOf(func(_ js.Value, a []js.Value) any {
		defer release()
		msg := "the screen builder failed"
		if len(a) > 0 {
			msg = a[0].Call("toString").String()
		}
		ch <- outcome{err: &hmiErrors{[]string{msg}}}
		return nil
	})
	get.Invoke(root, string(opts)).Call("then", ok, fail)
	o := <-ch
	return o.res, o.err
}

// jsApp answers one request for the screen: app(method, path, headers, body),
// shaped as handle() answers. It waits for the build (up to a minute).
func jsApp(_ js.Value, args []js.Value) any {
	req := request(context.Background(), args)
	return promise(func() any {
		r := current()
		if r == nil || r.hmi == nil {
			return page(404, "No operator screen", "This project has no screen for naut to build: no server.hmi app (a folder with src/routes) and no *.mimic.json.")
		}
		b := r.hmi
		b.start()
		select {
		case <-b.done:
		case <-time.After(60 * time.Second):
			return page(503, "Still building", "The operator screen is still building. Reload in a moment.")
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		if len(b.errs) > 0 {
			return page(500, "The operator screen didn't build", b.src+" didn't build:\n\n"+strings.Join(b.errs, "\n")+"\n\nFix it and press Restart.")
		}
		name := strings.TrimPrefix(path.Clean("/"+req.URL.Path), "/")
		data, ok := b.files[name]
		if !ok && (name == "" || path.Ext(name) == "") {
			name, data, ok = "index.html", b.files["index.html"], true // a client-side route
		}
		if !ok {
			return page(404, "Not found", "No "+name+" in the operator screen.")
		}
		ctype := mime.TypeByExtension(path.Ext(name))
		if ctype == "" {
			ctype = http.DetectContentType(data)
		}
		out := map[string]any{"status": 200, "headers": map[string]any{"Content-Type": ctype, "Cache-Control": "no-cache"}}
		if textual(ctype) {
			out["body"] = string(data)
		} else {
			bs := js.Global().Get("Uint8Array").New(len(data))
			js.CopyBytesToJS(bs, data)
			out["bytes"] = bs
			out["body"] = ""
		}
		return js.ValueOf(out)
	})
}

// jsAppStatus says how the screen's build went: {state: "none" | "idle" | "building"
// | "ok" | "error", pages, messages, ms}.
func jsAppStatus(js.Value, []js.Value) any {
	r := current()
	if r == nil || r.hmi == nil {
		return js.ValueOf(map[string]any{"state": "none"})
	}
	b := r.hmi
	if !b.started.Load() {
		// nothing has asked for the screen yet: the builder isn't fetched
		return js.ValueOf(map[string]any{"state": "idle", "source": b.src})
	}
	select {
	case <-b.done:
	default:
		return js.ValueOf(map[string]any{"state": "building", "source": b.src})
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.errs) > 0 {
		return js.ValueOf(map[string]any{"state": "error", "source": b.src, "messages": toAny(b.errs)})
	}
	return js.ValueOf(map[string]any{"state": "ok", "source": b.src, "pages": toAny(b.pages), "ms": float64(b.took.Milliseconds())})
}

// page is a plain HTML answer: what a screen that isn't there says.
func page(status int, title, text string) js.Value {
	var b bytes.Buffer
	fmt.Fprintf(&b, `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>%s</title></head>`+
		`<body style="font: 15px/1.5 system-ui, sans-serif; margin: 2rem; max-width: 60rem"><h1 style="font-size: 1.3rem">%s</h1>`+
		`<pre style="white-space: pre-wrap; font: 13px/1.5 ui-monospace, monospace">%s</pre></body></html>`,
		html.EscapeString(title), html.EscapeString(title), html.EscapeString(text))
	return js.ValueOf(map[string]any{"status": status, "headers": map[string]any{"Content-Type": "text/html; charset=utf-8"}, "body": b.String()})
}
