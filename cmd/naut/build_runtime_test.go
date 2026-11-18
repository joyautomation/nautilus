package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeELF is the 64-byte header of a little-endian ELF64 executable for
// machine, with no program or section headers: enough for debug/elf to
// name the platform, and a stand-in for a release's linux_arm64 naut.
func fakeELF(machine uint16) []byte {
	h := make([]byte, 64)
	copy(h, []byte{0x7f, 'E', 'L', 'F', 2 /* 64-bit */, 1 /* LE */, 1 /* version */})
	binary.LittleEndian.PutUint16(h[16:], 2) // ET_EXEC
	binary.LittleEndian.PutUint16(h[18:], machine)
	binary.LittleEndian.PutUint32(h[20:], 1)
	binary.LittleEndian.PutUint16(h[52:], 64) // e_ehsize
	return h
}

// -runtime builds the controller on another naut binary, byte for byte,
// and says which platform it targets: how an amd64 CI runner builds the
// controller an ARM64 IPC runs.
func TestBuildOnAnotherRuntime(t *testing.T) {
	dir := t.TempDir()
	runner := filepath.Join(dir, "naut-arm64")
	runnerBytes := append(fakeELF(183 /* EM_AARCH64 */), []byte("ARM64-RUNNER")...)
	if err := os.WriteFile(runner, runnerBytes, 0o755); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(dir, "proj")
	writeProjectFiles(t, proj, map[string]string{
		"nautilus.yaml": "name: arm\ntasks:\n  - program: p.st\n",
		"p.st":          "PROGRAM P\nEND_PROGRAM\n",
	})
	out := filepath.Join(dir, "arm")
	text, code := buildIn(t, proj, "-runtime", runner, "-o", out)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, text)
	}
	if !strings.Contains(text, "for linux/arm64") {
		t.Errorf("the build line should name the target platform: %s", text)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	off, ok := embeddedOffset(f, st.Size())
	if !ok {
		t.Fatal("no embedded project")
	}
	prefix := make([]byte, off)
	if _, err := f.ReadAt(prefix, 0); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(prefix, runnerBytes) {
		t.Fatal("the controller must start with the -runtime binary, byte for byte")
	}
}

// The release archive is the likeliest wrong thing to pass, and a file that
// is not an executable at all must not become a "controller" either.
func TestBuildRuntimeRefusesNonExecutables(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "proj")
	writeProjectFiles(t, proj, map[string]string{
		"nautilus.yaml": "name: arm\ntasks:\n  - program: p.st\n",
		"p.st":          "PROGRAM P\nEND_PROGRAM\n",
	})
	cases := map[string]struct {
		body []byte
		want string
	}{
		"nautilus_0.22.0_linux_arm64.tar.gz": {[]byte{0x1f, 0x8b, 8, 0, 0, 0}, "extract naut from it"},
		"notes.txt":                          {[]byte("hello"), "not an executable"},
		"missing":                            {nil, "no such file"},
	}
	for name, c := range cases {
		p := filepath.Join(dir, name)
		if c.body != nil {
			if err := os.WriteFile(p, c.body, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		out := filepath.Join(dir, "out-"+name)
		text, code := buildIn(t, proj, "-runtime", p, "-o", out)
		if code != 1 || !strings.Contains(text, c.want) {
			t.Errorf("%s: exit %d, want 1 with %q: %s", name, code, c.want, text)
		}
		if _, err := os.Stat(out); err == nil {
			t.Errorf("%s: a refused runtime must not leave an output", name)
		}
	}
}
