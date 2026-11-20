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
	out, did, changed, err := agentsRender(agentsAll, "", false, agentsRefuse)
	if err != nil || !changed || did != "wrote" {
		t.Fatalf("got %q changed=%v err=%v", did, changed, err)
	}
	if st, _ := agentsClassify(out, agentsAll); st != agentsCurrent {
		t.Fatalf("a freshly written AGENTS.md classifies as %s", st)
	}
	if !strings.HasPrefix(out, agentsBeginMark) || !strings.HasSuffix(out, agentsEndMark+"\n") {
		t.Fatalf("not wrapped in the markers:\n%s", out)
	}
}

func TestAgentsRenderLeavesACurrentFileAlone(t *testing.T) {
	cur := agentsBlock(agentsAll)
	_, did, changed, err := agentsRender(agentsAll, cur, true, agentsRefuse)
	if err != nil || changed || did != "already current" {
		t.Fatalf("got %q changed=%v err=%v", did, changed, err)
	}
}

func TestAgentsRenderStampsAnUnmarkedCopyOfTodaysText(t *testing.T) {
	out, did, changed, err := agentsRender(agentsAll, agentsText(agentsAll), true, agentsRefuse)
	if err != nil || !changed || did != "stamped" {
		t.Fatalf("got %q changed=%v err=%v", did, changed, err)
	}
	if out != agentsBlock(agentsAll) {
		t.Fatal("a stamped copy is not exactly the block")
	}
}

// Every text an older naut new wrote, recognised by its hash, is replaced
// by today's: that project was ours all along.
func TestAgentsRenderUpdatesAnOlderNautsUnmarkedCopy(t *testing.T) {
	old := "# AGENTS.md\n\nolder rules\n"
	agentsPrevious[sha(old)] = "test"
	defer delete(agentsPrevious, sha(old))
	if st, _ := agentsClassify(old, agentsAll); st != agentsStale {
		t.Fatalf("an older naut's copy classifies as %s, want stale", st)
	}
	out, did, _, err := agentsRender(agentsAll, old, true, agentsRefuse)
	if err != nil || did != "updated" || out != agentsBlock(agentsAll) {
		t.Fatalf("got %q err=%v", did, err)
	}
}

func TestAgentsRenderRefreshKeepsTheProjectsOwnNotes(t *testing.T) {
	above := "# Plant notes\n\nPump P-101 trips on a cold start.\n\n"
	below := "\n## Ours\n\nUse metric units.\n"
	stale := strings.Replace(agentsBlock(agentsAll), "# AGENTS.md", "# AGENTS.md (older)", 1)
	doc := above + stale + below
	if st, _ := agentsClassify(doc, agentsAll); st != agentsStale {
		t.Fatalf("an edited block classifies as %s, want stale", st)
	}
	out, did, _, err := agentsRender(agentsAll, doc, true, agentsRefuse)
	if err != nil || did != "updated" {
		t.Fatalf("got %q err=%v", did, err)
	}
	if out != above+agentsBlock(agentsAll)+below {
		t.Fatalf("the notes around the block moved or changed:\n%s", out)
	}
}

func TestAgentsRenderKeepsCRLF(t *testing.T) {
	above := "# Ours\r\n\r\n"
	stale := strings.ReplaceAll(strings.Replace(agentsBlock(agentsAll), "# AGENTS.md", "# old", 1), "\n", "\r\n")
	out, _, _, err := agentsRender(agentsAll, above+stale, true, agentsRefuse)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "\n") != strings.Count(out, "\r\n") {
		t.Fatal("a CRLF file came back with bare LFs")
	}
	if lf(out) != "# Ours\n\n"+agentsBlock(agentsAll) {
		t.Fatalf("wrong content:\n%s", out)
	}
}

// Somebody's own AGENTS.md is never overwritten unless they say so.
func TestAgentsRenderRefusesSomebodysOwnFileUnlessTold(t *testing.T) {
	own := "# Our rules\n\nNo magic numbers.\n"
	if st, _ := agentsClassify(own, agentsAll); st != agentsUnmanaged {
		t.Fatalf("own file classifies as %s", st)
	}
	if _, _, _, err := agentsRender(agentsAll, own, true, agentsRefuse); err == nil || !strings.Contains(err.Error(), "-append") {
		t.Fatalf("want a refusal that names -append, got %v", err)
	}
	out, _, _, err := agentsRender(agentsAll, own, true, agentsAppend)
	if err != nil || out != own+"\n"+agentsBlock(agentsAll) {
		t.Fatalf("append: err=%v\n%s", err, out)
	}
	out, _, _, err = agentsRender(agentsAll, "no newline", true, agentsAppend)
	if err != nil || out != "no newline\n\n"+agentsBlock(agentsAll) {
		t.Fatalf("append to a file with no final newline: err=%v\n%q", err, out)
	}
	out, _, _, err = agentsRender(agentsAll, own, true, agentsForce)
	if err != nil || out != agentsBlock(agentsAll) {
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
	if st, _ := agentsClassify(grown, agentsAll); st != agentsStale {
		t.Fatalf("an older copy with notes after it classifies as %s, want stale", st)
	}
	out, did, _, err := agentsRender(agentsAll, grown, true, agentsRefuse)
	if err != nil || did != "updated" || out != agentsBlock(agentsAll)+"\n## Site conventions\n\nTags are ISA-5.1.\n" {
		t.Fatalf("got %q err=%v:\n%s", did, err, out)
	}

	todayGrown := agentsText(agentsAll) + "\n## Ours\n"
	if st, _ := agentsClassify(todayGrown, agentsAll); st != agentsUnstamped {
		t.Fatalf("today's text with notes after it classifies as %s, want unstamped", st)
	}
	if out, _, _, _ := agentsRender(agentsAll, todayGrown, true, agentsRefuse); out != agentsBlock(agentsAll)+"\n## Ours\n" {
		t.Fatalf("stamping lost the notes:\n%s", out)
	}

	edited := strings.Replace(old, "older rules", "older rules, edited", 1)
	st, why := agentsClassify(edited, agentsAll)
	if st != agentsConflict || why == nil {
		t.Fatalf("an older copy edited inside classifies as %s (%v), want conflict", st, why)
	}
	if _, _, _, err := agentsRender(agentsAll, edited, true, agentsAppend); err == nil {
		t.Fatal("-append would leave two sets of rules in the file")
	}
	if out, _, _, err := agentsRender(agentsAll, edited, true, agentsForce); err != nil || out != agentsBlock(agentsAll) {
		t.Fatalf("-force: %v", err)
	}
}

func TestAgentsBrokenMarkersAreNeverGuessedAt(t *testing.T) {
	b := agentsBlock(agentsAll)
	for name, doc := range map[string]string{
		"no end":         strings.Replace(b, agentsEndMark, "", 1),
		"two begins":     b + b,
		"end before one": agentsEndMark + "\n" + "notes\n",
	} {
		if st, err := agentsClassify(doc, agentsAll); st != agentsConflict || err == nil {
			t.Errorf("%s: classified %s err=%v, want conflict with a reason", name, st, err)
		}
		if _, _, _, err := agentsRender(agentsAll, doc, true, agentsAppend); err == nil {
			t.Errorf("%s: -append went ahead on broken markers", name)
		}
		if out, _, _, err := agentsRender(agentsAll, doc, true, agentsForce); err != nil || out != b {
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
		{"current", agentsBlock(agentsAll), claudeText(), nil},
		{"current, claude has no import", agentsBlock(agentsAll), "Be terse.\n", []string{"CLAUDE.md does not import"}},
		{"own file, claude has no import", "# ours\n", "Be terse.\n", nil},
		{"stale", strings.Replace(agentsBlock(agentsAll), "# AGENTS.md", "# old", 1), claudeText(), []string{"older than this naut"}},
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
	current := agentsText(agentsAll)
	for _, rev := range strings.Fields(string(revs)) {
		body, err := exec.Command("git", "-C", top, "show", rev+":"+path).Output()
		if err != nil {
			t.Fatal(err)
		}
		text := strings.TrimRight(lf(string(body)), "\n") + "\n"
		// The template file is the core of a text assembled from sections
		// (agentsText), so the file itself was never written out whole:
		// only revisions from before the split were.
		if text == current || text == embeddedText("AGENTS.md.tmpl") {
			continue
		}
		if _, ok := agentsPrevious[sha(text)]; !ok {
			t.Errorf("%s at %.8s is not in agentsPrevious: add %q", path, rev, sha(text))
		}
	}
}

// A project's guidance carries the sections for what it uses (#1076): the
// AI reads it on every turn, so a project with no screens does not pay for
// the screens section, and one that adds its first screen is told, by the
// check, that its copy is stale.
func TestAgentsTextFollowsTheProject(t *testing.T) {
	st := agentsFeatures{ST: true}
	text := agentsText(st)
	if !strings.Contains(text, "## Structured Text (`.st`)") || !strings.Contains(text, "PROGRAM Scaling") {
		t.Error("an ST project's guidance has no worked ST example")
	}
	for _, gone := range []string{"## Ladder (`.ld`)\n", "## Function Block Diagram (`.fbd`)\n", "## Sequential Function Chart (`.sfc`)\n", "RUNG fillcmd", "CommandButton"} {
		if strings.Contains(text, gone) {
			t.Errorf("an ST-only project carries %q", gone)
		}
	}
	for _, stub := range []string{"## Ladder (`.ld`) (not used in this project yet)", "## Operator screens (not used in this project yet)"} {
		if !strings.Contains(text, stub) {
			t.Errorf("no stub %q for a section left out", stub)
		}
	}
	for _, how := range []string{"run `naut agents`", "https://nautilus.joyautomation.com/guides/operator-screens/", "https://nautilus.joyautomation.com/languages/ladder/"} {
		if !strings.Contains(text, how) {
			t.Errorf("the stubs never say %q", how)
		}
	}
	if !strings.Contains(text, "never rewrite a program into another language") {
		t.Error("the language table no longer says to leave working code in its language")
	}
	if len(agentsText(agentsAll)) < 2*len(text) {
		t.Errorf("the ST text (%d bytes) is not much shorter than the full one (%d)", len(text), len(agentsText(agentsAll)))
	}
}

func TestAgentsFeaturesFollowTheFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) {
		t.Helper()
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := agentsFeaturesFor(dir); got != agentsAll {
		t.Errorf("a project with no program gets %+v, want every section", got)
	}
	write("main.st")
	if got := agentsFeaturesFor(dir); got != (agentsFeatures{ST: true}) {
		t.Errorf("one .st file: %+v", got)
	}
	write("a.ld")
	write("b.mimic.json")
	if got := agentsFeaturesFor(dir); got != (agentsFeatures{ST: true, LD: true, Screens: true}) {
		t.Errorf("st + ld + screen: %+v", got)
	}
	write("c.fbd")
	write("d.sfc")
	if got := agentsFeaturesFor(dir); got != agentsAll {
		t.Errorf("every kind of file: %+v", got)
	}

	// The loader lower-cases extensions, so Main.ST and Loop.FBD count.
	up := t.TempDir()
	dir = up
	write("Main.ST")
	write("Loop.FBD")
	if got := agentsFeaturesFor(up); got != (agentsFeatures{ST: true, FBD: true}) {
		t.Errorf("upper-case extensions: %+v", got)
	}

	// Nested projects, build output, node_modules and dot-directories are
	// not this project's.
	nest := t.TempDir()
	dir = nest
	write("main.st")
	write("sub/nautilus.yaml")
	write("sub/x.mimic.json")
	write("build/y.ld")
	write("node_modules/z/z.sfc")
	write(".hidden/w.fbd")
	if got := agentsFeaturesFor(nest); got != (agentsFeatures{ST: true}) {
		t.Errorf("files outside the project counted: %+v", got)
	}
}

// Adding the first screen or ladder file makes the check report the copy
// stale for the new variant; a project that did not change stays current.
func TestAgentsStaleWhenTheProjectGrowsASection(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.st"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, agentsFileName), []byte(agentsBlock(agentsFeaturesFor(dir))), 0o644); err != nil {
		t.Fatal(err)
	}
	state := func() agentsState {
		t.Helper()
		as, _, _, err := agentsInspect(dir)
		if err != nil {
			t.Fatal(err)
		}
		return as
	}
	if as := state(); as != agentsCurrent {
		t.Fatalf("an unchanged project is %s, want current", as)
	}
	for _, first := range []string{"loop.ld", "plant.mimic.json"} {
		if err := os.WriteFile(filepath.Join(dir, first), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if as := state(); as != agentsStale {
			t.Errorf("after adding %s the guidance is %s, want stale", first, as)
		}
		if _, err := agentsWrite(dir, agentsRefuse); err != nil {
			t.Fatal(err)
		}
		if as := state(); as != agentsCurrent {
			t.Errorf("after naut agents with %s the guidance is %s, want current", first, as)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(dir, agentsFileName))
	if !strings.Contains(string(raw), "RUNG fillcmd") || !strings.Contains(string(raw), "CommandButton") {
		t.Error("the refreshed guidance does not carry the ladder and screens sections")
	}
}
