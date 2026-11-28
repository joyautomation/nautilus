package main

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/oplog"
	"github.com/joyautomation/nautilus/quorum/node"
)

// #1455 PR 2a-2 in the pair's wiring: a controller whose log diverged (a
// committed compaction did not fit it) restarts itself under the
// supersede rule, once per start; and the log compaction's conditions.

func TestADivergedControllerRestartsItselfOnlyUnderTheRule(t *testing.T) {
	own := oplog.Lineage{History: 2, HistoryID: 0xbeef}
	peer := node.PeerLog{Synced: true, Leads: true, History: 2, HistoryID: 0xbeef}
	const why = "the compaction at seq 9 does not fit"
	for _, tc := range []struct {
		name                         string
		diverged                     string
		leading                      bool
		pl                           node.PeerLog
		supervised, atStart, restart bool
		want                         string // in the condition; "" none
	}{
		{"not diverged", "", false, peer, true, false, false, ""},
		{"every condition holds", why, false, peer, true, false, true, "restarting now"},
		{"it leads", why, true, peer, true, false, false, "it leads"},
		{"the other is at another history", why, false, node.PeerLog{Synced: true, Leads: true, History: 3}, true, false, false, "does not lead synced"},
		{"no supervisor", why, false, peer, false, false, false, "no supervisor"},
		{"restarted once already", why, false, peer, true, true, false, "already restarted once for a diverged log"},
	} {
		msg, restart := divergedDecision(tc.diverged, own, tc.pl, true, tc.leading, tc.supervised, tc.atStart)
		switch {
		case restart != tc.restart:
			t.Errorf("%s: restart %v, want %v (%q)", tc.name, restart, tc.restart, msg)
		case tc.want == "" && msg != "":
			t.Errorf("%s: condition %q, want none", tc.name, msg)
		case tc.want != "" && (!strings.Contains(msg, tc.want) || !strings.Contains(msg, why) || strings.Contains(msg, "vouch")):
			t.Errorf("%s: condition %q, want %q and why, no vouch", tc.name, msg, tc.want)
		}
	}
}

func TestTheLogCompactionsConditions(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	disk, behind, blocked, refused := compactConditions(oplog.Status{}, now)
	if disk != "" || behind != "" || blocked != "" || refused != "" {
		t.Fatalf("a log with nothing to say: %q %q %q %q", disk, behind, blocked, refused)
	}
	var st oplog.Status
	st.CompactRefused = "/var/lib/naut: 3 MB free, the log is 9 MB: the compaction waits for at least 18 MB free"
	st.BehindBase = []string{"b: it runs a build from before log compaction and missed the compacted part (it has 0, the base is at 30): update it"}
	st.CompactBlocked = "waiting for the witness"
	st.CompactBlockedSince = now.Add(-6 * 24 * time.Hour).UnixMilli()
	disk, behind, blocked, _ = compactConditions(st, now)
	if !strings.Contains(disk, "3 MB free") || !strings.Contains(behind, "b: it runs a build from before") || blocked != "" {
		t.Fatalf("disk %q, behind %q, blocked %q (6 days: not yet)", disk, behind, blocked)
	}
	st.CompactBlockedSince = now.Add(-8 * 24 * time.Hour).UnixMilli()
	if _, _, blocked, _ = compactConditions(st, now); !strings.Contains(blocked, "8 days") || !strings.Contains(blocked, "waiting for the witness") {
		t.Fatalf("blocked 8 days: %q", blocked)
	}
	st.CompactDecisionRefused = "oplog: compaction refused: alarms: a replay of the kept entries differs"
	if _, _, _, refused = compactConditions(st, now); !strings.Contains(refused, "alarms: a replay") || !strings.Contains(refused, "report it") {
		t.Fatalf("a refused decision: %q", refused)
	}
}

// A diverged log is unsynced, but its condition is oplog-diverged's
// restart, never the unsynced one's vouch.
func TestADivergedLogIsNotToldToVouch(t *testing.T) {
	st := oplog.Status{Unsynced: true}
	if _, unsynced := syncConditions(st); !strings.Contains(unsynced, "vouch") {
		t.Fatalf("an unsynced log: %q", unsynced)
	}
	st.Diverged = "the compaction at seq 9 does not fit"
	if lost, unsynced := syncConditions(st); lost != "" || unsynced != "" {
		t.Fatalf("a diverged log: lost %q, unsynced %q; want neither", lost, unsynced)
	}
}

// TestPairCompactionIsOnInThisBuild (#1455 PR 2b-2, the switch-on): the
// pair's two logs and the witness's, opened as naut run and naut witness
// open them, each advertise this build's keep-rules version in their
// answers, which the leader's gate reads: one left without it (the
// mutants pair_compaction_off, witness_compaction_off) holds every
// compaction back for good.
func TestPairCompactionIsOnInThisBuild(t *testing.T) {
	r := newPairRig(t)
	rules := func(who string, code int, body string) {
		t.Helper()
		var ans struct {
			CompactRules int `json:"compactRules"`
		}
		if code != http.StatusOK || json.Unmarshal([]byte(body), &ans) != nil {
			t.Fatalf("%s's log: %d %s", who, code, body)
		}
		if ans.CompactRules < 1 || ans.CompactRules != compactRules {
			t.Errorf("%s's log advertises keep rules %d, want %d and at least 1 (compaction on): %s", who, ans.CompactRules, compactRules, body)
		}
	}
	for _, n := range []string{"a", "b"} {
		req, _ := http.NewRequest("GET", r.peerScheme()+r.peer[n]+"/api/oplog?after=0", nil)
		req.Header.Set(node.TokenHeader, testPeerToken)
		resp, err := r.peerHTTPAs(other(n), n).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		rules("controller "+n, resp.StatusCode, string(b))
	}
	code, body := r.witnessLog("GET", "/api/oplog?after=0", "")
	rules("the witness", code, body)
}

// TestTheCompactionSeamIsNotInAReleaseBuild (#1455 PR 2b-1.9, the lead's
// condition): the oplogtest seam, which sets a keep-rules version and
// short timings, is compiled only with its tag. In cmd/naut and oplog the
// files that name the tag are exactly the seam's; a release build (the
// release config sets no tags or flags) takes the no-op hook, whose body
// is empty, and leaves the seam out; and in oplog only the seam sets a
// keep-rules version (cmd/naut's own, compactRules, is
// TestPairCompactionIsOnInThisBuild's).
func TestTheCompactionSeamIsNotInAReleaseBuild(t *testing.T) {
	ctx := build.Default
	ctx.BuildTags = nil
	want := map[string]bool{"logseam_off.go": true, "logseam_on.go": false, "../../oplog/compact_seam.go": false}
	for _, dir := range []string{".", "../../oplog"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			in, err := ctx.MatchFile(dir, filepath.Base(f))
			if err != nil {
				t.Fatal(err)
			}
			name := filepath.Base(f)
			if dir != "." {
				name = filepath.Join(dir, name)
			}
			seam, listed := want[name]
			switch {
			case bytes.Contains(src, []byte("oplogtest")) != listed:
				t.Errorf("%s: names the oplogtest tag %v, but the seam's files are exactly %v", name, !listed, want)
			case listed && in != seam:
				t.Errorf("%s in a release build: %v, want %v", name, in, seam)
			case dir != "." && in && setsRules.Match(src):
				t.Errorf("%s sets a keep-rules version in a release build: only the seam may", name)
			}
		}
	}
	// The no-op hook does nothing.
	fs := token.NewFileSet()
	af, err := parser.ParseFile(fs, "logseam_off.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range af.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "testLogOptions" && len(fd.Body.List) != 0 {
			t.Error("logseam_off.go's testLogOptions does something: a release build would change the logs' options")
		}
	}
	// Build tags or flags in the release's config (release.yml's own
	// `tags:` is its trigger, a git tag filter).
	for f, pats := range map[string][]string{
		"../../.goreleaser.yaml":              {`oplogtest`, `GOFLAGS`, `(?m)(^|\s)-tags[ =]`, `(?m)(^|\s)flags:`, `(?m)(^|\s)tags:`},
		"../../.github/workflows/release.yml": {`oplogtest`, `GOFLAGS`, `(?m)(^|\s)-tags[ =]`, `(?m)(^|\s)flags:`},
	} {
		cfg, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range pats {
			if regexp.MustCompile(p).Match(cfg) {
				t.Errorf("%s matches %q: check the oplogtest seam stays out of a release", f, p)
			}
		}
	}
}

// setsRules finds an Options keep-rules version set in source: an
// assignment to o.CompactRules or the field in an Options literal.
var setsRules = regexp.MustCompile(`(?s)\bo\.CompactRules\s*=[^=]|Options\{[^}]*\bCompactRules:`)
