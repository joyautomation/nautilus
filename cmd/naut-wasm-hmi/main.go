//go:build js && wasm

// naut-wasm-hmi is the playground's screen builder: internal/hmibuild (esbuild
// and the Svelte pipeline) compiled to its own WebAssembly module, so the
// 2 MB of it is not part of naut.wasm. The page fetches it the first time a
// project's screen is opened (playground/src/hmi-worker.js runs it in a Web
// Worker of its own, so a build never stalls the scan loop). It knows
// nothing of the controller: it reads the project's files from the
// in-memory file system its worker filled (globalThis.fs) and answers one
// build at a time.
//
//	globalThis.nautilusHMI = {
//	  build(root, optsJSON) -> Promise<{ok: true, files: {path: Uint8Array},
//	                                     pages: [string], ms: number}
//	                                  | {ok: false, messages: [string]}>
//	}
//
// optsJSON is {"app": <dir of the HMI app, or "">, "title": <project name>}.
// This package may import only hmibuild (and the standard library): the
// controller's packages are what naut.wasm carries
// (TestWasmHMIModuleStaysSmall).
package main

import (
	"encoding/json"
	"errors"
	"syscall/js"

	"github.com/joyautomation/nautilus/internal/hmibuild"
)

type request struct {
	App   string `json:"app"`
	Title string `json:"title"`
}

func main() {
	api := js.Global().Get("Object").New()
	api.Set("build", js.FuncOf(func(_ js.Value, a []js.Value) any {
		root, opts := a[0].String(), a[1].String()
		return js.Global().Get("Promise").New(js.FuncOf(func(_ js.Value, p []js.Value) any {
			resolve := p[0]
			// off the event loop: a js.FuncOf callback must not block
			go func() { resolve.Invoke(build(root, opts)) }()
			return nil
		}))
	}))
	js.Global().Set("nautilusHMI", api)
	if cb := js.Global().Get("onNautilusHMIReady"); cb.Type() == js.TypeFunction {
		cb.Invoke()
	}
	select {}
}

func build(root, optsJSON string) js.Value {
	var rq request
	if err := json.Unmarshal([]byte(optsJSON), &rq); err != nil {
		return failed([]string{"the screen builder was sent bad options: " + err.Error()})
	}
	res, err := hmibuild.Build(hmibuild.Options{Project: root, Title: rq.Title, App: rq.App, Minify: true, NoPWA: true})
	if err != nil {
		var be *hmibuild.Error
		if errors.As(err, &be) {
			return failed(be.Messages)
		}
		return failed([]string{err.Error()})
	}
	files := js.Global().Get("Object").New()
	for name, data := range res.Files {
		u8 := js.Global().Get("Uint8Array").New(len(data))
		js.CopyBytesToJS(u8, data)
		files.Set(name, u8)
	}
	pages := make([]any, len(res.Pages))
	for i, p := range res.Pages {
		pages[i] = p
	}
	return js.ValueOf(map[string]any{"ok": true, "files": files, "pages": pages, "ms": float64(res.Took.Milliseconds())})
}

func failed(msgs []string) js.Value {
	m := make([]any, len(msgs))
	for i, s := range msgs {
		m[i] = s
	}
	return js.ValueOf(map[string]any{"ok": false, "messages": m})
}
