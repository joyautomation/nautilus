package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"
)

// makeController is a built controller as naut build writes one: a runtime,
// a project zip and the footer that holds where the zip starts.
func makeController(t *testing.T, runtime []byte, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	b.Write(runtime)
	zw := zip.NewWriter(&b)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sortStrings(names)
	for _, n := range names {
		w, _ := zw.Create(n)
		w.Write([]byte(files[n]))
	}
	zw.Close()
	var footer [16]byte
	binary.LittleEndian.PutUint64(footer[:8], uint64(len(runtime)))
	copy(footer[8:], embedMagic[:])
	b.Write(footer[:])
	return b.Bytes()
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// project is a project of n files of about 2 KB of program text each, with
// the rung of one of them set to rung.
func projectFiles(n int, rung string) map[string]string {
	files := map[string]string{"nautilus.yaml": "name: plant\ntasks:\n  - program: p0.st\n"}
	for i := 0; i < n; i++ {
		var sb strings.Builder
		fmt.Fprintf(&sb, "PROGRAM P%d\nVAR a, b, c : REAL; run : BOOL; END_VAR\n", i)
		for j := 0; j < 60; j++ {
			fmt.Fprintf(&sb, "IF run AND a > %d.0 THEN b := b + c * %d.5; END_IF;\n", j, i)
		}
		if i == 3 {
			sb.WriteString(rung + "\n")
		}
		sb.WriteString("END_PROGRAM\n")
		files[fmt.Sprintf("p%d.st", i)] = sb.String()
	}
	return files
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(7)).Read(b)
	return b
}

// localBox is a box.sh in cron mode on this machine, reached through an ssh
// stand-in that runs the remote command here. dropAfter, when it names a
// file holding a number, cuts the next append off after that many bytes and
// exits 255, as a radio link does.
type localBox struct {
	root, dropFile string
}

func newLocalBox(t *testing.T) (*localBox, *sshBox) {
	t.Helper()
	if goruntime.GOOS != "linux" {
		t.Skip("box.sh runs on this machine")
	}
	for _, c := range []string{"sha256sum", "od", "gunzip", "head", "tail"} {
		if _, err := exec.LookPath(c); err != nil {
			t.Skip("needs " + c)
		}
	}
	tmp := t.TempDir()
	lb := &localBox{root: filepath.Join(tmp, "naut"), dropFile: filepath.Join(tmp, "drop")}
	bin := filepath.Join(tmp, "bin")
	os.MkdirAll(bin, 0o755)
	script := `#!/bin/sh
for a; do last=$a; done
case " $* " in *" -O exit "*) exit 0 ;; esac
case $last in
*"cat >> "*)
	if [ -f "$LB_DROP" ]; then
		n=$(cat "$LB_DROP"); rm -f "$LB_DROP"
		head -c "$n" | sh -c "$last"
		exit 255
	fi ;;
esac
exec sh -c "$last"
`
	os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LB_DROP", lb.dropFile)
	t.Setenv("NAUT_SSH_OPTS", "")
	old := deployRetryWait
	deployRetryWait = time.Millisecond
	t.Cleanup(func() { deployRetryWait = old })
	b, err := newSSHBox("box")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.close)
	b.known, b.cron, b.cronRoot = true, true, lb.root
	return lb, b
}

// install puts ctl on the box as the current release id.
func (lb *localBox) install(t *testing.T, id string, ctl []byte) {
	t.Helper()
	dir := filepath.Join(lb.root, "releases", id)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "controller"), ctl, 0o755)
	os.Remove(filepath.Join(lb.root, "current"))
	if err := os.Symlink(filepath.Join("releases", id), filepath.Join(lb.root, "current")); err != nil {
		t.Fatal(err)
	}
}

func (lb *localBox) incoming(t *testing.T, id string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(lb.root, "incoming", id))
	if err != nil {
		t.Fatalf("nothing became incoming/%s: %v", id, err)
	}
	return b
}

func writeCtl(t *testing.T, dir string, ctl []byte) (path, sum string) {
	t.Helper()
	path = filepath.Join(dir, "controller")
	if err := os.WriteFile(path, ctl, 0o755); err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(ctl)
	return path, hex.EncodeToString(s[:])
}

// The headline: a one-rung change, built on the runtime the box already has,
// sends the project only (under 64 KB here, where the controller is over
// 3 MB), and the box rebuilds a file byte for byte the controller built.
func TestProjectOnlySendForAOneRungChange(t *testing.T) {
	lb, b := newLocalBox(t)
	runtime := randomBytes(3 << 20) // incompressible: what a real runtime nearly is
	old := makeController(t, runtime, projectFiles(40, "IF run THEN a := 1.0; END_IF;"))
	lb.install(t, "plant-old", old)
	next := makeController(t, runtime, projectFiles(40, "IF run THEN a := 2.0; END_IF;"))
	dir := t.TempDir()
	ctl, sum := writeCtl(t, dir, next)

	base, ok, err := b.base()
	if err != nil || !ok || base.release != "plant-old" {
		t.Fatalf("base: %+v %v %v", base, ok, err)
	}
	tr, err := planTransfer(ctl, sum, dir, base, true)
	if err != nil {
		t.Fatal(err)
	}
	if tr.kind != "project" || tr.wireSize > 64<<10 {
		t.Fatalf("plan %s of %d bytes, want the project only under 64 KB (the controller is %d)", tr.kind, tr.wireSize, len(next))
	}
	if code := b.sendController(tr, ctl, "plant-new", 0); code != 0 {
		t.Fatalf("send: %d", code)
	}
	if got := lb.incoming(t, "plant-new"); !bytes.Equal(got, next) {
		t.Errorf("the box rebuilt %d bytes that are not the %d-byte controller", len(got), len(next))
	}
	if _, err := os.Stat(filepath.Join(lb.root, "incoming", "plant-new."+tr.wireSha[:12]+".wire.part")); err == nil {
		t.Error("the upload was left in incoming after it became a release")
	}
}

// A different runtime (an upgrade, or a different naut) sends the whole
// controller, gzipped.
func TestANewRuntimeSendsTheWholeControllerGzipped(t *testing.T) {
	lb, b := newLocalBox(t)
	old := makeController(t, randomBytes(1<<20), projectFiles(5, "A"))
	lb.install(t, "plant-old", old)
	rt2 := append(randomBytes(1 << 20)[:512<<10], bytes.Repeat([]byte("compressible "), 40000)...)
	next := makeController(t, rt2, projectFiles(5, "A"))
	dir := t.TempDir()
	ctl, sum := writeCtl(t, dir, next)
	base, _, _ := b.base()
	tr, err := planTransfer(ctl, sum, dir, base, true)
	if err != nil {
		t.Fatal(err)
	}
	if tr.kind != "full" || tr.wireSize >= int64(len(next)) {
		t.Fatalf("plan %s of %d bytes for a %d-byte controller: want full and gzipped smaller", tr.kind, tr.wireSize, len(next))
	}
	if code := b.sendController(tr, ctl, "plant-new", 0); code != 0 {
		t.Fatalf("send: %d", code)
	}
	if got := lb.incoming(t, "plant-new"); !bytes.Equal(got, next) {
		t.Error("the box did not rebuild the controller")
	}
}

// A dropped link resumes where the box left off, and no byte goes twice.
func TestADroppedLinkResumes(t *testing.T) {
	lb, b := newLocalBox(t)
	next := makeController(t, randomBytes(2<<20), projectFiles(5, "A"))
	dir := t.TempDir()
	ctl, sum := writeCtl(t, dir, next)
	tr, err := planTransfer(ctl, sum, dir, boxBase{}, false)
	if err != nil {
		t.Fatal(err)
	}
	cut := tr.wireSize / 3
	os.WriteFile(lb.dropFile, []byte(fmt.Sprint(cut)), 0o644)
	if code := b.sendController(tr, ctl, "plant-new", 0); code != 0 {
		t.Fatalf("send: %d", code)
	}
	if got := lb.incoming(t, "plant-new"); !bytes.Equal(got, next) {
		t.Error("the controller on the box is not the one built")
	}
	if _, err := os.Stat(lb.dropFile); err == nil {
		t.Error("the link was never dropped: the test did not test resuming")
	}
}

// A partial upload that is damaged (what the box holds is not a prefix of the
// file) is found by the hash, thrown away and sent again from nothing; a
// half-written release never exists.
func TestADamagedPartialUploadIsRestarted(t *testing.T) {
	lb, b := newLocalBox(t)
	next := makeController(t, randomBytes(1<<20), projectFiles(5, "A"))
	dir := t.TempDir()
	ctl, sum := writeCtl(t, dir, next)
	tr, err := planTransfer(ctl, sum, dir, boxBase{}, false)
	if err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(lb.root, "incoming")
	os.MkdirAll(in, 0o755)
	part := filepath.Join(in, "plant-new."+tr.wireSha[:12]+".wire.part")
	os.WriteFile(part, bytes.Repeat([]byte{0xAA}, int(tr.wireSize/2)), 0o644)
	if code := b.sendController(tr, ctl, "plant-new", 0); code != 0 {
		t.Fatalf("send: %d", code)
	}
	if got := lb.incoming(t, "plant-new"); !bytes.Equal(got, next) {
		t.Error("the controller on the box is not the one built")
	}
}

// A box whose runtime is not the one the laptop assumed (it changed between
// the question and the rebuild) fails the whole-file check, and the whole
// controller is sent instead; nothing wrong becomes a release.
func TestAWrongRuntimeOnTheBoxFallsBackToTheWholeController(t *testing.T) {
	lb, b := newLocalBox(t)
	runtime := randomBytes(1 << 20)
	lb.install(t, "plant-old", makeController(t, randomBytes(1<<20) /* not runtime */, projectFiles(5, "A")))
	next := makeController(t, runtime, projectFiles(5, "B"))
	dir := t.TempDir()
	ctl, sum := writeCtl(t, dir, next)
	// the laptop believes the box holds `runtime`
	rsha := sha256.Sum256(runtime)
	tr, err := planTransfer(ctl, sum, dir, boxBase{release: "plant-old", rsize: int64(len(runtime)), rsha: hex.EncodeToString(rsha[:])}, true)
	if err != nil {
		t.Fatal(err)
	}
	if tr.kind != "project" {
		t.Fatalf("plan %s, the test wants a project-only plan against a wrong box", tr.kind)
	}
	if code := b.sendController(tr, ctl, "plant-new", 0); code != 0 {
		t.Fatalf("send: %d", code)
	}
	if got := lb.incoming(t, "plant-new"); !bytes.Equal(got, next) {
		t.Error("the controller on the box is not the one built")
	}
}

// Nothing the box rebuilds becomes a release unless it hashes to what was
// built: a wrong whole-file hash is refused (exit 5) and nothing is named.
func TestTheBoxRefusesAControllerThatDoesNotHash(t *testing.T) {
	lb, b := newLocalBox(t)
	next := makeController(t, randomBytes(1<<18), projectFiles(2, "A"))
	dir := t.TempDir()
	ctl, _ := writeCtl(t, dir, next)
	tr, err := planTransfer(ctl, strings.Repeat("0", 64), dir, boxBase{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.uploadResumable("plant-new.x.wire", tr.wire, tr.wireSize, 0); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	code := b.root(&out, "assemble", "plant-new.x.wire", tr.wireSha, "plant-new", "0", strings.Repeat("0", 64))
	if code != 5 {
		t.Errorf("assemble exit %d, want 5", code)
	}
	if _, err := os.Stat(filepath.Join(lb.root, "incoming", "plant-new")); err == nil {
		t.Error("a controller that does not hash became incoming/plant-new")
	}
	if _, err := os.Stat(filepath.Join(lb.root, "incoming", "plant-new.build")); err == nil {
		t.Error("the rejected rebuild was left behind")
	}
}

// -limit holds the send to a rate: 64 KB at 640 kbit/s takes about a second.
func TestLimitHoldsTheSendRate(t *testing.T) {
	_, b := newLocalBox(t)
	next := makeController(t, randomBytes(70<<10), projectFiles(1, "A"))
	dir := t.TempDir()
	ctl, sum := writeCtl(t, dir, next)
	tr, err := planTransfer(ctl, sum, dir, boxBase{}, false)
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	if code := b.sendController(tr, ctl, "plant-new", 640_000); code != 0 {
		t.Fatalf("send: %d", code)
	}
	want := time.Duration(float64(tr.wireSize) * 8 / 640_000 * float64(time.Second))
	if got := time.Since(t0); got < want*8/10 {
		t.Errorf("sent %d bytes in %v at a 640 kbit/s limit, want at least %v", tr.wireSize, got, want*8/10)
	}
}

func TestThrottlePacesTheReader(t *testing.T) {
	var slept time.Duration
	clock := time.Unix(0, 0)
	th := &throttle{r: bytes.NewReader(make([]byte, 10000)), bps: 10000, // 80 kbit/s
		sleep: func(d time.Duration) { slept += d; clock = clock.Add(d) }, now: func() time.Time { return clock }}
	buf := make([]byte, 4096)
	total := 0
	for {
		n, err := th.Read(buf)
		total += n
		if err != nil {
			break
		}
	}
	if total != 10000 || slept < 950*time.Millisecond || slept > 1050*time.Millisecond {
		t.Errorf("read %d bytes with %v of waiting, want 10000 over about a second", total, slept)
	}
}

func TestParseRate(t *testing.T) {
	for in, want := range map[string]int64{"256k": 256000, "1M": 1000000, "64000": 64000, "0.5M": 500000} {
		if got, err := parseRate(in); err != nil || got != want {
			t.Errorf("parseRate(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "fast", "-1k", "0"} {
		if _, err := parseRate(bad); err == nil {
			t.Errorf("parseRate(%q) accepted", bad)
		}
	}
}

// The summary says how many bytes and how long, assumes 256 kbit/s unless told
// otherwise, and suggests -limit for a long send.
func TestSummaryEstimates(t *testing.T) {
	small := &transfer{kind: "project", wireSize: 1 << 20, note: "the project only"}
	if s := small.summary(0, 0); !strings.Contains(s, "1.0 MB (project)") || !strings.Contains(s, "33 s at 256 kbit/s") || strings.Contains(s, "-limit") {
		t.Errorf("a 1 MB project send: %s", s)
	}
	big := &transfer{kind: "full", wireSize: 20 << 20, note: "the whole controller"}
	if s := big.summary(0, 0); !strings.Contains(s, "11 min at 256 kbit/s") || !strings.Contains(s, "-limit 128k") {
		t.Errorf("a 20 MB send: %s", s)
	}
	if s := big.summary(1_000_000, 0); !strings.Contains(s, "at 1 Mbit/s") || strings.Contains(s, "assumed") {
		t.Errorf("with -bandwidth: %s", s)
	}
	if s := big.summary(0, 128_000); !strings.Contains(s, "limited to 128 kbit/s") || strings.Contains(s, "crowd") {
		t.Errorf("with -limit: %s", s)
	}
}

func TestParseBase(t *testing.T) {
	b, ok := parseBase("release=plant-1\nruntime-size=1234\nruntime-sha256=" + strings.Repeat("a", 64) + "\n")
	if !ok || b.release != "plant-1" || b.rsize != 1234 {
		t.Errorf("%+v %v", b, ok)
	}
	for _, bad := range []string{"", "release=x\n", "release=x\nruntime-size=0\nruntime-sha256=" + strings.Repeat("a", 64)} {
		if _, ok := parseBase(bad); ok {
			t.Errorf("parseBase(%q) accepted", bad)
		}
	}
}

// --dry-run says what would go and sends nothing: no upload, no unit, no update.
func TestDryRunSendsNothing(t *testing.T) {
	log := fakeSSH(t, goMachine(), 0)
	if code := runDeploy([]string{"update", "root@ipc", "-dry-run", deployProject(t)}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, c := range sshCalls(t, log) {
		for _, verb := range []string{"append", "assemble", "update ", "partsize", "receive", "nautilus.service"} {
			if strings.Contains(c.cmd, verb) {
				t.Errorf("a dry run called the box with %q: %s", verb, c.cmd)
			}
		}
	}
}

func TestDeployRateFlagsAreChecked(t *testing.T) {
	fakeSSH(t, goMachine(), 0)
	for _, args := range [][]string{{"-limit", "fast"}, {"-bandwidth", "-3"}} {
		if code := runDeploy(append([]string{"update", "root@ipc"}, append(args, deployProject(t))...)); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}
