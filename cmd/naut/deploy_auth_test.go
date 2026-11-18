package main

import (
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"testing"
)

// ssh may ask for a password only when something can answer: an askpass
// program or a person at a terminal. Otherwise BatchMode=yes, so CD fails
// fast instead of hanging at a prompt.
func TestDeploySSHAuth(t *testing.T) {
	for _, c := range []struct {
		name                   string
		naut, askpass, require string
		tty                    bool
		batch                  bool
		env                    []string
	}{
		{name: "keys only by default", batch: true},
		{name: "NAUT_SSH_ASKPASS", naut: "/x/askpass", batch: false,
			env: []string{"SSH_ASKPASS=/x/askpass", "SSH_ASKPASS_REQUIRE=force"}},
		{name: "SSH_ASKPASS forced", askpass: "/x/a", require: "force", batch: false},
		{name: "SSH_ASKPASS preferred", askpass: "/x/a", require: "prefer", batch: false},
		{name: "SSH_ASKPASS alone does not count", askpass: "/x/a", batch: true},
		{name: "a person at a terminal", tty: true, batch: false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("NAUT_SSH_ASKPASS", c.naut)
			t.Setenv("SSH_ASKPASS", c.askpass)
			t.Setenv("SSH_ASKPASS_REQUIRE", c.require)
			tty := c.tty
			old := stdinIsTerminal
			stdinIsTerminal = func() bool { return tty }
			t.Cleanup(func() { stdinIsTerminal = old })
			opts, env := sshAuth()
			if got := slices.Contains(opts, "BatchMode=yes"); got != c.batch {
				t.Errorf("BatchMode=yes: %v, want %v (opts %q)", got, c.batch, opts)
			}
			if !slices.Equal(env, c.env) {
				t.Errorf("env %q, want %q", env, c.env)
			}
		})
	}
}

// Through to ssh itself: with NAUT_SSH_ASKPASS set, ssh runs without
// BatchMode and with SSH_ASKPASS and SSH_ASKPASS_REQUIRE=force in its
// environment.
func TestDeployAskpassReachesSSH(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("the fake ssh is a shell script")
	}
	bin, log := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(bin, "ssh"), []byte(`#!/bin/sh
case " $* " in *" -O exit "*) exit 0 ;; esac
printf '%s\n' "$*" >>"$LOG/args"
printf '%s %s\n' "$SSH_ASKPASS" "$SSH_ASKPASS_REQUIRE" >>"$LOG/env"
`), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LOG", log)
	t.Setenv("NAUT_SSH_OPTS", "")
	t.Setenv("NAUT_SSH_ASKPASS", "/usr/local/bin/askpass-bw.sh")
	old := stdinIsTerminal
	stdinIsTerminal = func() bool { return false }
	t.Cleanup(func() { stdinIsTerminal = old })

	if code := runDeploy([]string{"status", "admin@plc"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	args, _ := os.ReadFile(filepath.Join(log, "args"))
	env, _ := os.ReadFile(filepath.Join(log, "env"))
	if strings.Contains(string(args), "BatchMode") {
		t.Errorf("ssh must not run in BatchMode when a password can come from askpass:\n%s", args)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(env)), "\n") {
		if line != "/usr/local/bin/askpass-bw.sh force" {
			t.Errorf("every ssh call needs SSH_ASKPASS and SSH_ASKPASS_REQUIRE=force, got %q", line)
		}
	}
}

// tools/deploy/askpass-bw.sh answers a password prompt from Bitwarden,
// without ever prompting, and refuses every other prompt: answering an
// unknown host key's yes/no with the password would send it to a host
// nobody checked.
func TestDeployAskpassBitwarden(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("a shell script")
	}
	script, err := filepath.Abs("../../tools/deploy/askpass-bw.sh")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "bw"), []byte(`#!/bin/sh
echo "args=$* session=$BW_SESSION" >>"$BW_LOG"
echo s3cret
`), 0o755)
	session := filepath.Join(t.TempDir(), "bw-session")
	os.WriteFile(session, []byte("KEY123"), 0o600)
	bwLog := filepath.Join(t.TempDir(), "bw.log")
	run := func(prompt string, env ...string) (string, error) {
		c := exec.Command(script, prompt)
		c.Env = append([]string{"PATH=" + bin + ":/usr/bin:/bin", "BW_LOG=" + bwLog,
			"NAUT_BW_ITEM=plc-item", "NAUT_BW_SESSION_FILE=" + session}, env...)
		out, err := c.Output()
		return string(out), err
	}

	out, err := run("admin@plc's password: ")
	if err != nil || out != "s3cret\n" {
		t.Fatalf("a password prompt gets the password: %q, %v", out, err)
	}
	logged, _ := os.ReadFile(bwLog)
	if string(logged) != "args=--nointeraction get password plc-item session=KEY123\n" {
		t.Errorf("bw call: %q", logged)
	}

	for _, c := range []struct {
		prompt string
		env    []string
	}{
		{"The authenticity of host 'plc' can't be established.\nAre you sure you want to continue connecting (yes/no/[fingerprint])? ", nil},
		{"admin@plc's password: ", []string{"SSH_ASKPASS_PROMPT=confirm"}},
		{"Enter passphrase for key '/home/x/.ssh/id': ", nil},
	} {
		os.Remove(bwLog)
		out, err := run(c.prompt, c.env...)
		if err == nil || out != "" {
			t.Errorf("prompt %q (env %q) must be refused, got %q, %v", c.prompt, c.env, out, err)
		}
		if _, err := os.Stat(bwLog); err == nil {
			t.Errorf("prompt %q must not reach bw", c.prompt)
		}
	}
}
