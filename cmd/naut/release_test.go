package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// buildNautCLI compiles this package into dir/naut with extra env and go
// build flags and returns its path.
func buildNautCLI(t *testing.T, dir string, env []string, flags ...string) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}
	bin := filepath.Join(dir, "naut")
	args := append([]string{"build"}, flags...)
	args = append(args, "-o", bin, ".")
	cmd := exec.Command(goBin, args...)
	cmd.Env = append(os.Environ(), env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build (%v): %v\n%s", env, err, out)
	}
	return bin
}

// `naut version` answers once the CLI is built, printing the version the
// release stamps in with -X.
func TestNautVersionAnswers(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the CLI")
	}
	bin := buildNautCLI(t, t.TempDir(), nil,
		"-ldflags", "-X github.com/joyautomation/nautilus/internal/lsp.Version=9.8.7")
	for _, arg := range []string{"version", "--version"} {
		out, err := exec.Command(bin, arg).CombinedOutput()
		if err != nil {
			t.Fatalf("naut %s: %v\n%s", arg, err, out)
		}
		if got := strings.TrimSpace(string(out)); got != "nautilus 9.8.7" {
			t.Errorf("naut %s = %q, want %q", arg, got, "nautilus 9.8.7")
		}
	}
}

// naut build says so when the embedded project passes the ~50 MB comfort
// threshold, and stays quiet below it. The payload is zeros, so it zips
// small; the threshold is on the uncompressed total.
func TestBuildWarnsWhenEmbeddedProjectIsOver50MB(t *testing.T) {
	if testing.Short() {
		t.Skip("writes a 51 MB project file")
	}
	dir := t.TempDir()
	writeProjectFiles(t, dir, map[string]string{
		"nautilus.yaml": "name: t\ntasks:\n  - program: p.st\n",
		"p.st":          "PROGRAM P\nEND_PROGRAM",
	})
	output, code := buildIn(t, dir, "-o", filepath.Join(t.TempDir(), "small-bin"))
	if code != 0 || strings.Contains(output, "comfort threshold") {
		t.Fatalf("a small project must build without the size warning (code %d): %q", code, output)
	}

	if err := os.WriteFile(filepath.Join(dir, "assets.bin"), make([]byte, 51*1024*1024), 0o644); err != nil {
		t.Fatal(err)
	}
	output, code = buildIn(t, dir, "-o", filepath.Join(t.TempDir(), "big-bin"))
	if code != 0 {
		t.Fatalf("the warning is not a failure: code %d, output %q", code, output)
	}
	if !strings.Contains(output, "warning: the embedded project is 51.") ||
		!strings.Contains(output, "over the 50 MB comfort threshold") {
		t.Fatalf("a 51 MB project must warn about its size: %q", output)
	}
}

// The release configuration promises macOS, Linux and Windows on amd64 and
// arm64, plus Linux on ARMv7, with no runtime dependencies (CGO off) and a
// checksums.txt beside the archives; the CLI must really compile for every
// one of those targets.
func TestReleaseTargetsBuildWithoutCgo(t *testing.T) {
	raw, err := os.ReadFile("../../.goreleaser.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Builds []struct {
			Main   string   `yaml:"main"`
			Env    []string `yaml:"env"`
			Goos   []string `yaml:"goos"`
			Goarch []string `yaml:"goarch"`
			Goarm  []string `yaml:"goarm"`
			Ignore []struct {
				Goos   string `yaml:"goos"`
				Goarch string `yaml:"goarch"`
			} `yaml:"ignore"`
		} `yaml:"builds"`
		Checksum struct {
			Name string `yaml:"name_template"`
		} `yaml:"checksum"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Builds) != 1 || cfg.Builds[0].Main != "./cmd/naut" {
		t.Fatalf("want one release build of ./cmd/naut, got %+v", cfg.Builds)
	}
	b := cfg.Builds[0]
	if !slices.Contains(b.Env, "CGO_ENABLED=0") {
		t.Errorf("release build env %v must set CGO_ENABLED=0 (no runtime dependencies)", b.Env)
	}
	for _, goos := range []string{"darwin", "linux", "windows"} {
		if !slices.Contains(b.Goos, goos) {
			t.Errorf("goos %v is missing %s", b.Goos, goos)
		}
	}
	for _, arch := range []string{"amd64", "arm64"} {
		if !slices.Contains(b.Goarch, arch) {
			t.Errorf("goarch %v is missing %s", b.Goarch, arch)
		}
	}
	if cfg.Checksum.Name != "checksums.txt" {
		t.Errorf("checksum name = %q, want checksums.txt", cfg.Checksum.Name)
	}

	if testing.Short() {
		t.Skip("cross-compiles the CLI for every release target")
	}
	ignored := func(goos, goarch string) bool {
		for _, ig := range b.Ignore {
			if ig.Goos == goos && ig.Goarch == goarch {
				return true
			}
		}
		return false
	}
	for _, goos := range b.Goos {
		for _, goarch := range b.Goarch {
			if ignored(goos, goarch) {
				continue
			}
			env := []string{"CGO_ENABLED=0", "GOOS=" + goos, "GOARCH=" + goarch}
			name := goos + "_" + goarch
			if goarch == "arm" {
				if len(b.Goarm) != 1 {
					t.Fatalf("goarm %v: want exactly one ARM version", b.Goarm)
				}
				env = append(env, "GOARM="+b.Goarm[0])
				name += "v" + b.Goarm[0]
			}
			t.Run(name, func(t *testing.T) {
				buildNautCLI(t, t.TempDir(), env)
			})
		}
	}
}
