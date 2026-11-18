package main

import (
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/joyautomation/nautilus/internal/lsp"
)

// naut new --deploy-ssh (#721): a CD workflow that runs naut deploy update
// on a box over SSH. The workflow's shell steps are run here, not only
// rendered: the install step against a stand-in release server, the deploy
// step against a stand-in naut.

func scaffoldDeploySSH(t *testing.T, sc scaffold) string {
	t.Helper()
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)
	if err := write(&sc); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, sc.Name)
}

type ghWorkflow struct {
	Jobs map[string]struct {
		Env   map[string]string `yaml:"env"`
		Steps []struct {
			Name string            `yaml:"name"`
			Run  string            `yaml:"run"`
			Env  map[string]string `yaml:"env"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

// step returns the run script of the deploy job's step named name.
func (w ghWorkflow) step(t *testing.T, name string) string {
	t.Helper()
	for _, s := range w.Jobs["deploy"].Steps {
		if s.Name == name {
			return s.Run
		}
	}
	t.Fatalf("no step %q in the deploy job", name)
	return ""
}

func readWorkflow(t *testing.T, proj string) (string, ghWorkflow) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(proj, ".github", "workflows", "deploy-ssh.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var w ghWorkflow
	if err := yaml.Unmarshal(raw, &w); err != nil {
		t.Fatalf("deploy-ssh.yml is not YAML: %v", err)
	}
	return string(raw), w
}

// The workflow renders clean, pins the naut that scaffolded it, takes the
// box from a repository variable and the key from secrets, and the README
// says how to set it up. Without the feature, or on a Go template, there
// is no such workflow.
func TestDeploySSHWorkflowRenders(t *testing.T) {
	proj := scaffoldDeploySSH(t, scaffold{Name: "ssh-proj", Program: "SshProj", Template: tmplMinimal, DeploySSH: true, NautVersion: "v9.8.7"})
	text, w := readWorkflow(t, proj)
	for _, leftover := range []string{"{{.", "{{if", "{{end", `{{"`} {
		if strings.Contains(text, leftover) {
			t.Errorf("deploy-ssh.yml still contains template syntax %q", leftover)
		}
	}
	env := w.Jobs["deploy"].Env
	if env["NAUT_VERSION"] != "v9.8.7" {
		t.Errorf("NAUT_VERSION = %q, want the scaffolding naut's v9.8.7", env["NAUT_VERSION"])
	}
	if env["TARGET"] != "${{ vars.DEPLOY_TARGET }}" {
		t.Errorf("TARGET = %q, want the repository variable", env["TARGET"])
	}
	if !strings.Contains(w.step(t, "deploy"), `naut deploy update "$TARGET"`) {
		t.Error("the deploy step does not run naut deploy update")
	}
	for _, want := range []string{"${{ secrets.DEPLOY_SSH_KEY }}", "${{ secrets.DEPLOY_KNOWN_HOSTS }}", "fetch-depth: 0", "tags: [\"v*\"]"} {
		if !strings.Contains(text, want) {
			t.Errorf("deploy-ssh.yml is missing %q", want)
		}
	}
	readme, _ := os.ReadFile(filepath.Join(proj, "README.md"))
	if !strings.Contains(string(readme), "## Deploy to a box over SSH") || !strings.Contains(string(readme), "naut v9.8.7") {
		t.Error("the README should explain the SSH deploy and name the pinned naut")
	}

	// A development naut pins nothing: the placeholder fails the job (see
	// TestDeploySSHInstallStep), it never deploys on an unknown naut.
	saved := lsp.Version
	lsp.Version = "dev"
	dev := scaffoldDeploySSH(t, scaffold{Name: "dev-proj", Program: "DevProj", Template: tmplMinimal, DeploySSH: true})
	lsp.Version = saved
	if _, w := readWorkflow(t, dev); !strings.HasPrefix(w.Jobs["deploy"].Env["NAUT_VERSION"], "vX.Y.Z") {
		t.Errorf("a dev naut should leave the vX.Y.Z placeholder, got %q", w.Jobs["deploy"].Env["NAUT_VERSION"])
	}

	for _, sc := range []scaffold{
		{Name: "no-ssh", Program: "NoSsh", Template: tmplMinimal},
		{Name: "go-ssh", Module: "example.com/go-ssh", Program: "GoSsh", Template: tmplSDK, DeploySSH: true},
	} {
		p := scaffoldDeploySSH(t, sc)
		if _, err := os.Stat(filepath.Join(p, ".github", "workflows", "deploy-ssh.yml")); err == nil {
			t.Errorf("%s (%s, DeploySSH %v) should have no deploy-ssh.yml", sc.Name, sc.Template, sc.DeploySSH)
		}
	}
}

func TestNewDeploySSHFlag(t *testing.T) {
	sc, _, code := parseNew([]string{"plant", "--template", "minimal", "--deploy-ssh"}, false)
	if code != 0 || !sc.DeploySSH {
		t.Fatalf("--deploy-ssh: code %d, DeploySSH %v", code, sc.DeploySSH)
	}
	if sc, _, _ := parseNew([]string{"plant", "--template", "minimal"}, false); sc.DeploySSH {
		t.Error("DeploySSH should be off by default")
	}
}

func needTools(t *testing.T, tools ...string) {
	t.Helper()
	if goruntime.GOOS == "windows" {
		t.Skip("the workflow's steps are bash on a Linux runner")
	}
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("needs %s", tool)
		}
	}
}

// runStep runs a workflow step's script the way Actions does (bash -e) in
// dir, with env added.
func runStep(t *testing.T, script, dir string, env ...string) (string, error) {
	t.Helper()
	c := exec.Command("bash", "-e", "-c", script)
	c.Dir = dir
	c.Env = append(os.Environ(), env...)
	out, err := c.CombinedOutput()
	return string(out), err
}

// The install step downloads the pinned release for the runner's
// architecture, checks it against checksums.txt, and unpacks naut into
// RUNNER_TEMP, never into the checkout (naut build would embed it in the
// controller). A tampered archive, an unpinned version or no target fails
// the job before anything is unpacked.
func TestDeploySSHInstallStep(t *testing.T) {
	needTools(t, "bash", "curl", "sha256sum", "tar", "grep")
	proj := scaffoldDeploySSH(t, scaffold{Name: "inst-proj", Program: "InstProj", Template: tmplMinimal, DeploySSH: true, NautVersion: "v9.8.7"})
	_, w := readWorkflow(t, proj)
	script := w.step(t, "install naut")
	arch := releaseArch(goruntime.GOARCH)

	run := func(t *testing.T, corrupt bool, env ...string) (string, error, string, string) {
		fakeReleases(t, "9.8.7", arch, []byte("NAUT-9.8.7"), corrupt)
		tmp, ghPath := t.TempDir(), filepath.Join(t.TempDir(), "path")
		out, err := runStep(t, script, proj, append([]string{
			"NAUT_RELEASES=" + os.Getenv("NAUT_RELEASES_URL"), "RUNNER_TEMP=" + tmp, "GITHUB_PATH=" + ghPath,
			"NAUT_VERSION=v9.8.7", "TARGET=deploy@box",
		}, env...)...)
		return out, err, tmp, ghPath
	}

	t.Run("installs into RUNNER_TEMP", func(t *testing.T) {
		before, _ := os.ReadDir(proj)
		out, err, tmp, ghPath := run(t, false)
		if err != nil {
			t.Fatalf("install step failed: %v\n%s", err, out)
		}
		if b, _ := os.ReadFile(filepath.Join(tmp, "naut")); string(b) != "NAUT-9.8.7" {
			t.Errorf("naut in RUNNER_TEMP = %q, want the release's", b)
		}
		if p, _ := os.ReadFile(ghPath); strings.TrimSpace(string(p)) != tmp {
			t.Errorf("GITHUB_PATH = %q, want RUNNER_TEMP", p)
		}
		after, _ := os.ReadDir(proj)
		if len(after) != len(before) {
			t.Errorf("the install step changed the checkout: %d entries, then %d", len(before), len(after))
		}
	})
	t.Run("refuses a tampered archive", func(t *testing.T) {
		out, err, tmp, _ := run(t, true)
		if err == nil {
			t.Fatalf("a tampered archive passed:\n%s", out)
		}
		if _, err := os.Stat(filepath.Join(tmp, "naut")); err == nil {
			t.Error("naut was unpacked from a tampered archive")
		}
	})
	t.Run("refuses an unpinned naut", func(t *testing.T) {
		out, err, _, _ := run(t, false, "NAUT_VERSION=vX.Y.Z")
		if err == nil || !strings.Contains(out, "set NAUT_VERSION") {
			t.Errorf("an unpinned naut should fail and say so: %v\n%s", err, out)
		}
	})
	t.Run("refuses no target", func(t *testing.T) {
		out, err, _, _ := run(t, false, "TARGET=")
		if err == nil || !strings.Contains(out, "DEPLOY_TARGET") {
			t.Errorf("an empty DEPLOY_TARGET should fail and say so: %v\n%s", err, out)
		}
	})
}

// The deploy step hands naut deploy the key and known hosts through
// NAUT_SSH_OPTS, with the key readable by the runner's user only while it
// runs and gone afterwards, and fails the job when naut deploy fails (an
// update the box rolled back exits 3).
func TestDeploySSHDeployStep(t *testing.T) {
	needTools(t, "bash")
	proj := scaffoldDeploySSH(t, scaffold{Name: "dep-proj", Program: "DepProj", Template: tmplMinimal, DeploySSH: true, NautVersion: "v9.8.7"})
	_, w := readWorkflow(t, proj)
	script := w.step(t, "deploy")

	for _, exit := range []int{0, 3} {
		tmp, bin := t.TempDir(), t.TempDir()
		// A stand-in naut: records its arguments, NAUT_SSH_OPTS, and the
		// key's mode and contents as it sees them, then exits as told.
		fake := `#!/bin/sh
{ echo "args: $*"; echo "opts: $NAUT_SSH_OPTS"; echo "mode: $(ls -l "$RUNNER_TEMP/key" | cut -c1-10)"; echo "key: $(cat "$RUNNER_TEMP/key")"; echo "hosts: $(cat "$RUNNER_TEMP/known_hosts")"; } >"$RUNNER_TEMP/seen"
exit ` + map[int]string{0: "0", 3: "3"}[exit] + "\n"
		os.WriteFile(filepath.Join(bin, "naut"), []byte(fake), 0o755)
		out, err := runStep(t, script, proj,
			"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "RUNNER_TEMP="+tmp,
			"TARGET=deploy@box", "SSH_KEY=PRIVATE-KEY", "KNOWN_HOSTS=box ssh-ed25519 AAAA")
		if (err == nil) != (exit == 0) {
			t.Errorf("naut deploy exiting %d: the step's error is %v\n%s", exit, err, out)
		}
		seen, _ := os.ReadFile(filepath.Join(tmp, "seen"))
		for _, want := range []string{
			"args: deploy update deploy@box",
			"opts: -i " + tmp + "/key -o UserKnownHostsFile=" + tmp + "/known_hosts",
			"mode: -rw-------", "key: PRIVATE-KEY", "hosts: box ssh-ed25519 AAAA",
		} {
			if !strings.Contains(string(seen), want) {
				t.Errorf("exit %d: naut saw no %q:\n%s", exit, want, seen)
			}
		}
		if _, err := os.Stat(filepath.Join(tmp, "key")); err == nil {
			t.Errorf("exit %d: the key is still on the runner after the step", exit)
		}
	}
}

// The pin is the scaffolding naut's own release version, so the project
// builds on the naut that made it until someone changes it in a commit.
func TestDeploySSHPinsTheScaffoldingNaut(t *testing.T) {
	saved := lsp.Version
	defer func() { lsp.Version = saved }()
	lsp.Version = "9.8.7"
	proj := scaffoldDeploySSH(t, scaffold{Name: "pin-proj", Program: "PinProj", Template: tmplMinimal, DeploySSH: true})
	if _, w := readWorkflow(t, proj); w.Jobs["deploy"].Env["NAUT_VERSION"] != "v9.8.7" {
		t.Errorf("NAUT_VERSION = %q, want v9.8.7 from the scaffolding naut", w.Jobs["deploy"].Env["NAUT_VERSION"])
	}
	lsp.Version = "dev"
	proj = scaffoldDeploySSH(t, scaffold{Name: "pin-dev", Program: "PinDev", Template: tmplMinimal, DeploySSH: true})
	if _, w := readWorkflow(t, proj); !strings.HasPrefix(w.Jobs["deploy"].Env["NAUT_VERSION"], "vX.Y.Z") {
		t.Errorf("a dev naut should leave the placeholder, got %q", w.Jobs["deploy"].Env["NAUT_VERSION"])
	}
}
