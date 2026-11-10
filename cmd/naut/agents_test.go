package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A project's AGENTS.md is the AI assistant's guidance; naut agents keeps
// it current without ever losing what the project wrote itself (#645).

func TestAgentsRenderWritesTheBlockWhenThereIsNone(t *testing.T) {
	out, did, changed, err := agentsRender("", false, agentsRefuse)
	if err != nil || !changed || did != "wrote" {
		t.Fatalf("got %q changed=%v err=%v", did, changed, err)
	}
	if st, _ := agentsClassify(out); st != agentsCurrent {
		t.Fatalf("a freshly written AGENTS.md classifies as %s", st)
	}
	if !strings.HasPrefix(out, agentsBeginMark) || !strings.HasSuffix(out, agentsEndMark+"\n") {
		t.Fatalf("not wrapped in the markers:\n%s", out)
	}
}

func TestAgentsRenderLeavesACurrentFileAlone(t *testing.T) {
	cur := agentsBlock()
	_, did, changed, err := agentsRender(cur, true, agentsRefuse)
	if err != nil || changed || did != "already current" {
		t.Fatalf("got %q changed=%v err=%v", did, changed, err)
	}
}

func TestAgentsRenderStampsAnUnmarkedCopyOfTodaysText(t *testing.T) {
	out, did, changed, err := agentsRender(agentsText(), true, agentsRefuse)
	if err != nil || !changed || did != "stamped" {
		t.Fatalf("got %q changed=%v err=%v", did, changed, err)
	}
	if out != agentsBlock() {
		t.Fatal("a stamped copy is not exactly the block")
	}
}

// Every text an older naut new wrote, recognised by its hash, is replaced
// by today's: that project was ours all along.
func TestAgentsRenderUpdatesAnOlderNautsUnmarkedCopy(t *testing.T) {
	old := "# AGENTS.md\n\nolder rules\n"
	agentsPrevious[sha(old)] = "test"
	defer delete(agentsPrevious, sha(old))
	if st, _ := agentsClassify(old); st != agentsStale {
		t.Fatalf("an older naut's copy classifies as %s, want stale", st)
	}
	out, did, _, err := agentsRender(old, true, agentsRefuse)
	if err != nil || did != "updated" || out != agentsBlock() {
		t.Fatalf("got %q err=%v", did, err)
	}
}

func TestAgentsRenderRefreshKeepsTheProjectsOwnNotes(t *testing.T) {
	above := "# Plant notes\n\nPump P-101 trips on a cold start.\n\n"
	below := "\n## Ours\n\nUse metric units.\n"
	stale := strings.Replace(agentsBlock(), "# AGENTS.md", "# AGENTS.md (older)", 1)
	doc := above + stale + below
	if st, _ := agentsClassify(doc); st != agentsStale {
		t.Fatalf("an edited block classifies as %s, want stale", st)
	}
	out, did, _, err := agentsRender(doc, true, agentsRefuse)
	if err != nil || did != "updated" {
		t.Fatalf("got %q err=%v", did, err)
	}
	if out != above+agentsBlock()+below {
		t.Fatalf("the notes around the block moved or changed:\n%s", out)
	}
}

func TestAgentsRenderKeepsCRLF(t *testing.T) {
	above := "# Ours\r\n\r\n"
	stale := strings.ReplaceAll(strings.Replace(agentsBlock(), "# AGENTS.md", "# old", 1), "\n", "\r\n")
	out, _, _, err := agentsRender(above+stale, true, agentsRefuse)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "\n") != strings.Count(out, "\r\n") {
		t.Fatal("a CRLF file came back with bare LFs")
	}
	if lf(out) != "# Ours\n\n"+agentsBlock() {
		t.Fatalf("wrong content:\n%s", out)
	}
}

// Somebody's own AGENTS.md is never overwritten unless they say so.
func TestAgentsRenderRefusesSomebodysOwnFileUnlessTold(t *testing.T) {
	own := "# Our rules\n\nNo magic numbers.\n"
	if st, _ := agentsClassify(own); st != agentsUnmanaged {
		t.Fatalf("own file classifies as %s", st)
	}
	if _, _, _, err := agentsRender(own, true, agentsRefuse); err == nil || !strings.Contains(err.Error(), "-append") {
		t.Fatalf("want a refusal that names -append, got %v", err)
	}
	out, _, _, err := agentsRender(own, true, agentsAppend)
	if err != nil || out != own+"\n"+agentsBlock() {
		t.Fatalf("append: err=%v\n%s", err, out)
	}
	out, _, _, err = agentsRender("no newline", true, agentsAppend)
	if err != nil || out != "no newline\n\n"+agentsBlock() {
		t.Fatalf("append to a file with no final newline: err=%v\n%q", err, out)
	}
	out, _, _, err = agentsRender(own, true, agentsForce)
	if err != nil || out != agentsBlock() {
		t.Fatalf("force: err=%v", err)
	}
}

// An older copy that the old guide's "add your conventions to the same
// file" grew: the known text, then the project's notes. The notes are kept
// below the new block; edits inside the text cannot be told apart, so that
// is a conflict, and -append (two sets of rules) is refused.
func TestAgentsOlderCopyWithTheProjectsAdditions(t *testing.T) {
	old := "# AGENTS.md\n\n" + agentsSignature + " (IEC 61131-3) project.\nolder rules\n"
	agentsPrevious[sha(old)] = "test"
	defer delete(agentsPrevious, sha(old))

	grown := old + "\n## Site conventions\n\nTags are ISA-5.1.\n"
	if st, _ := agentsClassify(grown); st != agentsStale {
		t.Fatalf("an older copy with notes after it classifies as %s, want stale", st)
	}
	out, did, _, err := agentsRender(grown, true, agentsRefuse)
	if err != nil || did != "updated" || out != agentsBlock()+"\n## Site conventions\n\nTags are ISA-5.1.\n" {
		t.Fatalf("got %q err=%v:\n%s", did, err, out)
	}

	todayGrown := agentsText() + "\n## Ours\n"
	if st, _ := agentsClassify(todayGrown); st != agentsUnstamped {
		t.Fatalf("today's text with notes after it classifies as %s, want unstamped", st)
	}
	if out, _, _, _ := agentsRender(todayGrown, true, agentsRefuse); out != agentsBlock()+"\n## Ours\n" {
		t.Fatalf("stamping lost the notes:\n%s", out)
	}

	edited := strings.Replace(old, "older rules", "older rules, edited", 1)
	st, why := agentsClassify(edited)
	if st != agentsConflict || why == nil {
		t.Fatalf("an older copy edited inside classifies as %s (%v), want conflict", st, why)
	}
	if _, _, _, err := agentsRender(edited, true, agentsAppend); err == nil {
		t.Fatal("-append would leave two sets of rules in the file")
	}
	if out, _, _, err := agentsRender(edited, true, agentsForce); err != nil || out != agentsBlock() {
		t.Fatalf("-force: %v", err)
	}
}

func TestAgentsBrokenMarkersAreNeverGuessedAt(t *testing.T) {
	b := agentsBlock()
	for name, doc := range map[string]string{
		"no end":         strings.Replace(b, agentsEndMark, "", 1),
		"two begins":     b + b,
		"end before one": agentsEndMark + "\n" + "notes\n",
	} {
		if st, err := agentsClassify(doc); st != agentsConflict || err == nil {
			t.Errorf("%s: classified %s err=%v, want conflict with a reason", name, st, err)
		}
		if _, _, _, err := agentsRender(doc, true, agentsAppend); err == nil {
			t.Errorf("%s: -append went ahead on broken markers", name)
		}
		if out, _, _, err := agentsRender(doc, true, agentsForce); err != nil || out != b {
			t.Errorf("%s: -force did not replace the file: %v", name, err)
		}
	}
}

func TestClaudeRender(t *testing.T) {
	out, did, changed := claudeRender("", false)
	if !changed || did != "wrote" || claudeClassify(out) != claudeImports {
		t.Fatalf("missing: %q %q", did, out)
	}
	out, did, changed = claudeRender("Be terse.", true)
	if !changed || out != "Be terse.\n\n@AGENTS.md\n" {
		t.Fatalf("no import: %q %q", did, out)
	}
	out, _, _ = claudeRender("Be terse.\r\n", true)
	if out != "Be terse.\r\n\r\n@AGENTS.md\r\n" {
		t.Fatalf("CRLF: %q", out)
	}
	if _, _, changed = claudeRender("x\n@./AGENTS.md\n", true); changed {
		t.Fatal("@./AGENTS.md is an import too")
	}
}

// naut check speaks only to a project that carries the guidance.
func TestAgentsWarnings(t *testing.T) {
	cases := []struct {
		name, agents, claude string
		want                 []string
	}{
		{"none", "", "", nil},
		{"own file", "# ours\n", "", nil},
		{"current", agentsBlock(), claudeText(), nil},
		{"current, claude has no import", agentsBlock(), "Be terse.\n", []string{"CLAUDE.md does not import"}},
		{"own file, claude has no import", "# ours\n", "Be terse.\n", nil},
		{"stale", strings.Replace(agentsBlock(), "# AGENTS.md", "# old", 1), claudeText(), []string{"older than this naut"}},
		{"broken", agentsBeginMark + " -->\nx\n", "", []string{"no nautilus:end"}},
	}
	for _, c := range cases {
		dir := t.TempDir()
		if c.agents != "" {
			os.WriteFile(filepath.Join(dir, agentsFileName), []byte(c.agents), 0o644)
		}
		if c.claude != "" {
			os.WriteFile(filepath.Join(dir, claudeFileName), []byte(c.claude), 0o644)
		}
		got := agentsWarnings(dir)
		if len(got) != len(c.want) {
			t.Errorf("%s: got %q, want %d warning(s)", c.name, got, len(c.want))
			continue
		}
		for i, w := range c.want {
			if !strings.Contains(got[i], w) {
				t.Errorf("%s: warning %q does not say %q", c.name, got[i], w)
			}
		}
	}
}

// runAgents end to end: -check's exit code and JSON, the write, the
// refusals.
func TestRunAgents(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte("name: x\n"), 0o644)

	check := func() (int, map[string]string) {
		var code int
		out := captureStdout(t, func() int { code = runAgents([]string{"-check", "-json", dir}); return code })
		var m map[string]string
		if err := json.Unmarshal([]byte(out), &m); err != nil {
			t.Fatalf("-json printed %q: %v", out, err)
		}
		return code, m
	}
	if code, m := check(); code != 1 || m["agentsMd"] != "missing" || m["claudeMd"] != "missing" {
		t.Fatalf("empty project: exit %d %v", code, m)
	}
	if code := runAgents([]string{dir}); code != 0 {
		t.Fatalf("write: exit %d", code)
	}
	if code, m := check(); code != 0 || m["agentsMd"] != "current" || m["claudeMd"] != "current" {
		t.Fatalf("after write: exit %d %v", code, m)
	}

	// Somebody's own AGENTS.md and no CLAUDE.md are choices: -check is 0.
	mine := t.TempDir()
	os.WriteFile(filepath.Join(mine, "nautilus.yaml"), []byte("name: x\n"), 0o644)
	os.WriteFile(filepath.Join(mine, agentsFileName), []byte("# ours\n"), 0o644)
	if code := runAgents([]string{"-check", mine}); code != 0 {
		t.Errorf("own AGENTS.md, no CLAUDE.md: -check exit %d, want 0", code)
	}

	// A file that is there but cannot be read is an error, never "missing".
	odd := t.TempDir()
	os.WriteFile(filepath.Join(odd, "nautilus.yaml"), []byte("name: x\n"), 0o644)
	os.Mkdir(filepath.Join(odd, agentsFileName), 0o755)
	if code := runAgents([]string{"-check", odd}); code != 2 {
		t.Errorf("AGENTS.md a directory: -check exit %d, want 2", code)
	}
	if code := runAgents([]string{odd}); code != 1 {
		t.Errorf("AGENTS.md a directory: write exit %d, want 1", code)
	}

	own := t.TempDir()
	os.WriteFile(filepath.Join(own, "nautilus.yaml"), []byte("name: x\n"), 0o644)
	os.WriteFile(filepath.Join(own, agentsFileName), []byte("# ours\n"), 0o644)
	if code := runAgents([]string{own}); code != 1 {
		t.Fatalf("own file: exit %d, want 1 (refused)", code)
	}
	if b, _ := os.ReadFile(filepath.Join(own, agentsFileName)); string(b) != "# ours\n" {
		t.Fatal("a refused run changed the file")
	}

	for _, args := range [][]string{{"-append", "-force", dir}, {"-json", dir}, {t.TempDir()}} {
		if code := runAgents(args); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}

// When the template changes, the text it replaces must go into
// agentsPrevious, or every project made by the older naut is refused as
// somebody's own file. Checked against the template's git history where
// the checkout has it.
func TestAgentsPreviousCoversHistory(t *testing.T) {
	const path = "cmd/naut/templates/AGENTS.md.tmpl"
	root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skip("no git checkout")
	}
	top := strings.TrimSpace(string(root))
	revs, err := exec.Command("git", "-C", top, "log", "--format=%H", "--", path).Output()
	if err != nil || len(strings.Fields(string(revs))) < 2 {
		t.Skip("the template's history is not in this checkout (shallow clone)")
	}
	current := agentsText()
	for _, rev := range strings.Fields(string(revs)) {
		body, err := exec.Command("git", "-C", top, "show", rev+":"+path).Output()
		if err != nil {
			t.Fatal(err)
		}
		text := strings.TrimRight(lf(string(body)), "\n") + "\n"
		if text == current {
			continue
		}
		if _, ok := agentsPrevious[sha(text)]; !ok {
			t.Errorf("%s at %.8s is not in agentsPrevious: add %q", path, rev, sha(text))
		}
	}
}
