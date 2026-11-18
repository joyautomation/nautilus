package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The helper mode of naut deploy (#771): for a box whose SSH user may not
// have unrestricted sudo, box.sh is installed once, by someone with root,
// as a root-owned file the SSH user may run as root and nothing else
// (one sudoers line, the way a vendor's own drop-ins grant one command).
// naut deploy finds it at helperPath and calls `sudo -n <helper> <verb>`
// instead of piping box.sh to `sudo -n sh -s`. The helper is the root
// boundary: its prologue pins every setting box.sh would take from the
// environment, box.sh checks the helper's arguments itself, the unit comes
// only from the root-owned template at helperUnitPath, and nothing an
// uploaded controller can touch steers root (box.sh's stateread and
// statewrite).

const (
	helperPath     = "/usr/local/sbin/naut-deploy-box"
	helperUnitPath = "/usr/local/share/nautilus/nautilus.service"
)

// helperPaths are the settings a helper pins. Tests point them at temp
// directories; the installed helper uses defaultHelperPaths.
type helperPaths struct {
	root, state, unitDir, unitSrc, path string
}

var defaultHelperPaths = helperPaths{
	root:    deployRoot,
	state:   "/var/lib/nautilus",
	unitDir: "/etc/systemd/system",
	unitSrc: helperUnitPath,
	path:    "/usr/sbin:/usr/bin:/sbin:/bin",
}

// helperVersion names the box.sh and unit a helper was made from. naut
// deploy refuses a helper whose version is not its own: the helper is
// frozen on the box while naut moves on, so a change to either means
// installing the helper again.
func helperVersion() string {
	h := sha256.New()
	h.Write([]byte(deployBoxSh))
	h.Write([]byte{0})
	h.Write(deployUnit)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// helperScript is the helper: a prologue that pins every setting, then
// box.sh unchanged. The paths are written unquoted, so they are held to
// plain characters.
func helperScript(p helperPaths) (string, error) {
	for _, v := range []string{p.root, p.state, p.unitDir, p.unitSrc, p.path} {
		if v == "" || strings.ContainsFunc(v, func(c rune) bool {
			return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("/._+-:", c))
		}) {
			return "", fmt.Errorf("helper path %q: letters, digits and / . _ + - : only", v)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, `#!/bin/sh
# naut-deploy-box: naut deploy's on-box half (cmd/naut/deploybox/box.sh),
# for a box whose SSH user may run only this file as root. Written by
# "naut deploy helper"; install it root-owned, mode 0755, with the unit
# template beside it, and again whenever naut deploy says the versions
# differ. Every setting box.sh reads from the environment is pinned here:
# nothing the caller passes reaches root.
NAUT_HELPER=1
NAUT_HELPER_VERSION=%s
NAUT_SUPERVISOR=systemd
NAUT_ROOT=%s
NAUT_STATE=%s
NAUT_UNITDIR=%s
NAUT_UNITSRC=%s
PATH=%s
LC_ALL=C
export PATH LC_ALL
unset TMPDIR
umask 022
`, helperVersion(), p.root, p.state, p.unitDir, p.unitSrc, p.path)
	b.WriteString(deployBoxSh)
	return b.String(), nil
}

const deployHelperUsage = `usage: naut deploy helper -user <ssh-user> <dir>

Writes what a box needs so that its SSH user can run naut deploy without
unrestricted sudo, for someone with root on the box to read and install:

  <dir>/naut-deploy-box       install root-owned, 0755, as ` + helperPath + `
  <dir>/nautilus.service      install root-owned, 0644, as ` + helperUnitPath + `
  <dir>/nautilus-deploy       the sudoers drop-in: <ssh-user> may run the helper as root,
                              nothing else (check with visudo -cf, install 0440 in /etc/sudoers.d)

naut deploy then finds the helper by itself. Install the helper again when
naut deploy reports that its version differs (any change to the on-box half
or the unit).
`

// runDeployHelper writes the helper, the unit template and the sudoers
// drop-in into a directory.
func runDeployHelper(args []string) int {
	fs := flag.NewFlagSet("deploy helper", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, deployHelperUsage) }
	user := fs.String("user", "", "the SSH user the sudoers line names")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 1 || *user == "" {
		fmt.Fprint(os.Stderr, deployHelperUsage)
		return 2
	}
	if cleanID(*user) != *user || strings.HasPrefix(*user, "-") || strings.HasPrefix(*user, ".") {
		fmt.Fprintf(os.Stderr, "naut deploy helper: -user %q: a plain user name\n", *user)
		return 2
	}
	script, err := helperScript(defaultHelperPaths)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut deploy helper:", err)
		return 1
	}
	dir := pos[0]
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "naut deploy helper:", err)
		return 1
	}
	files := []struct {
		name string
		body []byte
		mode os.FileMode
	}{
		{"naut-deploy-box", []byte(script), 0o755},
		{"nautilus.service", deployUnit, 0o644},
		{"nautilus-deploy", []byte(sudoersLine(*user)), 0o440},
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f.name), f.body, f.mode); err != nil {
			fmt.Fprintln(os.Stderr, "naut deploy helper:", err)
			return 1
		}
	}
	fmt.Fprintf(os.Stderr, "naut deploy helper: wrote %s (helper version %s)\n", dir, helperVersion())
	return 0
}

// sudoersLine is the drop-in: the user may run the helper as root, with
// any of its verbs, and nothing else.
func sudoersLine(user string) string {
	return "# naut deploy (helper mode): " + user + " may run the release helper as root, and nothing else.\n" +
		user + " ALL=(root) NOPASSWD: " + helperPath + "\n"
}
