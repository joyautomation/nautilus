//go:build js

package hmibuild

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"syscall/js"
)

// compiler, in the browser (naut.wasm, the playground): Svelte's compiler
// runs in the page's own JavaScript engine, which is many times faster
// than an interpreter compiled to wasm, and keeps goja out of naut.wasm.
// Calls into JavaScript are synchronous, so one engine serves every
// goroutine esbuild calls its plugins from.
type compiler struct {
	mu      sync.Mutex
	compile js.Value
	module  js.Value
	memo    sync.Map
}

func newCompiler(script string) (c *compiler, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("starting the Svelte compiler: %v", r)
		}
	}()
	fn := js.Global().Get("Function").New("var process = { env: {}, platform: 'browser', versions: {}, cwd: function () { return '/'; } };\n" + script + "\n;return svelteCompiler;")
	sc := fn.Invoke()
	if sc.Type() != js.TypeObject {
		return nil, fmt.Errorf("starting the Svelte compiler: svelte-compiler.js defines no svelteCompiler")
	}
	return &compiler{compile: sc.Get("compile"), module: sc.Get("compileModule")}, nil
}

func (c *compiler) Compile(src, filename string, module bool) (out compiled, err error) {
	h := sha256.Sum256([]byte(fmt.Sprintf("%t\x00%s\x00%s", module, filename, src)))
	key := hex.EncodeToString(h[:])
	if v, ok := c.memo.Load(key); ok {
		return v.(compiled), nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			err = jsCompileError(r)
		}
	}()
	opts := map[string]any{"filename": filename, "generate": "client"}
	fn := c.module
	if !module {
		fn = c.compile
		opts["css"] = "injected"
	}
	v := fn.Invoke(src, js.ValueOf(opts))
	out.JS = v.Get("js").Get("code").String()
	if ws := v.Get("warnings"); ws.Type() == js.TypeObject {
		for i := 0; i < ws.Length(); i++ {
			w := ws.Index(i)
			warning := Warning{Code: jsStr(w.Get("code")), Message: jsStr(w.Get("message"))}
			if s := w.Get("start"); s.Type() == js.TypeObject {
				warning.Line = s.Get("line").Int()
				warning.Column = s.Get("column").Int() + 1
			}
			out.Warnings = append(out.Warnings, warning)
		}
	}
	c.memo.Store(key, out)
	return out, nil
}

// jsCompileError keeps a Svelte CompileError's position: "line:col: message".
func jsCompileError(r any) error {
	je, ok := r.(js.Error)
	if !ok {
		return fmt.Errorf("%v", r)
	}
	v := je.Value
	msg := jsStr(v.Get("message"))
	if msg == "" {
		return je
	}
	if s := v.Get("start"); s.Type() == js.TypeObject {
		return fmt.Errorf("%d:%d: %s", s.Get("line").Int(), s.Get("column").Int()+1, msg)
	}
	return fmt.Errorf("%s", msg)
}

func jsStr(v js.Value) string {
	if v.Type() != js.TypeString {
		return ""
	}
	return v.String()
}
