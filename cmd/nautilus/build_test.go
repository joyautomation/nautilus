package main

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// emitBinary must produce runner-bytes + zip + footer, be readable back via
// embeddedOffset, and — building FROM a built binary — replace the old
// archive rather than stacking a second one.
func TestBuildEmbedRoundTrip(t *testing.T) {
	dir := t.TempDir()
	runner := filepath.Join(dir, "runner")
	runnerBytes := []byte("FAKE-RUNNER-EXECUTABLE-BYTES")
	if err := os.WriteFile(runner, runnerBytes, 0o755); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(dir, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "name: embedded-test\ntasks:\n  - program: program.st\n"
	os.WriteFile(filepath.Join(proj, "nautilus.yaml"), []byte(manifest), 0o644)
	os.WriteFile(filepath.Join(proj, "program.st"), []byte("PROGRAM P\nEND_PROGRAM"), 0o644)

	out := filepath.Join(dir, "out")
	if err := emitBinary(runner, proj, out, ""); err != nil {
		t.Fatal(err)
	}

	read := func(path string) (prefix []byte, files map[string]string) {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		st, _ := f.Stat()
		off, ok := embeddedOffset(f, st.Size())
		if !ok {
			t.Fatalf("%s: no embedded project", path)
		}
		prefix = make([]byte, off)
		if _, err := f.ReadAt(prefix, 0); err != nil {
			t.Fatal(err)
		}
		zr, err := zip.NewReader(io.NewSectionReader(f, off, st.Size()-16-off), st.Size()-16-off)
		if err != nil {
			t.Fatal(err)
		}
		files = map[string]string{}
		for _, zf := range zr.File {
			r, _ := zf.Open()
			b, _ := io.ReadAll(r)
			r.Close()
			files[zf.Name] = string(b)
		}
		return prefix, files
	}

	prefix, files := read(out)
	if !bytes.Equal(prefix, runnerBytes) {
		t.Fatal("runner bytes must be preserved verbatim")
	}
	if files["nautilus.yaml"] != manifest || files["program.st"] == "" {
		t.Fatalf("archive contents: %v", files)
	}

	// Rebuild from the BUILT binary with a changed project.
	os.WriteFile(filepath.Join(proj, "program.st"), []byte("PROGRAM P2\nEND_PROGRAM"), 0o644)
	out2 := filepath.Join(dir, "out2")
	if err := emitBinary(out, proj, out2, ""); err != nil {
		t.Fatal(err)
	}
	prefix2, files2 := read(out2)
	if !bytes.Equal(prefix2, runnerBytes) {
		t.Fatal("rebuilding from a built binary must strip the old archive")
	}
	if files2["program.st"] != "PROGRAM P2\nEND_PROGRAM" {
		t.Fatal("rebuild must carry the NEW project")
	}
}
