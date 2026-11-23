package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// cli runs naut redundancy against member's tag API with the rig's token.
func (r *boxRig) cli(member string, args ...string) (int, string) {
	var out bytes.Buffer
	env := map[string]string{"NAUTILUS_TOKEN": "api-secret", "NAUTILUS_URL": r.apiURL[member]}
	code := redundancyMain(args, func(k string) string { return env[k] }, &out, &out)
	return code, out.String()
}

// The operator's clear-duplicate command end to end (#1029 3c-3a), through
// naut redundancy, the tag API, the audit seam and the peer port: refused
// while the copy runs (nothing cleared), refused without --copy-stopped,
// then, with the copy stopped, every member's mark cleared and a freed
// WITHOUT a restart: it leads again. (The journal entries are pinned by
// the server's audit table; on a standby GET /api/audit reads the
// leader's journal.)
func TestTheClearCommandClearsAMarkWithoutARestart(t *testing.T) {
	r := newBoxRigAPI(t)
	r.cloneDisk("a", "box-a")
	pairWait(t, "b to mark a", 10*time.Second, func() bool { return slices.Contains(r.el("b").DuplicateMarks(), "a") })

	if code, out := r.cli("b", "clear-duplicate", "a"); code != 1 || !strings.Contains(out, "--copy-stopped") {
		t.Fatalf("without --copy-stopped: exit %d\n%s", code, out)
	}
	code, out := r.cli("b", "clear-duplicate", "a", "--copy-stopped")
	if code != 1 || !strings.Contains(out, "REFUSED, nothing was cleared") {
		t.Fatalf("with the copy running: exit %d\n%s", code, out)
	}
	if !slices.Contains(r.el("b").DuplicateMarks(), "a") {
		t.Fatal("b's mark went while the copy ran")
	}

	r.nodes["clone"].crash()
	time.Sleep(1500 * time.Millisecond) // past a lease and D since the copy's last frame
	code, out = r.cli("b", "clear-duplicate", "a", "--copy-stopped")
	if code != 0 || !strings.Contains(out, "done: no member marks a") {
		t.Fatalf("with the copy stopped: exit %d\n%s", code, out)
	}
	if m := r.el("b").DuplicateMarks(); len(m) != 0 {
		t.Fatalf("b marks %v after the clear", m)
	}
	pairWait(t, "a's hold to end without a restart", 5*time.Second, func() bool { return !r.el("a").Status().Held })
	pairWait(t, "a leader", 15*time.Second, func() bool { return r.el("a").IsLeader() || r.el("b").IsLeader() })
	r.handLeadTo("a") // a campaigns again: no restart of a, b or the witness

	if code, out := r.cli("b", "status"); code != 0 || !strings.Contains(out, "controller b") || strings.Contains(out, "marks as run by two") {
		t.Fatalf("status: exit %d\n%s", code, out)
	}
}

// The real box made a spare (its copy was running first), recovered with
// the command, in the guide's order: stop the copy; on the real box stop
// naut, delete the voter state file and start it (a spare answers no
// command, so the clear must wait until it is a member again); then clear
// from the other controller. It re-adopts, the clear frees it, and it
// leads again with no member restarted (#1135 review: the first order,
// clear then reset, was always refused).
func TestTheRealBoxMadeASpareRecoversWithTheCommand(t *testing.T) {
	r := newBoxRigAPI(t)
	r.handLeadTo("b")
	r.nodes["a"].crash()
	r.cloneDiskEnv("a", "box-a")
	time.Sleep(2 * time.Second) // four leases: the copy is the a everyone hears
	ln, err := net.Listen("tcp", r.lns[0].Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	r.start("a", "box-a", r.env["a"], ln)
	pairWait(t, "the real a to go spare beside the copy heard first", 10*time.Second, func() bool { return r.el("a").Spare() })

	r.nodes["clone"].crash()
	if code, out := r.cli("b", "clear-duplicate", "a", "--copy-stopped"); code != 1 || !strings.Contains(out, "REFUSED") {
		t.Fatalf("a clear while a is still a spare: exit %d\n%s", code, out)
	}
	r.nodes["a"].stop()
	if err := os.Remove(filepath.Join(r.env["a"]["STATE_DIRECTORY"], pairStateFile)); err != nil {
		t.Fatal(err)
	}
	ln, err = net.Listen("tcp", r.lns[0].Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	r.start("a", "box-a", r.env["a"], ln)
	pairWait(t, "a to re-adopt from b", 15*time.Second, func() bool { return r.el("a").Status().Commissioned })
	var out string
	pairWait(t, "the clear to go through", 10*time.Second, func() bool {
		var code int
		code, out = r.cli("b", "clear-duplicate", "a", "--copy-stopped")
		return code == 0
	})
	if !strings.Contains(out, "done: no member marks a") {
		t.Fatalf("clear:\n%s", out)
	}
	pairWait(t, "a's hold to end", 5*time.Second, func() bool { return !r.el("a").Status().Held })
	r.handLeadTo("a")
	r.handLeadTo("b")
}

// #710: naut redundancy status names the writes cut at the send deadline,
// by device, and says nothing when none were.
func TestStatusPrintsWritesCutAtTheSendDeadline(t *testing.T) {
	var out strings.Builder
	printClusterStatus(&out, []byte(`{"mode":"quorum","pod":"a","leader":"a","isLeader":true,"writesCut":{"plc1":14,"bmc2":3}}`))
	if !strings.Contains(out.String(), "writes cut at the send deadline (last 5 min): bmc2 3, plc1 14\n") {
		t.Fatalf("status:\n%s", out.String())
	}
	out.Reset()
	printClusterStatus(&out, []byte(`{"mode":"quorum","pod":"a","leader":"a","isLeader":true}`))
	if strings.Contains(out.String(), "writes cut") {
		t.Fatalf("no cuts, no line:\n%s", out.String())
	}
}
