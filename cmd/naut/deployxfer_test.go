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
	"strconv"
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
if [ -n "$LB_PATH" ]; then PATH=$LB_PATH; export PATH; fi
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
	tr, err := planTransfer(ctl, sum, dir, base, true, "gzip")
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
	tr, err := planTransfer(ctl, sum, dir, base, true, "gzip")
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
	tr, err := planTransfer(ctl, sum, dir, boxBase{}, false, "gzip")
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
	tr, err := planTransfer(ctl, sum, dir, boxBase{}, false, "gzip")
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
	lb.install(t, "plant-old", makeController(t, append([]byte{0xff}, randomBytes(1<<20)...) /* not runtime: randomBytes is seeded, so a copy of it would be */, projectFiles(5, "A")))
	next := makeController(t, runtime, projectFiles(5, "B"))
	dir := t.TempDir()
	ctl, sum := writeCtl(t, dir, next)
	// the laptop believes the box holds `runtime`
	rsha := sha256.Sum256(runtime)
	tr, err := planTransfer(ctl, sum, dir, boxBase{release: "plant-old", rsize: int64(len(runtime)), rsha: hex.EncodeToString(rsha[:])}, true, "gzip")
	if err != nil {
		t.Fatal(err)
	}
	if tr.kind != "project" {
		t.Fatalf("plan %s, the test wants a project-only plan against a wrong box", tr.kind)
	}
	if code := b.sendController(tr, ctl, "plant-new", 0); code != 0 {
		t.Fatalf("send: %d", code)
	}
	if tr.kind != "full" {
		t.Errorf("the send ended as %s: the wrong runtime on the box was not noticed", tr.kind)
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
	tr, err := planTransfer(ctl, strings.Repeat("0", 64), dir, boxBase{}, false, "gzip")
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
	tr, err := planTransfer(ctl, sum, dir, boxBase{}, false, "gzip")
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

// A stall is not credit: after a long quiet the rate holds from there, and
// nothing bursts.
func TestThrottleDoesNotBurstAfterAStall(t *testing.T) {
	clock := time.Unix(0, 0)
	var slept time.Duration
	th := &throttle{r: bytes.NewReader(make([]byte, 100000)), bps: 10000,
		sleep: func(d time.Duration) { slept += d; clock = clock.Add(d) }, now: func() time.Time { return clock }}
	buf := make([]byte, 1000)
	th.Read(buf)
	clock = clock.Add(60 * time.Second) // the link went quiet for a minute
	slept = 0
	for i := 0; i < 20; i++ {
		th.Read(buf)
	}
	// 20 reads of 1000 bytes at 10000 B/s: about two seconds, not an
	// unthrottled burst of the minute's worth
	if slept < 1800*time.Millisecond {
		t.Errorf("only %v of waiting for 20 KB after a stall at 10 KB/s: it burst", slept)
	}
}

// An append goes only where the box holds exactly the offset the sender
// thinks: a cut-off earlier send that is still writing makes it ask again.
func TestAppendRefusesAWrongOffset(t *testing.T) {
	lb, b := newLocalBox(t)
	in := filepath.Join(lb.root, "incoming")
	os.MkdirAll(in, 0o755)
	part := filepath.Join(in, "x.wire.part")
	os.WriteFile(part, []byte("12345"), 0o644)
	if code := b.appendPart(strings.NewReader("ZZ"), "x.wire", 3); code != 6 {
		t.Errorf("an append at the wrong offset: exit %d, want 6", code)
	}
	if got, _ := os.ReadFile(part); string(got) != "12345" {
		t.Errorf("the part changed to %q", got)
	}
	if code := b.appendPart(strings.NewReader("67"), "x.wire", 5); code != 0 {
		t.Errorf("an append at the right offset: exit %d", code)
	}
	if got, _ := os.ReadFile(part); string(got) != "1234567" {
		t.Errorf("the part is %q, want 1234567", got)
	}
}

// A release id names one controller: the same id with other contents is
// refused, never silently skipped as "already current"; the same contents
// send nothing.
func TestAReusedReleaseIDWithOtherContentsIsRefused(t *testing.T) {
	log := fakeSSH(t, goMachine(), 0)
	dir := t.TempDir()
	ctl := makeController(t, randomBytes(1<<16), projectFiles(2, "A"))
	path, sum := writeCtl(t, dir, ctl)
	rt, rsha, _, _ := runtimePart(path)
	base := func(csha string) string {
		return fmt.Sprintf(`release=plant-v1\nruntime-size=%d\nruntime-sha256=%s\ncontroller-sha256=%s\n`, rt, rsha, csha)
	}
	t.Setenv("FAKE_BASE", base(strings.Repeat("0", 64)))
	if code := runDeploy([]string{"update", "-controller", path, "-id", "plant-v1", "root@ipc"}); code != 2 {
		t.Errorf("a reused id with other contents: exit %d, want 2", code)
	}
	for _, c := range sshCalls(t, log) {
		if strings.Contains(c.cmd, "append") || strings.Contains(c.cmd, " update ") {
			t.Errorf("the box was sent %q", c.cmd)
		}
	}
	log = fakeSSH(t, goMachine(), 0)
	t.Setenv("FAKE_BASE", base(sum))
	if code := runDeploy([]string{"update", "-controller", path, "-id", "plant-v1", "root@ipc"}); code != 0 {
		t.Errorf("the same contents: exit %d", code)
	}
	for _, c := range sshCalls(t, log) {
		if strings.Contains(c.cmd, "append") || strings.Contains(c.cmd, "assemble") {
			t.Errorf("an already-current release was sent again: %q", c.cmd)
		}
	}
}

func TestReleaseIDsThatCollideWithTheUploadFilesAreRefused(t *testing.T) {
	for _, id := range []string{"x.build", "x.part", "nautilus.service", "restore.tgz"} {
		if checkReleaseID(id) == nil {
			t.Errorf("-id %s accepted", id)
		}
	}
}

// Every helper from before the slow-link upload is accepted for the old
// upload (released helpers included); a helper's version is a hash of its
// box.sh and unit.
func TestLegacyHelperVersionsAreTheReleasedOnes(t *testing.T) {
	for _, v := range []string{"40e9c9fef68243e2", "5b0ccbee0a2913d3", "177ab940e0359f6e"} {
		if !legacyHelperVersions[v] {
			t.Errorf("helper %s (released before the slow-link upload) is not accepted", v)
		}
	}
	if legacyHelperVersions[helperVersion()] {
		t.Error("this naut's own helper is listed as legacy: it would never get the new upload")
	}
}

// The box probe says whether it can unpack xz, with or without a release: a
// first install is the biggest upload.
func TestParseBaseSaysWhetherTheBoxHasXZ(t *testing.T) {
	b, ok := parseBase("xz=1\n")
	if ok || !b.xz {
		t.Errorf("an empty box that has xz: %+v %v", b, ok)
	}
	b, ok = parseBase("xz=1\nrelease=plant-1\nruntime-size=1234\nruntime-sha256=" + strings.Repeat("a", 64) + "\n")
	if !ok || !b.xz || b.release != "plant-1" {
		t.Errorf("a box with a release and xz: %+v %v", b, ok)
	}
	if b, _ := parseBase("release=plant-1\n"); b.xz {
		t.Errorf("xz without the box saying so")
	}
}

// xz only when the box can unpack it and this machine can make it; gzip
// otherwise, and the plan line says why.
func TestPickCompression(t *testing.T) {
	_, haveXZ := exec.LookPath("xz")
	for _, c := range []struct {
		comp   string
		boxXZ  bool
		want   string
		whySub string
	}{
		{"gzip", true, "gzip", "-compress gzip"},
		{"auto", false, "gzip", "the box cannot unpack xz"},
		{"xz", false, "gzip", "the box has no xzcat"},
	} {
		if got, why := pickCompression(c.comp, c.boxXZ); got != c.want || !strings.Contains(why, c.whySub) {
			t.Errorf("pickCompression(%q, %v) = %q, %q; want %q containing %q", c.comp, c.boxXZ, got, why, c.want, c.whySub)
		}
	}
	if haveXZ == nil {
		if got, why := pickCompression("auto", true); got != "xz" || why != "" {
			t.Errorf("auto with xz on both ends: %q %q", got, why)
		}
	}
	t.Setenv("PATH", t.TempDir()) // no xz here
	if got, why := pickCompression("auto", true); got != "gzip" || !strings.Contains(why, "not installed here") {
		t.Errorf("auto without xz here: %q %q", got, why)
	}
}

func needXZ(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("xz"); err != nil {
		t.Skip("needs xz")
	}
}

// With xz at both ends the whole controller and the project-only send both
// go as xz and rebuild exactly; the plan line says xz; the wire file is the
// smaller one; the box's hash checks are on the unpacked bytes.
func TestXZUploadRebuildsTheController(t *testing.T) {
	needXZ(t)
	lb, b := newLocalBox(t)
	old := makeController(t, append(randomBytes(256<<10), bytes.Repeat([]byte("runtime text "), 60000)...), projectFiles(5, "A"))
	lb.install(t, "plant-old", old)
	base, ok, err := b.base()
	if err != nil || !ok || !base.xz {
		t.Fatalf("the box says %+v %v %v: want it to have xz", base, ok, err)
	}
	// a project-only change
	rt := old[:int(binary.LittleEndian.Uint64(old[len(old)-16:]))]
	next := makeController(t, rt, projectFiles(5, "B"))
	dir := t.TempDir()
	ctl, sum := writeCtl(t, dir, next)
	tr, err := planTransfer(ctl, sum, dir, base, true, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if tr.kind != "project" || tr.comp != "xz" || !strings.Contains(tr.summary(0, 0), "xz-compressed") {
		t.Fatalf("plan %s %s: %s", tr.kind, tr.comp, tr.summary(0, 0))
	}
	if code := b.sendController(tr, ctl, "plant-b", 0); code != 0 {
		t.Fatalf("send: %d", code)
	}
	if got := lb.incoming(t, "plant-b"); !bytes.Equal(got, next) {
		t.Error("the box did not rebuild the controller from the xz upload")
	}
	// a new runtime: the whole controller
	next2 := makeController(t, append(randomBytes(256<<10), bytes.Repeat([]byte("other runtime "), 60000)...), projectFiles(5, "B"))
	ctl2, sum2 := writeCtl(t, t.TempDir(), next2)
	tr2, _ := planTransfer(ctl2, sum2, dir, base, true, "auto")
	gz, _ := planTransfer(ctl2, sum2, dir, base, true, "gzip")
	// the stream is the small-box kind: the CRC32 check every decoder has
	// (busybox's too) and a decoder that needs about 10 MB, not 65
	lv, _ := exec.Command("xz", "-lvv", tr2.wire).CombinedOutput()
	if !strings.Contains(string(lv), "Check:             CRC32") || !strings.Contains(string(lv), "Memory needed:     9 MiB") {
		t.Errorf("the wire file is not a CRC32, preset-6 stream:\n%s", lv)
	}
	if tr2.kind != "full" || tr2.comp != "xz" || tr2.wireSize >= gz.wireSize {
		t.Errorf("the whole controller as xz: %s %s %d bytes against %d gzipped", tr2.kind, tr2.comp, tr2.wireSize, gz.wireSize)
	}
	if code := b.sendController(tr2, ctl2, "plant-c", 0); code != 0 {
		t.Fatalf("send full: %d", code)
	}
	if got := lb.incoming(t, "plant-c"); !bytes.Equal(got, next2) {
		t.Error("the box did not rebuild the whole controller from the xz upload")
	}
}

// A box with only busybox's xzcat (no xz, no unxz: a PLCnext) unpacks the
// stream xz makes, the check kind and block layout included.
func TestXZUploadUnpacksWithBusyboxXzcat(t *testing.T) {
	needXZ(t)
	bb, err := exec.LookPath("busybox")
	if err != nil {
		t.Skip("needs busybox")
	}
	if out, err := exec.Command(bb, "xzcat", "--help").CombinedOutput(); err != nil && !strings.Contains(string(out), "xzcat") {
		t.Skip("this busybox has no xzcat")
	}
	lb, b := newLocalBox(t)
	// the box's xzcat is busybox's, and the box has no xz or unxz at all
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "xzcat"), []byte("#!/bin/sh\nexec "+bb+" xzcat \"$@\"\n"), 0o755)
	hideFromBox(t, bin, "xz", "unxz", "xzcat")
	old := makeController(t, append(randomBytes(256<<10), bytes.Repeat([]byte("runtime text "), 60000)...), projectFiles(5, "A"))
	lb.install(t, "plant-old", old)
	base, _, _ := b.base()
	if !base.xz {
		t.Fatal("the box does not say it has xz")
	}
	next := makeController(t, append(randomBytes(256<<10), bytes.Repeat([]byte("other runtime "), 60000)...), projectFiles(5, "B"))
	dir := t.TempDir()
	ctl, sum := writeCtl(t, dir, next)
	tr, _ := planTransfer(ctl, sum, dir, base, true, "auto")
	if tr.comp != "xz" {
		t.Fatalf("plan %s", tr.comp)
	}
	if code := b.sendController(tr, ctl, "plant-bb", 0); code != 0 {
		t.Fatalf("send: %d", code)
	}
	if got := lb.incoming(t, "plant-bb"); !bytes.Equal(got, next) {
		t.Error("busybox's xzcat did not rebuild the controller")
	}
}

// hideFromBox makes the box's PATH a directory of everything the system has
// except the named tools, plus extra (the box's own shims).
func hideFromBox(t *testing.T, extra string, hide ...string) {
	t.Helper()
	dir := t.TempDir()
	skip := map[string]bool{}
	for _, h := range hide {
		skip[h] = true
	}
	for _, d := range []string{"/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"} {
		ents, _ := os.ReadDir(d)
		for _, e := range ents {
			if skip[e.Name()] {
				continue
			}
			os.Symlink(filepath.Join(d, e.Name()), filepath.Join(dir, e.Name())) // first wins; later dirs fail quietly
		}
	}
	ents, _ := os.ReadDir(extra)
	for _, e := range ents {
		os.Remove(filepath.Join(dir, e.Name()))
		os.Symlink(filepath.Join(extra, e.Name()), filepath.Join(dir, e.Name()))
	}
	t.Setenv("LB_PATH", dir)
}

// A box with no way to unpack xz is sent gzip, whatever -compress says, and
// the plan line says why; the upload still rebuilds the controller.
func TestABoxWithoutXZGetsGzip(t *testing.T) {
	lb, b := newLocalBox(t)
	hideFromBox(t, t.TempDir(), "xz", "unxz", "xzcat")
	old := makeController(t, append(randomBytes(256<<10), bytes.Repeat([]byte("runtime text "), 60000)...), projectFiles(5, "A"))
	lb.install(t, "plant-old", old)
	base, ok, err := b.base()
	if err != nil || !ok || base.xz {
		t.Fatalf("the box says %+v %v %v: want no xz", base, ok, err)
	}
	next := makeController(t, append(randomBytes(256<<10), bytes.Repeat([]byte("other runtime "), 60000)...), projectFiles(5, "B"))
	dir := t.TempDir()
	ctl, sum := writeCtl(t, dir, next)
	for _, comp := range []string{"auto", "xz"} {
		tr, err := planTransfer(ctl, sum, dir, base, true, comp)
		if err != nil {
			t.Fatal(err)
		}
		if tr.comp != "gzip" || !strings.Contains(tr.summary(0, 0), "gzip-compressed") || !strings.Contains(tr.note, "the box ") {
			t.Fatalf("-compress %s on a box with no xz: %s, %s", comp, tr.comp, tr.note)
		}
	}
	tr, _ := planTransfer(ctl, sum, dir, base, true, "auto")
	if code := b.sendController(tr, ctl, "plant-g", 0); code != 0 {
		t.Fatalf("send: %d", code)
	}
	if got := lb.incoming(t, "plant-g"); !bytes.Equal(got, next) {
		t.Error("the gzip upload did not rebuild the controller")
	}
	// and a box that is told to unpack xz it cannot refuses (exit 1, the failure of a box, not a damaged upload (4) or a wrong runtime (5))
	needXZ(t)
	xzTr, _ := planTransfer(ctl, sum, dir, boxBase{release: base.release, rsize: base.rsize, rsha: base.rsha, xz: true}, true, "xz")
	if xzTr.comp != "xz" {
		t.Fatalf("plan %s", xzTr.comp)
	}
	wireName := "plant-x." + xzTr.wireSha[:12] + ".wire"
	if err := b.uploadResumable(wireName, xzTr.wire, xzTr.wireSize, 0); err != nil {
		t.Fatal(err)
	}
	if code := b.root(os.Stdout, "assemble", wireName, xzTr.wireSha, "plant-x", strconv.FormatInt(xzTr.rsize, 10), xzTr.whole, "xz"); code != 1 {
		t.Errorf("a box with no xz was asked for an xz upload: exit %d, want 1", code)
	}
	if _, err := os.Stat(filepath.Join(lb.root, "incoming", "plant-x")); err == nil {
		t.Errorf("a controller was made from an upload the box cannot unpack")
	}
}

// A helper from before xz (the slow-link upload) is accepted and sent gzip.
func TestAHelperFromBeforeXZIsGzipOnly(t *testing.T) {
	if !gzipOnlyHelperVersions["5818797304182e21"] {
		t.Error("the helper of the slow-link upload is not accepted as gzip-only")
	}
	if gzipOnlyHelperVersions[helperVersion()] || legacyHelperVersions[helperVersion()] {
		t.Error("this naut's own helper is listed as an older one")
	}
}

// The whole-controller fallback after a wrong-runtime rebuild keeps the
// compression the plan chose: an xz plan falls back to xz, and the hash
// check on the unpacked bytes still decides.
func TestAWrongRuntimeFallsBackToTheWholeControllerAsXZ(t *testing.T) {
	needXZ(t)
	lb, b := newLocalBox(t)
	runtime := randomBytes(1 << 20)
	lb.install(t, "plant-old", makeController(t, append([]byte{0xff}, randomBytes(1<<20)...), projectFiles(5, "A")))
	next := makeController(t, runtime, projectFiles(5, "B"))
	dir := t.TempDir()
	ctl, sum := writeCtl(t, dir, next)
	rsha := sha256.Sum256(runtime)
	tr, err := planTransfer(ctl, sum, dir, boxBase{release: "plant-old", rsize: int64(len(runtime)), rsha: hex.EncodeToString(rsha[:]), xz: true}, true, "auto")
	if err != nil || tr.kind != "project" || tr.comp != "xz" {
		t.Fatalf("plan %+v %v", tr, err)
	}
	if code := b.sendController(tr, ctl, "plant-new", 0); code != 0 {
		t.Fatalf("send: %d", code)
	}
	if tr.kind != "full" || tr.comp != "xz" || !strings.HasSuffix(tr.wire, ".xz") {
		t.Errorf("the fallback is %s %s (%s): want the whole controller as xz", tr.kind, tr.comp, tr.wire)
	}
	if got := lb.incoming(t, "plant-new"); !bytes.Equal(got, next) {
		t.Error("the controller on the box is not the one built")
	}
}

// A first install asks the box what it can unpack before it sends anything,
// though there is no release to build on.
func TestAnEmptyBoxSaysItHasXZ(t *testing.T) {
	needXZ(t)
	_, b := newLocalBox(t) // nothing installed
	base, ok, err := b.base()
	if err != nil || ok || !base.xz {
		t.Fatalf("an empty box: %+v ok=%v %v: want xz and no release", base, ok, err)
	}
	next := makeController(t, append(randomBytes(256<<10), bytes.Repeat([]byte("runtime text "), 60000)...), projectFiles(5, "A"))
	dir := t.TempDir()
	ctl, sum := writeCtl(t, dir, next)
	tr, err := planTransfer(ctl, sum, dir, base, false, "auto")
	if err != nil || tr.kind != "full" || tr.comp != "xz" {
		t.Fatalf("a first install's plan: %+v %v", tr, err)
	}
	if code := b.sendController(tr, ctl, "plant-first", 0); code != 0 {
		t.Fatalf("send: %d", code)
	}
}

// A truncated xz stream whose hash was computed over what was cut (the link
// dropped and the caller assembled the part) never becomes a release; and a
// damaged one is thrown away (exit 4) before it is unpacked.
func TestADamagedXZUploadNeverBecomesAController(t *testing.T) {
	needXZ(t)
	lb, b := newLocalBox(t)
	old := makeController(t, append(randomBytes(256<<10), bytes.Repeat([]byte("runtime text "), 60000)...), projectFiles(5, "A"))
	lb.install(t, "plant-old", old)
	base, _, _ := b.base()
	next := makeController(t, append(randomBytes(256<<10), bytes.Repeat([]byte("other runtime "), 60000)...), projectFiles(5, "B"))
	dir := t.TempDir()
	ctl, sum := writeCtl(t, dir, next)
	tr, _ := planTransfer(ctl, sum, dir, base, true, "xz")
	wire, _ := os.ReadFile(tr.wire)
	// the stream cut short, with a hash that matches the cut: the unpacker says so
	cut := filepath.Join(dir, "cut.xz")
	os.WriteFile(cut, wire[:len(wire)/2], 0o644)
	h := sha256.Sum256(wire[:len(wire)/2])
	name := "plant-cut." + hex.EncodeToString(h[:])[:12] + ".wire"
	if err := b.uploadResumable(name, cut, int64(len(wire)/2), 0); err != nil {
		t.Fatal(err)
	}
	if code := b.root(os.Stdout, "assemble", name, hex.EncodeToString(h[:]), "plant-cut", strconv.FormatInt(tr.rsize, 10), tr.whole, "xz"); code != 1 {
		t.Errorf("a cut xz stream: exit %d, want 1", code)
	}
	if _, err := os.Stat(filepath.Join(lb.root, "incoming", "plant-cut")); err == nil {
		t.Errorf("a cut xz stream became a controller")
	}
	// an upload that does not match the hash the caller computed
	name2 := "plant-bad." + tr.wireSha[:12] + ".wire"
	bad := filepath.Join(dir, "bad.xz")
	flipped := append([]byte(nil), wire...)
	flipped[len(flipped)/2] ^= 0xff
	os.WriteFile(bad, flipped, 0o644)
	if err := b.uploadResumable(name2, bad, int64(len(flipped)), 0); err != nil {
		t.Fatal(err)
	}
	if code := b.root(os.Stdout, "assemble", name2, tr.wireSha, "plant-bad", strconv.FormatInt(tr.rsize, 10), tr.whole, "xz"); code != 4 {
		t.Errorf("a damaged xz upload: exit %d, want 4", code)
	}
}

// A box whose helper predates xz is told so, whatever it may have: the plan
// line's reason is the helper's, not "the box cannot unpack xz".
func TestAGzipOnlyHelpersPlanLineNamesTheHelper(t *testing.T) {
	tr := &transfer{kind: "full", comp: "gzip", note: "the whole controller, gzip-compressed: the box has no controller to build on (-compress xz, but the box has no xzcat, xz or unxz)"}
	helperGzipOnlyNote(tr)
	if strings.Contains(tr.note, "no xzcat") || !strings.HasSuffix(tr.note, "(this box's release helper predates xz)") || !strings.Contains(tr.note, "no controller to build on") {
		t.Errorf("note %q", tr.note)
	}
}

func TestACompressFlagIsChecked(t *testing.T) {
	if code := runDeploy([]string{"update", "-compress", "foo", "user@box"}); code != 2 {
		t.Errorf("-compress foo: exit %d, want 2", code)
	}
}
