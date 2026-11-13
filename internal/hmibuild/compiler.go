package hmibuild

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"runtime"
	"sync"

	"github.com/dop251/goja"
)

// Warning is one Svelte compiler warning.
type Warning struct {
	Code    string
	Message string
	Line    int // 1-based; 0 when the compiler gave no position
	Column  int // 1-based
}

// compiled is one .svelte (or .svelte.js) file, compiled.
type compiled struct {
	JS       string
	Warnings []Warning
}

// fatalWarnings throw when the component mounts, so the build stops on them
// with Svelte's own words: a prop or variable named `state` beside
// `$state(...)` makes `$state` a store subscription to it
// (store_rune_conflict), which throws "e.subscribe is not a function".
var fatalWarnings = map[string]bool{"store_rune_conflict": true}

// compiler runs Svelte's own compiler (packs: svelte-compiler.js, the
// svelte/compiler of the pinned Svelte, bundled) in goja, a JavaScript
// engine in pure Go, so naut needs no Node. A pool of engines compiles in
// parallel (esbuild calls its plugins from many goroutines), and results
// are memoised by content for the life of the process.
type compiler struct {
	pool chan *engine
	memo sync.Map // key -> compiled
}

type engine struct {
	vm      *goja.Runtime
	compile goja.Callable
	module  goja.Callable
}

func newCompiler(script string) (*compiler, error) {
	n := runtime.GOMAXPROCS(0)
	if n > 8 {
		n = 8
	}
	c := &compiler{pool: make(chan *engine, n)}
	// one engine up front, so a broken script fails here; the rest start lazily
	e, err := newEngine(script)
	if err != nil {
		return nil, err
	}
	c.pool <- e
	for i := 1; i < n; i++ {
		go func() {
			if e, err := newEngine(script); err == nil {
				c.pool <- e
			}
		}()
	}
	return c, nil
}

func newEngine(script string) (*engine, error) {
	vm := goja.New()
	if _, err := vm.RunScript("svelte-compiler.js", "var globalThis = this, self = this, window = undefined;\n"+script); err != nil {
		return nil, fmt.Errorf("starting the Svelte compiler: %w", err)
	}
	sc := vm.Get("svelteCompiler")
	if sc == nil || goja.IsUndefined(sc) {
		return nil, fmt.Errorf("starting the Svelte compiler: svelte-compiler.js defines no svelteCompiler")
	}
	o := sc.ToObject(vm)
	compile, ok1 := goja.AssertFunction(o.Get("compile"))
	module, ok2 := goja.AssertFunction(o.Get("compileModule"))
	if !ok1 || !ok2 {
		return nil, fmt.Errorf("starting the Svelte compiler: no compile/compileModule")
	}
	return &engine{vm: vm, compile: compile, module: module}, nil
}

// Compile compiles a component (module=false) or a runes module
// (module=true, a .svelte.js; TypeScript already stripped).
func (c *compiler) Compile(src, filename string, module bool) (compiled, error) {
	h := sha256.Sum256([]byte(fmt.Sprintf("%t\x00%s\x00%s", module, filename, src)))
	key := hex.EncodeToString(h[:])
	if v, ok := c.memo.Load(key); ok {
		return v.(compiled), nil
	}
	e := <-c.pool
	out, err := e.run(src, filename, module)
	c.pool <- e
	if err == nil {
		c.memo.Store(key, out)
	}
	return out, err
}

func (e *engine) run(src, filename string, module bool) (out compiled, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	opts := e.vm.NewObject()
	_ = opts.Set("filename", filename)
	_ = opts.Set("generate", "client")
	fn := e.module
	if !module {
		fn = e.compile
		_ = opts.Set("css", "injected")
	}
	v, jsErr := fn(goja.Undefined(), e.vm.ToValue(src), opts)
	if jsErr != nil {
		return out, compileError(e.vm, jsErr)
	}
	o := v.ToObject(e.vm)
	out.JS = o.Get("js").ToObject(e.vm).Get("code").String()
	if ws := o.Get("warnings"); ws != nil && !goja.IsUndefined(ws) && !goja.IsNull(ws) {
		arr := ws.ToObject(e.vm)
		n := int(arr.Get("length").ToInteger())
		for i := 0; i < n; i++ {
			out.Warnings = append(out.Warnings, warningOf(e.vm, arr.Get(fmt.Sprint(i)).ToObject(e.vm)))
		}
	}
	return out, nil
}

func warningOf(vm *goja.Runtime, w *goja.Object) Warning {
	out := Warning{Code: str(w.Get("code")), Message: str(w.Get("message"))}
	if s := w.Get("start"); s != nil && !goja.IsUndefined(s) && !goja.IsNull(s) {
		so := s.ToObject(vm)
		out.Line = int(so.Get("line").ToInteger())
		out.Column = int(so.Get("column").ToInteger()) + 1
	}
	return out
}

// compileError keeps a Svelte CompileError's position: "line:col: message".
func compileError(vm *goja.Runtime, err error) error {
	ex, ok := err.(*goja.Exception)
	if !ok {
		return err
	}
	v := ex.Value()
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return err
	}
	o := v.ToObject(vm)
	msg := str(o.Get("message"))
	if msg == "" {
		return err
	}
	if s := o.Get("start"); s != nil && !goja.IsUndefined(s) && !goja.IsNull(s) {
		so := s.ToObject(vm)
		return fmt.Errorf("%d:%d: %s", so.Get("line").ToInteger(), so.Get("column").ToInteger()+1, msg)
	}
	return fmt.Errorf("%s", msg)
}

func str(v goja.Value) string {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return ""
	}
	return v.String()
}
