package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// fakeSSH puts an `ssh` on PATH that records each remote command and its
// stdin under the returned directory (0.cmd, 0.in, 1.cmd, ...), answers
// `uname -sm` as a Linux box of machine m, and exits updateExit for the
// box's update.
func fakeSSH(t *testing.T, m string, updateExit int) string {
	t.Helper()
	if goruntime.GOOS == "windows" {
		t.Skip("the fake ssh is a shell script")
	}
	bin, log := t.TempDir(), t.TempDir()
	script := `#!/bin/sh
for a; do last=$a; done
case " $* " in *" -O exit "*) exit 0 ;; esac
n=$(ls "$FAKE_SSH_LOG" | grep -c '\.cmd$')
printf '%s' "$last" >"$FAKE_SSH_LOG/$n.cmd"
cat >"$FAKE_SSH_LOG/$n.in"
case $last in
"uname -sm") echo "Linux $FAKE_UNAME_M" ;;
*"sh -s -- update "*) exit $FAKE_UPDATE_EXIT ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_SSH_LOG", log)
	t.Setenv("FAKE_UNAME_M", m)
	t.Setenv("FAKE_UPDATE_EXIT", strconv.Itoa(updateExit))
	t.Setenv("NAUT_SSH_OPTS", "")
	return log
}

type sshCall struct{ cmd, in string }

func sshCalls(t *testing.T, log string) []sshCall {
	t.Helper()
	names, _ := filepath.Glob(filepath.Join(log, "*.cmd"))
	sort.Slice(names, func(i, j int) bool {
		a, _ := strconv.Atoi(strings.TrimSuffix(filepath.Base(names[i]), ".cmd"))
		b, _ := strconv.Atoi(strings.TrimSuffix(filepath.Base(names[j]), ".cmd"))
		return a < b
	})
	var calls []sshCall
	for _, n := range names {
		cmd, _ := os.ReadFile(n)
		in, _ := os.ReadFile(strings.TrimSuffix(n, ".cmd") + ".in")
		calls = append(calls, sshCall{string(cmd), string(in)})
	}
	return calls
}

// fakeReleases serves a release the way GitHub does: the archive and a
// checksums.txt that lists it. corrupt makes the archive not match.
func fakeReleases(t *testing.T, version, arch string, naut []byte, corrupt bool) {
	t.Helper()
	var tgz bytes.Buffer
	zw := gzip.NewWriter(&tgz)
	tw := tar.NewWriter(zw)
	for _, f := range []struct {
		name string
		body []byte
	}{{"LICENSE", []byte("license")}, {"naut", naut}} {
		tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.body))})
		tw.Write(f.body)
	}
	tw.Close()
	zw.Close()
	name := fmt.Sprintf("nautilus_%s_linux_%s.tar.gz", version, arch)
	sum := sha256.Sum256(tgz.Bytes())
	sums := hex.EncodeToString(sum[:]) + "  " + name + "\n"
	archive := tgz.Bytes()
	if corrupt {
		archive = append(append([]byte{}, archive...), 0)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v" + version + "/checksums.txt":
			w.Write([]byte(sums))
		case "/v" + version + "/" + name:
			w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("NAUT_RELEASES_URL", srv.URL)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir()) // os.UserCacheDir on macOS
}

func deployProject(t *testing.T) string {
	t.Helper()
	proj := filepath.Join(t.TempDir(), "proj")
	writeProjectFiles(t, proj, map[string]string{
		"nautilus.yaml": "name: plant\ntasks:\n  - program: p.st\n",
		"p.st":          "PROGRAM P\nEND_PROGRAM\n",
	})
	return proj
}

// An update to an ARM64 box from any machine: naut asks the box its
// architecture, fetches that release's naut, checks it against the
// release's checksums.txt, builds on it, uploads the controller and the
// unit, and runs the box's update on the release it named.
func TestDeployUpdateBuildsForTheBox(t *testing.T) {
	log := fakeSSH(t, "aarch64", 0)
	runner := append(fakeELF(183 /* EM_AARCH64 */), []byte("ARM64-RUNNER")...)
	fakeReleases(t, "9.8.7", "arm64", runner, false)
	if code := runDeploy([]string{"update", "root@ipc", "-runtime", "v9.8.7", deployProject(t)}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	calls := sshCalls(t, log)
	if len(calls) != 4 {
		t.Fatalf("want 4 ssh calls (uname, controller, unit, update), got %d: %+v", len(calls), calls)
	}
	if calls[0].cmd != "uname -sm" {
		t.Errorf("first call should ask the box its architecture: %q", calls[0].cmd)
	}
	ctl := calls[1].in
	if !strings.HasPrefix(ctl, string(runner)) {
		t.Errorf("the controller was not built on the fetched arm64 naut")
	}
	sum := sha256.Sum256([]byte(ctl))
	id := "plant-" + hex.EncodeToString(sum[:])[:8] // not in git: no revision
	if !strings.Contains(calls[1].cmd, "/opt/nautilus/incoming/"+id+".part") {
		t.Errorf("controller upload goes to incoming/%s: %q", id, calls[1].cmd)
	}
	if !strings.Contains(calls[2].cmd, "/opt/nautilus/incoming/nautilus.service") || calls[2].in != string(deployUnit) {
		t.Errorf("second upload should be the unit: %q", calls[2].cmd)
	}
	if !strings.Contains(calls[3].cmd, "sh -s -- update "+id) || calls[3].in != deployBoxSh {
		t.Errorf("last call should feed box.sh `update %s`: %q", id, calls[3].cmd)
	}
	if !strings.Contains(calls[3].cmd, "sudo -n sh -s") {
		t.Errorf("a non-root user runs box.sh under sudo -n: %q", calls[3].cmd)
	}

	// The second deploy uses the cached runtime: the server is not asked.
	t.Setenv("NAUT_RELEASES_URL", "http://127.0.0.1:1")
	if code := runDeploy([]string{"update", "root@ipc", "-runtime", "9.8.7", deployProject(t)}); code != 0 {
		t.Fatalf("second update, from the cache: exit %d", code)
	}
}

// The box rolled the update back (box.sh exits 3): naut deploy exits 3,
// so CD fails the job.
func TestDeployUpdateExitStatusIsTheBoxs(t *testing.T) {
	fakeSSH(t, "x86_64", 3)
	ctl := filepath.Join(t.TempDir(), "ctl")
	os.WriteFile(ctl, []byte("#!/bin/sh\nexit 3\n"), 0o755)
	if code := runDeploy([]string{"update", "-controller", ctl, "root@ipc"}); code != 3 {
		t.Fatalf("exit %d, want the box's 3", code)
	}
}

// A checksum mismatch stops the deploy before anything reaches the box,
// and nothing is cached.
func TestDeployRefusesARuntimeThatFailsItsChecksum(t *testing.T) {
	log := fakeSSH(t, "aarch64", 0)
	fakeReleases(t, "9.8.7", "arm64", fakeELF(183), true)
	if code := runDeploy([]string{"install", "root@ipc", "-runtime", "v9.8.7", deployProject(t)}); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if calls := sshCalls(t, log); len(calls) != 1 {
		t.Errorf("only the architecture question should reach the box, got %+v", calls)
	}
	cache, _ := os.UserCacheDir()
	if _, err := os.Stat(filepath.Join(cache, "nautilus", "naut_9.8.7_linux_arm64", "naut")); err == nil {
		t.Error("a runtime that failed its checksum was cached")
	}
}

// Without -runtime, a development naut has no release to fetch for a box
// of another architecture: say so rather than build the wrong binary.
func TestDeployDevBuildNeedsARuntimeForAnotherArch(t *testing.T) {
	other := "aarch64"
	if goruntime.GOARCH == "arm64" {
		other = "x86_64"
	}
	log := fakeSSH(t, other, 0)
	if code := runDeploy([]string{"install", "root@ipc", deployProject(t)}); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if calls := sshCalls(t, log); len(calls) != 1 {
		t.Errorf("nothing but the architecture question should reach the box, got %+v", calls)
	}
}

func TestReleaseID(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	for _, c := range []struct{ name, rev, want string }{
		{"plant", "v1.2.0", "plant-v1.2.0-abababab"},
		{"plant", "v1.2.0-3-g1a2b3c4-dirty", "plant-v1.2.0-3-g1a2b3c4-dirty-abababab"},
		{"plant", "", "plant-abababab"},
		{"my plant/x", "", "my_plant_x-abababab"},
		{"-rf", "$(reboot)", "rf-__reboot_-abababab"},
	} {
		if got := releaseID(c.name, c.rev, sum); got != c.want {
			t.Errorf("releaseID(%q, %q) = %q, want %q", c.name, c.rev, got, c.want)
		}
	}
}

func TestUnameArch(t *testing.T) {
	for m, want := range map[string]string{"aarch64": "arm64", "x86_64": "amd64", "armv7l": "armv7"} {
		if got, err := unameArch(m); err != nil || got != want {
			t.Errorf("unameArch(%q) = %q, %v; want %q", m, got, err, want)
		}
	}
	if _, err := unameArch("riscv64"); err == nil {
		t.Error("riscv64 has no release")
	}
	for v, want := range map[string]bool{"0.22.0": true, "v0.22.0": true, "dev": false, "0.0.0-20261117-abc": false, "v1.2": false} {
		if isReleaseVersion(v) != want {
			t.Errorf("isReleaseVersion(%q) != %v", v, want)
		}
	}
}

// box.sh tells a controller that notifies systemd by finding WATCHDOG=1 in
// it. box.sh is embedded in every naut, so in every controller: spelled
// whole, its own pattern would match, and a release without notification
// would get Type=notify and time out at start (seen on the first run of
// tools/deploy/check.sh).
func TestDeployBoxDoesNotMatchItself(t *testing.T) {
	for name, text := range map[string]string{"box.sh": deployBoxSh, "nautilus.service": string(deployUnit), "usage": deployUsage} {
		if strings.Contains(text, "WATCHDOG=1") {
			t.Errorf("%s spells WATCHDOG=1 whole; every controller would look like it notifies systemd", name)
		}
	}
	if !strings.Contains(deployBoxSh, `grep -q 'WATCHDOG''=1'`) {
		t.Error("box.sh no longer looks for WATCHDOG=1 the way this test expects")
	}
}
