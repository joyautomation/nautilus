package contextmap

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// wasmUnlinked are packages cmd/naut-wasm may import but must never link
// a function of: the import graph (TestWasmStaysFreeOfControllerCode)
// cannot see what the linker drops, so this reads the built binary's
// function names instead.
//
// text/template (under html/template) calls reflect's MethodByName with a
// name it learns at run time, so a binary that can reach it keeps every
// exported method of every type it links: about 900 KB gzipped in the
// playground (#1239). server still imports html/template for the
// controller's pages, but only server.New parses them; the playground
// calls server.NewPlayground and must not reach them (server/pages.go).
var wasmUnlinked = []string{
	"text/template",
	"html/template",
}

// builtWasmNames builds the playground's wasm unstripped (the name section
// is what the symbol tests read) once and returns its function names.
var (
	wasmNamesOnce sync.Once
	wasmNames     []string
	wasmNamesErr  error
)

func builtWasmNames(t *testing.T) []string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the playground's wasm")
	}
	wasmNamesOnce.Do(func() {
		out, err := exec.Command("go", "env", "GOMOD").Output()
		if err != nil {
			wasmNamesErr = fmt.Errorf("go env GOMOD: %v", err)
			return
		}
		root := filepath.Dir(strings.TrimSpace(string(out)))
		dir, err := os.MkdirTemp("", "wasmsyms")
		if err != nil {
			wasmNamesErr = err
			return
		}
		defer os.RemoveAll(dir)
		bin := filepath.Join(dir, "naut.wasm")
		// Not stripped (no -s): the name section is what this reads.
		cmd := exec.Command("go", "build", "-trimpath", "-o", bin, "./cmd/naut-wasm")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm")
		if b, err := cmd.CombinedOutput(); err != nil {
			wasmNamesErr = fmt.Errorf("go build ./cmd/naut-wasm: %v\n%s", err, b)
			return
		}
		wasm, err := os.ReadFile(bin)
		if err != nil {
			wasmNamesErr = err
			return
		}
		wasmNames, wasmNamesErr = wasmFuncNames(wasm)
	})
	if wasmNamesErr != nil {
		t.Fatalf("the playground's wasm: %v", wasmNamesErr)
	}
	if len(wasmNames) < 1000 {
		t.Fatalf("only %d function names: not the name section this test expects", len(wasmNames))
	}
	return wasmNames
}

func TestWasmLinksNoTemplateEngine(t *testing.T) {
	names := builtWasmNames(t)
	// The name section spells a package path with _ for /
	// (html_template.init). server imports html/template, so its
	// initialiser is always there: if it is not found, the spelling has
	// changed and the checks below would pass without matching anything.
	sentinel := false
	for _, f := range names {
		sentinel = sentinel || f == "html_template.init"
	}
	if !sentinel {
		t.Fatal("no html_template.init in the wasm's names: the name section's spelling changed; fix the matching below")
	}
	for _, pkg := range wasmUnlinked {
		// A package's initialisers are linked whenever it is imported,
		// and are not the engine.
		prefix := strings.ReplaceAll(pkg, "/", "_")
		n, first := 0, ""
		for _, f := range names {
			fn, ok := strings.CutPrefix(f, prefix+".")
			if !ok {
				fn, ok = strings.CutPrefix(f, prefix+"_")
				if _, after, cut := strings.Cut(fn, "."); cut {
					fn = after
				}
			}
			if ok && fn != "init" && !strings.HasPrefix(fn, "init.") {
				if n == 0 {
					first = f
				}
				n++
			}
		}
		if n > 0 {
			t.Errorf("cmd/naut-wasm links %d functions of %s (first %s): something the playground reaches renders a template. "+
				"Render through the server's pages seam (server/pages.go), which only server.New installs", n, pkg, first)
		}
	}
}

// controllerHandlers are server handlers of the controller-only route groups
// (server/mounts_controller.go): only server.New mounts them, so the
// playground's wasm must link none. A name is matched as a prefix, so a
// handler's closures count. handleState is the core handler that proves the
// spelling below still matches something.
var controllerHandlers = []string{
	"handleSignIn", "handleSignOut", "handleSignInPage", "handleElevate",
	"handleOIDCStart", "handleOIDCCallback", "handleOIDCElevated",
	"handlePushKey", "handlePushList", "handlePushHealth", "handlePushSubscribe", "handlePushUnsubscribe", "handlePushConfirm",
	"handleAckLinkPage", "handleAckLink", "handleInboundAck",
	"handleClearDuplicate", "handleCommission", "handleRecommission", "handleVouch",
	"handleIO", "handleIOFiles", "handleIOStage", "handleIOStaged", "handleIOValidate", "handleIOApply", "handleIOCancelRevert",
	"handleConfigFiles", "handleConfigPatch", "handleConfigCommit",
	"handleProgramHistory", "handleActivate", "handleParametersImport",
	"handleParamList", "handleParamRead", "handleParamWrite",
	"handleHistory",
}

func TestWasmLinksNoControllerRoutes(t *testing.T) {
	names := builtWasmNames(t)
	// the name section spells github.com/joyautomation/nautilus/server's
	// *Server methods like this
	const server = "github.com_joyautomation_nautilus_server.__Server_."
	has := func(handler string) (string, bool) {
		for _, f := range names {
			if strings.HasPrefix(f, server+handler) {
				return f, true
			}
		}
		return "", false
	}
	if _, ok := has("handleState"); !ok {
		t.Fatal("no server.(*Server).handleState in the wasm's names: the name section's spelling changed; fix the matching here")
	}
	for _, h := range controllerHandlers {
		if f, ok := has(h); ok {
			t.Errorf("cmd/naut-wasm links %s: a controller-only route group is reachable from the playground. "+
				"Register it in a Mount of server/mounts_controller.go, which only server.New installs", f)
		}
	}
}

// wasmFuncNames reads the function names of a wasm module's "name" custom
// section (subsection 1).
func wasmFuncNames(b []byte) ([]string, error) {
	if len(b) < 8 || !bytes.Equal(b[:4], []byte("\x00asm")) {
		return nil, errors.New("not a wasm module")
	}
	r := bytes.NewReader(b[8:])
	for r.Len() > 0 {
		id, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		size, err := binary.ReadUvarint(r)
		if err != nil || size > uint64(r.Len()) {
			return nil, errors.New("bad section size")
		}
		sec := make([]byte, size)
		r.Read(sec)
		if id != 0 {
			continue
		}
		sr := bytes.NewReader(sec)
		name, err := wasmString(sr)
		if err != nil || name != "name" {
			continue
		}
		for sr.Len() > 0 {
			sub, _ := sr.ReadByte()
			n, err := binary.ReadUvarint(sr)
			if err != nil || n > uint64(sr.Len()) {
				return nil, errors.New("bad name subsection")
			}
			data := make([]byte, n)
			sr.Read(data)
			if sub != 1 {
				continue
			}
			dr := bytes.NewReader(data)
			count, err := binary.ReadUvarint(dr)
			if err != nil {
				return nil, err
			}
			out := make([]string, 0, count)
			for range count {
				if _, err := binary.ReadUvarint(dr); err != nil {
					return nil, err
				}
				s, err := wasmString(dr)
				if err != nil {
					return nil, err
				}
				out = append(out, s)
			}
			return out, nil
		}
	}
	return nil, errors.New("no function names")
}

func wasmString(r *bytes.Reader) (string, error) {
	n, err := binary.ReadUvarint(r)
	if err != nil || n > uint64(r.Len()) {
		return "", errors.New("bad string")
	}
	s := make([]byte, n)
	r.Read(s)
	return string(s), nil
}
