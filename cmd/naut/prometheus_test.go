package main

// The generator's semantics are tested in prom/codegen. What is pinned here
// is the COMMAND and the artifacts: the golden files a re-run must
// reproduce byte for byte (testdata/prometheus, refresh with -update), that
// the generated manifest loads through the core's strict decoder, that the
// generated hw_types.st compiles, and that `serve` actually answers a real
// HTTP GET with the seeded body.

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/lang/st"
	"github.com/joyautomation/nautilus/prom/serve"
)

func prometheusTestdata(name string) string {
	return filepath.Join("testdata", "prometheus", name)
}

// capturePrometheus runs runPrometheus with stdout captured — captureModbus's
// twin (modbus_test.go), duplicated rather than shared because each proto's
// CLI test package is meant to stand alone if split out later.
func capturePrometheus(t *testing.T, args ...string) (string, int) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	code := runPrometheus(args)
	w.Close()
	os.Stdout = old
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	r.Close()
	return sb.String(), code
}

func promGoldenCompare(t *testing.T, golden string, got []byte) {
	t.Helper()
	path := prometheusTestdata(golden)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run `go test -run Prometheus -update ./cmd/naut` to create it)", err)
	}
	if string(want) != string(got) {
		t.Errorf("%s drifted from the golden:\n--- got ---\n%s\n--- want ---\n%s", golden, got, want)
	}
}

func importPrometheusInto(t *testing.T, dir string) (manifest, tags, types string) {
	t.Helper()
	args := []string{
		"import", "--file", prometheusTestdata("metrics.txt"),
		"--url", "http://bench1:9100/metrics", "--tag", "BENCH1", "--out", dir,
	}
	out, code := capturePrometheus(t, args...)
	if code != 0 {
		t.Fatalf("import failed (%d):\n%s", code, out)
	}
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	return read(filepath.Join(dir, "prometheus_manifest.yaml")),
		read(filepath.Join(dir, "tags", "prometheus.yaml")),
		read(filepath.Join(dir, "hw_types.st"))
}

func TestPrometheusImportGolden(t *testing.T) {
	manifest, tags, types := importPrometheusInto(t, t.TempDir())
	promGoldenCompare(t, "prometheus_manifest.yaml", []byte(manifest))
	promGoldenCompare(t, "tags_prometheus.yaml", []byte(tags))

	prog, err := st.Parse(types + "\nPROGRAM P\nVAR_EXTERNAL BENCH1 : Server; END_VAR\nEND_PROGRAM\n")
	if err != nil {
		t.Fatalf("generated hw_types.st does not parse: %v", err)
	}
	if _, err := st.Lower(prog); err != nil {
		t.Fatalf("generated hw_types.st does not lower: %v", err)
	}
}

// Re-running import must reproduce the exact same manifest and tag file —
// the "byte-identical on re-run" promise, exercised through the actual CLI
// rather than the codegen package directly.
func TestPrometheusImportIsByteIdenticalOnRerun(t *testing.T) {
	m1, tg1, _ := importPrometheusInto(t, t.TempDir())
	m2, tg2, _ := importPrometheusInto(t, t.TempDir())
	if m1 != m2 {
		t.Error("prometheus_manifest.yaml is not byte-identical on re-run")
	}
	if tg1 != tg2 {
		t.Error("tags/prometheus.yaml is not byte-identical on re-run")
	}
}

// `naut prometheus tags` re-derives the tag file from an already-committed
// manifest alone (no scrape needed) — names, roles and types match; desc
// is dropped, since only the scrape/codegen path knows it.
func TestPrometheusTagsFromManifest(t *testing.T) {
	dir := t.TempDir()
	importPrometheusInto(t, dir)
	out, code := capturePrometheus(t, "tags",
		"--out", filepath.Join(dir, "regen.yaml"),
		filepath.Join(dir, "prometheus_manifest.yaml"))
	if code != 0 {
		t.Fatalf("tags failed (%d):\n%s", code, out)
	}
	regen, err := os.ReadFile(filepath.Join(dir, "regen.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(regen), "name: BENCH1, role: input, type: Server") {
		t.Errorf("regenerated tag file missing BENCH1's Server tag:\n%s", regen)
	}
}

// `naut prometheus serve` stands the recorded fixture up as a real HTTP
// server and answers a GET with the seeded body.
func TestPrometheusServeAnswersHTTP(t *testing.T) {
	body, err := os.ReadFile(prometheusTestdata("metrics.txt"))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := serve.New(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "node_load1 0.5") {
		t.Errorf("serve did not answer with the seeded body:\n%s", got)
	}
}
