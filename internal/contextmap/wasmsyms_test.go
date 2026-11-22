package contextmap

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestWasmLinksNoTemplateEngine(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the playground's wasm")
	}
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	root := filepath.Dir(strings.TrimSpace(string(out)))
	bin := filepath.Join(t.TempDir(), "naut.wasm")
	// Not stripped (no -s): the name section is what this reads.
	cmd := exec.Command("go", "build", "-trimpath", "-o", bin, "./cmd/naut-wasm")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/naut-wasm: %v\n%s", err, b)
	}
	wasm, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	names, err := wasmFuncNames(wasm)
	if err != nil {
		t.Fatalf("reading the wasm's name section: %v", err)
	}
	if len(names) < 1000 {
		t.Fatalf("only %d function names: not the name section this test expects", len(names))
	}
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
