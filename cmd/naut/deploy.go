package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/internal/lsp"
	"github.com/joyautomation/nautilus/internal/project"
)

// naut deploy puts a controller on a Linux box with systemd over SSH (#721).
// The half that runs on the box is deploybox/box.sh, fed to `sh -s` as
// root; this file builds, uploads and calls it. Nothing is installed on the
// box beyond the controller and its unit.

//go:embed deploybox/box.sh
var deployBoxSh string

//go:embed deploybox/nautilus.service
var deployUnit []byte

const deployUsage = `usage: naut deploy <command> <ssh-target> ...

Puts a controller on a Linux box with systemd, over SSH, and keeps the
release before it so that one command goes back.

  install   <ssh-target> [project-dir]  build the project, install it as nautilus.service, start it
  update    <ssh-target> [project-dir]  build, make it current and restart; if it does not start,
                                        put the release before it back and exit 3
  rollback  <ssh-target>                swap current and previous and restart (again goes forward)
  status    <ssh-target>
  backup    <ssh-target> [-o file.tgz]  the retained values, and the release they ran on
  restore   <ssh-target> <file.tgz>     stop, put the retained values back, start
  uninstall <ssh-target> [-purge]       remove the unit and the releases; -purge deletes the
                                        retained values too

install and update:
  -runtime <naut>|vX.Y.Z  the naut the controller is built on. Default: this naut when the box
                          runs the same OS and architecture, otherwise this naut's own release
                          for the box's architecture, downloaded from GitHub and checked
                          against the release's checksums.txt
  -controller <file>      deploy a controller already built with naut build
  -m <manifest>           as for naut build
  -id <id>                the release's name on the box
                          (default <name>-<git describe>-<first 8 hex of its sha256>)

<ssh-target> is anything ssh takes: user@host, or a Host from ~/.ssh/config.
The user must be root or have sudo without a password. NAUT_SSH_OPTS adds
ssh options, e.g. "-p 2222 -i ~/.ssh/ipc".

On the box:
  /opt/nautilus/releases/<id>/controller   one per release (the newest 3 besides current and previous)
  /opt/nautilus/current, previous          links to releases; nautilus.service runs current
  /var/lib/nautilus/retain.json            retained values, in no release, so every release keeps them
`

func runDeploy(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, deployUsage)
		return 2
	}
	switch args[0] {
	case "help", "-h", "-help", "--help":
		fmt.Print(deployUsage)
		return 0
	}
	cmd := args[0]
	fs := flag.NewFlagSet("deploy "+cmd, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, deployUsage) }
	var (
		runner, ctl, manifest, id, out string
		purge                          bool
	)
	nargs := [2]int{1, 1} // positional arguments, min and max
	switch cmd {
	case "install", "update":
		fs.StringVar(&runner, "runtime", "", "the naut the controller is built on: a path, or vX.Y.Z to download")
		fs.StringVar(&ctl, "controller", "", "deploy this controller instead of building one")
		fs.StringVar(&manifest, "m", "", manifestFlagUsage)
		fs.StringVar(&id, "id", "", "the release's name on the box")
		nargs = [2]int{1, 2}
	case "backup":
		fs.StringVar(&out, "o", "", "the file to write (default nautilus-retain-<target>-<time>.tgz)")
	case "restore":
		nargs = [2]int{2, 2}
	case "uninstall":
		fs.BoolVar(&purge, "purge", false, "delete the retained values too")
	case "rollback", "status":
	default:
		fmt.Fprintf(os.Stderr, "naut deploy: unknown command %q\n\n%s", cmd, deployUsage)
		return 2
	}
	pos, err := parseInterspersed(fs, args[1:])
	if err != nil {
		return 2
	}
	if len(pos) < nargs[0] || len(pos) > nargs[1] {
		fmt.Fprintf(os.Stderr, "naut deploy %s: wrong number of arguments\n\n%s", cmd, deployUsage)
		return 2
	}
	if strings.HasPrefix(pos[0], "-") {
		fmt.Fprintf(os.Stderr, "naut deploy: %q is not an ssh target\n", pos[0])
		return 2
	}
	if ctl != "" && len(pos) > 1 {
		fmt.Fprintln(os.Stderr, "naut deploy: -controller and a project directory: give one")
		return 2
	}
	b, err := newSSHBox(pos[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut deploy:", err)
		return 1
	}
	defer b.close()

	switch cmd {
	case "install", "update":
		dir := "."
		if len(pos) > 1 {
			dir = pos[1]
		}
		return deployRelease(b, cmd, dir, ctl, runner, manifest, id)
	case "backup":
		if out == "" {
			out = fmt.Sprintf("nautilus-retain-%s-%s.tgz", cleanID(b.target), time.Now().Format("20060102T150405"))
		}
		f, err := os.Create(out + ".part")
		if err != nil {
			fmt.Fprintln(os.Stderr, "naut deploy:", err)
			return 1
		}
		code := b.root(f, "backup")
		if cerr := f.Close(); code == 0 && cerr != nil {
			fmt.Fprintln(os.Stderr, "naut deploy:", cerr)
			code = 1
		}
		if code != 0 {
			os.Remove(out + ".part")
			return code
		}
		if err := os.Rename(out+".part", out); err != nil {
			fmt.Fprintln(os.Stderr, "naut deploy:", err)
			return 1
		}
		fmt.Println(out)
		return 0
	case "restore":
		f, err := os.Open(pos[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "naut deploy:", err)
			return 1
		}
		defer f.Close()
		if code := b.upload(f, "restore.tgz"); code != 0 {
			return code
		}
		return b.root(os.Stdout, "restore")
	case "uninstall":
		if purge {
			return b.root(os.Stdout, "uninstall", "-purge")
		}
		return b.root(os.Stdout, "uninstall")
	default: // rollback, status
		return b.root(os.Stdout, cmd)
	}
}

// deployRelease builds the project (or takes -controller), uploads it with
// the unit, and runs install or update on the box.
func deployRelease(b *sshBox, cmd, dir, ctl, runner, manifest, id string) int {
	name, rev := "", ""
	if ctl == "" {
		rt, err := deployRuntime(b, runner)
		if err != nil {
			fmt.Fprintln(os.Stderr, "naut deploy:", err)
			return 1
		}
		tmp, err := os.MkdirTemp("", "naut-deploy")
		if err != nil {
			fmt.Fprintln(os.Stderr, "naut deploy:", err)
			return 1
		}
		defer os.RemoveAll(tmp)
		ctl = filepath.Join(tmp, "controller")
		args := []string{"-o", ctl}
		if manifest != "" {
			args = append(args, "-m", manifest)
		}
		if rt != "" {
			args = append(args, "-runtime", rt)
		}
		if code := runBuild(append(args, dir)); code != 0 {
			return code
		}
		name = filepath.Base(absOr(dir))
		if p, err := project.Load(os.DirFS(dir), manifest); err == nil && p.Name != "" {
			name = p.Name
		}
		rev = gitDescribe(dir)
	} else {
		name = filepath.Base(ctl)
	}
	sum, err := fileSHA256(ctl)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut deploy:", err)
		return 1
	}
	if id == "" {
		id = releaseID(name, rev, sum)
	} else {
		id = cleanID(id)
	}
	f, err := os.Open(ctl)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut deploy:", err)
		return 1
	}
	defer f.Close()
	fmt.Fprintf(os.Stderr, "naut deploy: %s %s on %s\n", cmd, id, b.target)
	if code := b.upload(f, id); code != 0 {
		return code
	}
	if code := b.upload(bytes.NewReader(deployUnit), "nautilus.service"); code != 0 {
		return code
	}
	return b.root(os.Stdout, cmd, id)
}

// releaseID names a release on the box: readable (the project and its git
// tag or commit) and unique per binary (its hash), so a rebuild of the same
// commit on another naut is a new release, and deploying the same binary
// twice finds it already current.
func releaseID(name, rev, sum string) string {
	id := cleanID(name)
	if rev != "" {
		id += "-" + cleanID(rev)
	}
	return id + "-" + sum[:8]
}

// cleanID keeps an id to characters that need no quoting in a remote shell
// or a path: it is both on the box.
func cleanID(s string) string {
	b := []byte(s)
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '+', c == '-':
		default:
			b[i] = '_'
		}
	}
	return strings.TrimLeft(string(b), "-.")
}

func gitDescribe(dir string) string {
	out, err := exec.Command("git", "-C", dir, "describe", "--tags", "--always", "--dirty").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func absOr(dir string) string {
	if a, err := filepath.Abs(dir); err == nil {
		return a
	}
	return dir
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// deployRuntime picks the naut a controller for the box is built on: the
// -runtime path, a downloaded release (-runtime vX.Y.Z), this naut when it
// already suits the box (""), or this naut's own release for the box.
func deployRuntime(b *sshBox, flagVal string) (string, error) {
	if flagVal != "" && !isReleaseVersion(flagVal) {
		return flagVal, nil
	}
	arch, err := b.arch()
	if err != nil {
		return "", err
	}
	if flagVal != "" {
		return fetchRuntime(flagVal, arch)
	}
	if goruntime.GOOS == "linux" && releaseArch(goruntime.GOARCH) == arch {
		return "", nil
	}
	if !isReleaseVersion(lsp.Version) {
		return "", fmt.Errorf("the box is linux/%s and this naut (version %s) is a development build with no release to download for it; pass -runtime vX.Y.Z or -runtime <a naut for linux/%s>", arch, lsp.Version, arch)
	}
	return fetchRuntime(lsp.Version, arch)
}

// isReleaseVersion: v1.2.3 or 1.2.3, as a tag or `naut version` spells it.
func isReleaseVersion(v string) bool {
	v = strings.TrimPrefix(v, "v")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" || strings.Trim(p, "0123456789") != "" {
			return false
		}
	}
	return true
}

// releaseArch is GoReleaser's name for a Go architecture in an archive name.
func releaseArch(goarch string) string {
	if goarch == "arm" {
		return "armv7"
	}
	return goarch
}

// unameArch maps `uname -m` to the architecture in a release's archive name.
func unameArch(m string) (string, error) {
	switch {
	case m == "aarch64" || m == "arm64":
		return "arm64", nil
	case m == "x86_64" || m == "amd64":
		return "amd64", nil
	case strings.HasPrefix(m, "armv7") || strings.HasPrefix(m, "armv8l"):
		return "armv7", nil
	}
	return "", fmt.Errorf("naut has no release for a %s machine", m)
}

// releasesURL is where the release archives are; NAUT_RELEASES_URL points
// it elsewhere (a mirror, or a test server).
func releasesURL() string {
	if u := os.Getenv("NAUT_RELEASES_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "https://github.com/joyautomation/nautilus/releases/download"
}

// fetchRuntime downloads a release's naut for linux/<arch>, checks the
// archive against the release's checksums.txt, and caches the binary.
func fetchRuntime(version, arch string) (string, error) {
	v := strings.TrimPrefix(version, "v")
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cache, "nautilus", fmt.Sprintf("naut_%s_linux_%s", v, arch))
	bin := filepath.Join(dir, "naut")
	if fi, err := os.Stat(bin); err == nil && fi.Mode().IsRegular() {
		return bin, nil
	}
	name := fmt.Sprintf("nautilus_%s_linux_%s.tar.gz", v, arch)
	base := fmt.Sprintf("%s/v%s/", releasesURL(), v)
	sums, err := httpGet(base + "checksums.txt")
	if err != nil {
		return "", fmt.Errorf("naut v%s for linux/%s: %w", v, arch, err)
	}
	want := ""
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 2 && f[1] == name {
			want = f[0]
		}
	}
	if want == "" {
		return "", fmt.Errorf("naut v%s has no %s in its checksums.txt", v, name)
	}
	archive, err := httpGet(base + name)
	if err != nil {
		return "", fmt.Errorf("naut v%s for linux/%s: %w", v, arch, err)
	}
	if got := sha256.Sum256(archive); hex.EncodeToString(got[:]) != want {
		return "", fmt.Errorf("%s does not match its checksum in checksums.txt", name)
	}
	naut, err := untarFile(archive, "naut")
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(bin+".part", naut, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(bin+".part", bin); err != nil {
		return "", err
	}
	fmt.Fprintf(os.Stderr, "naut deploy: fetched naut v%s for linux/%s\n", v, arch)
	return bin, nil
}

func httpGet(url string) ([]byte, error) {
	c := &http.Client{Timeout: 5 * time.Minute}
	resp, err := c.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

func untarFile(tgz []byte, name string) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("no %s in the archive", name)
		}
		if err != nil {
			return nil, err
		}
		if h.Name == name || h.Name == "./"+name {
			return io.ReadAll(tr)
		}
	}
}

// sshBox runs commands on one box through the system ssh, so ~/.ssh/config,
// keys and the agent work as they do for the person's own ssh. One
// connection serves a whole deploy (ControlMaster), except on Windows,
// whose OpenSSH has none.
type sshBox struct {
	target string
	ctlDir string
	opts   []string
}

func newSSHBox(target string) (*sshBox, error) {
	b := &sshBox{target: target, opts: []string{"-o", "BatchMode=yes"}}
	if goruntime.GOOS != "windows" {
		d, err := os.MkdirTemp("", "naut-ssh")
		if err != nil {
			return nil, err
		}
		b.ctlDir = d
		b.opts = append(b.opts, "-o", "ControlMaster=auto", "-o", "ControlPath="+filepath.Join(d, "cm"), "-o", "ControlPersist=60")
	}
	b.opts = append(b.opts, strings.Fields(os.Getenv("NAUT_SSH_OPTS"))...)
	return b, nil
}

func (b *sshBox) close() {
	if b.ctlDir == "" {
		return
	}
	exec.Command("ssh", "-o", "ControlPath="+filepath.Join(b.ctlDir, "cm"), "-O", "exit", b.target).Run()
	os.RemoveAll(b.ctlDir)
}

// run runs a remote shell command and returns its exit status (ssh's 255
// when the box could not be reached).
func (b *sshBox) run(stdin io.Reader, stdout io.Writer, remote string) int {
	c := exec.Command("ssh", append(append([]string{}, b.opts...), b.target, remote)...)
	c.Stdin, c.Stdout, c.Stderr = stdin, stdout, os.Stderr
	err := c.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		return ee.ExitCode()
	default:
		fmt.Fprintln(os.Stderr, "naut deploy: ssh:", err)
		return 1
	}
}

// asRoot wraps a remote command: as is for root, under `sudo -n` otherwise.
func asRoot(cmd string) string {
	return fmt.Sprintf(`if [ "$(id -u)" = 0 ]; then %s; else sudo -n %s; fi`, cmd, cmd)
}

// root runs box.sh <args> on the box as root. Every arg is a command name,
// a flag or a cleanID id, so none needs quoting.
func (b *sshBox) root(stdout io.Writer, args ...string) int {
	for _, a := range args {
		if a == "" || cleanID(a) != a && a != "-purge" {
			fmt.Fprintf(os.Stderr, "naut deploy: %q cannot go to the box unquoted\n", a)
			return 2
		}
	}
	return b.run(strings.NewReader(deployBoxSh), stdout, asRoot("sh -s -- "+strings.Join(args, " ")))
}

// upload writes r to /opt/nautilus/incoming/<name> on the box, whole or
// not at all (it lands as .part and is renamed).
func (b *sshBox) upload(r io.Reader, name string) int {
	if cleanID(name) != name {
		fmt.Fprintf(os.Stderr, "naut deploy: %q cannot go to the box unquoted\n", name)
		return 2
	}
	in := "/opt/nautilus/incoming/" + name
	return b.run(r, os.Stdout, asRoot("mkdir -p /opt/nautilus/incoming")+" && "+
		asRoot(fmt.Sprintf("sh -c 'cat > %s.part && mv %s.part %s'", in, in, in)))
}

// arch is the box's architecture as a release archive names it. It
// refuses a box that is not Linux.
func (b *sshBox) arch() (string, error) {
	var out bytes.Buffer
	if code := b.run(nil, &out, "uname -sm"); code != 0 {
		return "", fmt.Errorf("could not ask %s for its architecture (ssh exit %d)", b.target, code)
	}
	f := strings.Fields(out.String())
	if len(f) != 2 || f[0] != "Linux" {
		return "", fmt.Errorf("%s runs %q, not Linux", b.target, strings.TrimSpace(out.String()))
	}
	return unameArch(f[1])
}

// parseInterspersed parses flags wherever they sit among the positional
// arguments: `naut deploy update root@ipc -runtime v0.23.0 plant`.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}
