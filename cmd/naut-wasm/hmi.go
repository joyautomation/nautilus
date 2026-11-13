//go:build js

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"mime"
	"net/http"
	"path"
	"strings"
	"sync"
	"syscall/js"
	"time"

	"github.com/joyautomation/nautilus/internal/hmibuild"
	"github.com/joyautomation/nautilus/internal/project"
)

// The project's operator screen in the playground: naut's own build
// (internal/hmibuild), as `naut run` does on a machine, run in the
// background once the controller is up, so the program never waits for it.
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
}

// startHMI builds the project's screen, if it has one, in the background.
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
	go func() {
		defer close(b.done)
		opts := hmibuild.Options{Project: root, Title: proj.Name, Minify: true}
		if proj.HMIApp != "" {
			opts.App = path.Join(root, proj.HMIApp)
		}
		res, err := hmibuild.Build(opts)
		b.mu.Lock()
		defer b.mu.Unlock()
		if err != nil {
			var be *hmibuild.Error
			if errors.As(err, &be) {
				b.errs = be.Messages
			} else {
				b.errs = []string{err.Error()}
			}
			return
		}
		b.files, b.pages, b.took = res.Files, res.Pages, res.Took
	}()
	return b
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

// jsAppStatus says how the screen's build went: {state: "none" | "building"
// | "ok" | "error", pages, messages, ms}.
func jsAppStatus(js.Value, []js.Value) any {
	r := current()
	if r == nil || r.hmi == nil {
		return js.ValueOf(map[string]any{"state": "none"})
	}
	b := r.hmi
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
