//go:build js

package hmibuild

import (
	"errors"
	"fmt"
	"syscall/js"
)

// packBytes, in the browser: naut.wasm doesn't carry the pack (it would add
// 3.4 MB to every playground visit); the page fetches it the first time a
// project has a screen to build, through globalThis.nautilusHMIPack(),
// which resolves to its bytes (playground/src/worker.js). Called off the
// event loop (startHMI's goroutine), so waiting here blocks nothing.
func packBytes() ([]byte, error) {
	get := js.Global().Get("nautilusHMIPack")
	if get.Type() != js.TypeFunction {
		return nil, errors.New("this page can't fetch naut's HMI packages (no nautilusHMIPack)")
	}
	type result struct {
		b   []byte
		err error
	}
	ch := make(chan result, 1)
	var ok, fail js.Func
	ok = js.FuncOf(func(_ js.Value, a []js.Value) any {
		u8 := a[0]
		b := make([]byte, u8.Get("length").Int())
		js.CopyBytesToGo(b, u8)
		ch <- result{b: b}
		ok.Release()
		fail.Release()
		return nil
	})
	fail = js.FuncOf(func(_ js.Value, a []js.Value) any {
		msg := "fetch failed"
		if len(a) > 0 {
			msg = a[0].Call("toString").String()
		}
		ch <- result{err: fmt.Errorf("fetching naut's HMI packages: %s", msg)}
		ok.Release()
		fail.Release()
		return nil
	})
	get.Invoke().Call("then", ok, fail)
	r := <-ch
	return r.b, r.err
}
