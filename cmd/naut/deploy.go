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
	"golang.org/x/term"
)

// naut deploy puts a controller on a Linux box with systemd over SSH (#721),
// or, with -supervisor cron, on one without systemd or root (#768: a
// PLCnext). The half that runs on the box is deploybox/box.sh, fed to
// `sh -s` as root (as the SSH user in cron mode); this file builds, uploads
// and calls it. Nothing is installed on the box beyond the controller and
// its unit (its keepalive and crontab lines in cron mode).

//go:embed deploybox/box.sh
var deployBoxSh string

// deployRoot is where releases go on the box: box.sh's default ROOT, and
// the one place deploy.go names it. asRoot is the one place privilege is
// added.
const deployRoot = "/opt/nautilus"

//go:embed deploybox/nautilus.service
var deployUnit []byte

const deployUsage = `usage: naut deploy <command> <ssh-target> ...

Puts a controller on a Linux box with systemd, over SSH, and keeps the
release before it so that one command goes back. A box without systemd or
root (a PLCnext) takes -supervisor cron at install.

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

install only:
  -supervisor cron        no systemd and no root: everything runs as the SSH user, under -root,
                          started by <root>/keepalive.sh from that user's crontab (at boot and
                          every minute); through <root>/naut-launch when it is there, for
                          priority: without root (rt/plcnext-io). The box remembers the mode
                          and root in ~/.config/naut-deploy, so later commands need neither.
                          "Started" means still running 3 s later: a first scan that hangs is
                          not caught, as under Type=simple.
  -root <dir>             with -supervisor cron: where releases, current, previous and state/
                          (retain.json, nautilus.log) go; the SSH user must be able to write it

<ssh-target> is anything ssh takes: user@host, or a Host from ~/.ssh/config.
The user must be root or have sudo without a password (any user that can write
-root in cron mode). NAUT_SSH_OPTS adds ssh options, e.g. "-p 2222 -i ~/.ssh/ipc".
ssh signs in with keys only (BatchMode), unless a password can come from
somewhere: NAUT_SSH_ASKPASS=<program> prints it (tools/deploy/askpass-bw.sh
reads it from Bitwarden), as does SSH_ASKPASS with SSH_ASKPASS_REQUIRE=force,
or a person at a terminal types it, once per command.

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
		supervisor, root               string
		purge                          bool
	)
	nargs := [2]int{1, 1} // positional arguments, min and max
	switch cmd {
	case "install", "update":
		fs.StringVar(&runner, "runtime", "", "the naut the controller is built on: a path, or vX.Y.Z to download")
		fs.StringVar(&ctl, "controller", "", "deploy this controller instead of building one")
		fs.StringVar(&manifest, "m", "", manifestFlagUsage)
		fs.StringVar(&id, "id", "", "the release's name on the box")
		if cmd == "install" {
			fs.StringVar(&supervisor, "supervisor", "", "cron: a box without systemd or root (see usage)")
			fs.StringVar(&root, "root", "", "with -supervisor cron: the directory releases and state go in")
		}
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
	if err := checkSupervisor(supervisor, root); err != nil {
		fmt.Fprintln(os.Stderr, "naut deploy:", err)
		return 2
	}
	b, err := newSSHBox(pos[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut deploy:", err)
		return 1
	}
	defer b.close()
	if supervisor != "" {
		// An explicit mode must agree with the one the box remembers:
		// a systemd install beside a cron one would run two controllers.
		if err := b.discover(); err != nil {
			fmt.Fprintln(os.Stderr, "naut deploy:", err)
			return 1
		}
		if b.cron && (supervisor != "cron" || b.cronRoot != filepath.Clean(root)) {
			fmt.Fprintf(os.Stderr, "naut deploy: %s already runs nautilus in cron mode under %s (~/.config/naut-deploy); uninstall that first\n", b.target, b.cronRoot)
			return 2
		}
		if supervisor == "cron" {
			b.cron, b.cronRoot = true, filepath.Clean(root)
		}
	}

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
	if err := checkReleaseID(id); err != nil {
		fmt.Fprintln(os.Stderr, "naut deploy:", err)
		return 2
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
	if !b.cron { // set by now: the controller's upload discovered the mode
		if code := b.upload(bytes.NewReader(deployUnit), "nautilus.service"); code != 0 {
			return code
		}
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

// checkReleaseID refuses an id that is empty (-id "..." cleans to nothing)
// or that would collide with the other files deploy puts in incoming/.
func checkReleaseID(id string) error {
	switch {
	case id == "":
		return errors.New("-id: empty once cleaned to letters, digits and . _ + -")
	case id == "nautilus.service", id == "restore.tgz", strings.HasSuffix(id, ".part"):
		return fmt.Errorf("-id %s: that name is reserved", id)
	}
	return nil
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
	env    []string // added to ssh's environment (SSH_ASKPASS)
	// cron mode (#768): no systemd and no root; box.sh runs as the SSH user
	// with releases and state under cronRoot.
	cron     bool
	cronRoot string
	known    bool // the mode is known: from install's flags, or discovered
}

// checkSupervisor vets install's -supervisor and -root. The root goes into
// remote shell commands and a crontab unquoted, so it is held to an
// absolute path of plain characters.
func checkSupervisor(supervisor, root string) error {
	switch supervisor {
	case "", "systemd":
		if root != "" {
			return errors.New("-root goes with -supervisor cron (systemd boxes use " + deployRoot + ")")
		}
		return nil
	case "cron":
	default:
		return fmt.Errorf("-supervisor %q: the choices are systemd (the default) and cron", supervisor)
	}
	if root == "" {
		return errors.New("-supervisor cron needs -root <dir>: where releases and retained values go")
	}
	return checkCronRoot(root)
}

func checkCronRoot(root string) error {
	clean := filepath.Clean(root)
	if !strings.HasPrefix(clean, "/") || clean == "/" {
		return fmt.Errorf("-root %q: an absolute directory, not /", root)
	}
	for _, c := range clean {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("/._+-", c)) {
			return fmt.Errorf("-root %q: letters, digits and / . _ + - only", root)
		}
	}
	for _, part := range strings.Split(root, "/") {
		if part == ".." {
			return fmt.Errorf("-root %q: no ..", root)
		}
	}
	return nil
}

// discover reads the mode an earlier `install -supervisor cron` left on the
// box, once, just before the first upload or box.sh call (so everything
// refused before that never reaches the box); a box without it is a
// systemd box.
func (b *sshBox) discover() error {
	if b.known {
		return nil
	}
	b.known = true
	var out bytes.Buffer
	if code := b.run(nil, &out, `cat "$HOME/.config/naut-deploy" 2>/dev/null || true`); code != 0 {
		return fmt.Errorf("could not reach %s (ssh exit %d)", b.target, code)
	}
	return b.parseMode(out.String())
}

func (b *sshBox) parseMode(text string) error {
	mode, root := "", ""
	for _, line := range strings.Split(text, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "supervisor":
			mode = v
		case "root":
			root = v
		}
	}
	switch mode {
	case "":
		return nil
	case "cron":
		if err := checkCronRoot(root); err != nil {
			return fmt.Errorf("%s: ~/.config/naut-deploy: %v", b.target, err)
		}
		b.cron, b.cronRoot = true, filepath.Clean(root)
		return nil
	}
	return fmt.Errorf("%s: ~/.config/naut-deploy: unknown supervisor %q", b.target, mode)
}

// boxRoot is where releases go on this box.
func (b *sshBox) boxRoot() string {
	if b.cron {
		return b.cronRoot
	}
	return deployRoot
}

// priv wraps a remote command with the privilege the box's mode needs:
// root for systemd (asRoot), none in cron mode.
func (b *sshBox) priv(cmd string) string {
	if b.cron {
		return cmd
	}
	return asRoot(cmd)
}

// stdinIsTerminal is a variable so tests can stand in for a person.
var stdinIsTerminal = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// sshAuth decides how ssh may authenticate. Keys alone (BatchMode=yes) is
// the default, so CD never hangs at a prompt. A password is allowed when a
// program can supply it: NAUT_SSH_ASKPASS names one (e.g.
// tools/deploy/askpass-bw.sh, from Bitwarden), or the standard SSH_ASKPASS
// with SSH_ASKPASS_REQUIRE=force|prefer is set; or when a person is at a
// terminal, where ssh asks on /dev/tty. With ControlMaster it is asked once
// per command. Some controllers need it: a PLCnext's sshd refuses keys for
// admin, whose home is group writable (#768).
//
// When a password may be asked, ssh asks once per connection
// (NumberOfPasswordPrompts=1): a wrong answer is one failed login, not
// three, on a controller that locks an account out.
func sshAuth() (opts, env []string) {
	password := []string{"-o", "NumberOfPasswordPrompts=1"}
	if p := os.Getenv("NAUT_SSH_ASKPASS"); p != "" {
		return password, []string{"SSH_ASKPASS=" + p, "SSH_ASKPASS_REQUIRE=force"}
	}
	if os.Getenv("SSH_ASKPASS") != "" {
		switch os.Getenv("SSH_ASKPASS_REQUIRE") {
		case "force", "prefer":
			return password, nil
		}
	}
	if stdinIsTerminal() {
		return password, nil
	}
	return []string{"-o", "BatchMode=yes"}, nil
}

func newSSHBox(target string) (*sshBox, error) {
	opts, env := sshAuth()
	// A box that is off the network fails in 10 s, not the TCP default.
	opts = append(opts, "-o", "ConnectTimeout=10")
	b := &sshBox{target: target, opts: opts, env: env}
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
	if len(b.env) > 0 {
		c.Env = append(os.Environ(), b.env...)
	}
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

// root runs box.sh <args> on the box as root (as the SSH user, with the
// mode and root in its environment, in cron mode). Every arg is a command
// name, a flag or a cleanID id, so none needs quoting.
func (b *sshBox) root(stdout io.Writer, args ...string) int {
	for _, a := range args {
		if a == "" || cleanID(a) != a && a != "-purge" {
			fmt.Fprintf(os.Stderr, "naut deploy: %q cannot go to the box unquoted\n", a)
			return 2
		}
	}
	if err := b.discover(); err != nil {
		fmt.Fprintln(os.Stderr, "naut deploy:", err)
		return 1
	}
	sh := "sh -s -- " + strings.Join(args, " ")
	if b.cron {
		sh = "NAUT_SUPERVISOR=cron NAUT_ROOT=" + b.cronRoot + " " + sh
	}
	return b.run(strings.NewReader(deployBoxSh), stdout, b.priv(sh))
}

// upload writes r to <root>/incoming/<name> on the box, whole or not at
// all (it lands as .part and is renamed).
func (b *sshBox) upload(r io.Reader, name string) int {
	if cleanID(name) != name {
		fmt.Fprintf(os.Stderr, "naut deploy: %q cannot go to the box unquoted\n", name)
		return 2
	}
	if err := b.discover(); err != nil {
		fmt.Fprintln(os.Stderr, "naut deploy:", err)
		return 1
	}
	in := b.boxRoot() + "/incoming/" + name
	return b.run(r, os.Stdout, b.priv("mkdir -p "+b.boxRoot()+"/incoming")+" && "+
		b.priv(fmt.Sprintf("sh -c 'cat > %s.part && mv %s.part %s'", in, in, in)))
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
