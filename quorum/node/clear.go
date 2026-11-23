package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// The operator's clear-duplicate command across the pair (#1029 3c-3a).
// A mark lives in each member's voter: this controller's, its peer's and
// the witness's. The command runs on one controller, which asks the other
// two over the peer port (POST Prefix + cluster + "/dup", behind Guard like
// every peer request) in three phases:
//
//  1. check: every member must answer, and the boots of the name the
//     members hear, taken together (quorum.CopyLive), must be at most one.
//     A copy behind a split may reach one member and its original another,
//     so no member alone hears both. Any member silent, or two boots heard,
//     refuses the whole command and nothing is cleared (all or nothing).
//  2. clear: each member clears its mark, re-checking its own voter first
//     (quorum.Voter.CopyHeard): a copy heard since the check refuses there.
//  3. re-check, a lease later: a member that marks the name again heard
//     the copy beside the original, so a copy still runs.
//
// The held controller frees itself: its next probe gets an unmarked reply
// from the same voter boot that marked it (quorum.Candidate.NoteMark), so
// no restart is needed. A member the clear phase can't reach keeps its
// mark (never worse than before the command); running it again finishes.

// dupOp is a /dup request: check or clear name, on behalf of By (the
// principal the issuing controller authenticated; for the member's log).
type dupOp struct {
	Op   string `json:"op"` // "check", "clear" or "fence"
	Name string `json:"name"`
	By   string `json:"by,omitempty"`
	// Keep, on a clear: the one boot of the name the check heard (0:
	// none). The member refuses the clear if it hears any other, and
	// retires every other boot it knows (quorum.Voter.ClearDuplicateKeeping).
	Keep uint64 `json:"keep,omitempty"`
	// Retire, on a clear: every boot of the name any member knew at the
	// check but Keep; each member retires them all, so a copy known to one
	// member is retired at the others too (naut-lead's ruling on #1135).
	Retire []quorum.BootSeq `json:"retire,omitempty"`
	// Gen, on a clear: the name's new clear generation, above every
	// member's and every hold's at the check (checkPhase). A hold on a
	// durable mark ends only on a word with a higher one, which a wiped
	// voter or a copy from an older disk can't give (#1029 3c-3b).
	Gen uint64 `json:"gen,omitempty"`
	// Fence, on a fence (recommission --from-spare's last phase, #1029
	// 3c-3c-1b): the fence the command carries, the peer's answer for the
	// witness; the member raises it above its own adoption (quorum.Voter.Fence).
	Fence uint64 `json:"fence,omitempty"`
}

// DupAnswer is one member's answer to a check or a clear.
type DupAnswer struct {
	Member string `json:"member"` // "a", "b" or "witness"
	// Boots are the boots of the name this member's voter heard within a
	// lease and D (quorum.Voter.LiveBoots).
	Boots []uint64 `json:"boots,omitempty"`
	// Known are every boot of the name the member's voter knows
	// (quorum.Voter.KnownBoots), on a check; Retired, the ones a clear
	// retired there (quorum.Voter.RetiredBoots).
	Known   []quorum.BootSeq `json:"known,omitempty"`
	Retired []uint64         `json:"retired,omitempty"`
	// Marked: the member's voter marks the name (after a clear: still).
	Marked bool `json:"marked"`
	// Gen is the name's clear generation at the member's voter, and
	// HoldGen the highest a hold on this controller's own candidate was
	// set under (a controller's own name only): the clear's generation
	// goes above both, so no hold is left on an older one (a member
	// restored from a backup).
	Gen     uint64 `json:"gen,omitempty"`
	HoldGen uint64 `json:"holdGen,omitempty"`
	// Held: this controller's own candidate is held by a mark (its own
	// name only). A hold can outlive every member's mark (the marking
	// voter crashed before its write, or was wiped), and only a clear's
	// generation ends it then, so the check counts it as a mark (#1144
	// review).
	Held bool `json:"held,omitempty"`
	// Cleared: this clear removed the member's mark.
	Cleared bool `json:"cleared,omitempty"`
	// Fence is the name's fence at the member (quorum.Voter.FenceOf): the
	// lowest adoption generation of the name's state it takes, 0 for none.
	Fence uint64 `json:"fence,omitempty"`
	// Refused is why the member refused a clear (it hears two boots).
	Refused string `json:"refused,omitempty"`
	// Err is why the member could not be asked (unreachable, an older
	// release, a spare); empty when it answered.
	Err string `json:"error,omitempty"`
}

// ClearReport is what the clear command did, member by member.
type ClearReport struct {
	Name string `json:"name"`
	// Check is each member's answer to the check phase.
	Check []DupAnswer `json:"check"`
	// Clear is each member's answer to the clear phase (empty when the
	// check refused).
	Clear []DupAnswer `json:"clear,omitempty"`
	// Recheck is each member's answer a lease after the clear.
	Recheck []DupAnswer `json:"recheck,omitempty"`
	// Refused is why the check phase refused the command, else empty.
	Refused string `json:"refused,omitempty"`
	// Keep is the one boot the check heard, which the clear kept (0: none).
	Keep uint64 `json:"keep,omitempty"`
	// Retired are the other boots of the name the members knew, which the
	// clear set aside: empty when no member knew one (a hold whose marks
	// were all forgotten), so a copy still alive would not be marked again
	// on its return, only on being heard beside the original.
	Retired []quorum.BootSeq `json:"retired,omitempty"`
	// CopyHeardAfter: at the re-check the members together heard two
	// processes of the name: a copy runs.
	CopyHeardAfter bool `json:"copyHeardAfter,omitempty"`
}

// Done reports whether no member marks the name after the command.
func (r ClearReport) Done() bool {
	if r.Refused != "" || len(r.Recheck) == 0 || r.CopyHeardAfter {
		return false
	}
	if !mut("clear_done_ignores_clear") {
		// A member the clear phase missed retired nothing: not done, and
		// the check lets the command run again (checkPhase).
		for _, a := range r.Clear {
			if a.Err != "" || a.Refused != "" {
				return false
			}
		}
	}
	for _, a := range r.Recheck {
		if a.Marked || a.Err != "" || a.Refused != "" {
			return false
		}
		if a.Held && !mut("done_ignores_held") {
			return false // a hold still standing: not done (#1144 re-check)
		}
	}
	return true
}

// ErrClearRefused: the clear's check phase refused (ClearReport.Refused
// says why); nothing was cleared.
var ErrClearRefused = errors.New("redundancy: clear refused")

// ErrClearIncomplete: the clear ran, but a member still marks the name, or
// could not be asked, after it (ClearReport says which).
var ErrClearIncomplete = errors.New("redundancy: clear incomplete")

// dupAnswer applies q to voter v at now, for member: under the voter's
// lock.
func dupAnswer(v *quorum.Voter, now quorum.Instant, member string, q dupOp) DupAnswer {
	name := quorum.NodeID(q.Name)
	a := DupAnswer{Member: member, Boots: v.LiveBoots(now, name), Known: v.KnownBoots(name), Retired: v.RetiredBoots(name), Gen: v.DupGen(name), Fence: v.FenceOf(name)}
	if !v.Settled(now) {
		// Started under a lease and D ago: it can't say what boots ran
		// before, so it answers neither a check nor a clear.
		a.Refused = member + " started less than a lease and a quarter ago and can't yet tell which processes run: try again shortly"
		a.Boots = nil
		for _, n := range v.Marked() {
			a.Marked = a.Marked || n == name
		}
		return a
	}
	marked := false
	for _, n := range v.Marked() {
		if n == name {
			marked = true
		}
	}
	if q.Op == "fence" {
		// No hearing guard: the clear just before it had one, and a fence
		// only refuses the name's older state (#1029 3c-3c-1b).
		a.Fence = v.Fence(name, q.Fence)
	}
	if q.Op == "clear" {
		// A member that marks nothing still retires the boots any member
		// knew: the copy may come back beside the member that never heard
		// it (naut-lead's ruling on #1135).
		other := false
		for _, b := range a.Boots {
			other = other || b != q.Keep
		}
		// Any boot heard but Keep refuses (two boots heard is one such).
		if other && !mut("clear_ignores_keep") {
			a.Refused = fmt.Sprintf("%s hears a process named %s other than the one the check heard: stop the copy first", member, q.Name)
		} else if marked || !mut("clear_retires_marked_only") {
			v.ClearDuplicateKeeping(name, q.Keep, q.Retire, q.Gen)
			a.Cleared, marked = marked, false
			a.Gen = v.DupGen(name)
		}
	}
	a.Marked = marked
	return a
}

// readDupOp reads a /dup request body: a small JSON object.
func readDupOp(r *http.Request) (dupOp, error) {
	var q dupOp
	b, err := io.ReadAll(io.LimitReader(r.Body, 16384))
	if err != nil {
		return q, err
	}
	if err := json.Unmarshal(b, &q); err != nil {
		return q, err
	}
	if q.Op == "state" {
		return q, nil // a member's commissioning state (commission.go): no name
	}
	if (q.Op != "check" && q.Op != "clear" && q.Op != "fence") || !memberName.MatchString(q.Name) {
		return q, fmt.Errorf("bad request: op %q, name %q", q.Op, q.Name)
	}
	return q, nil
}

func writeDupAnswer(w http.ResponseWriter, a DupAnswer) { writeJSON(w, a) }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// serveDup answers the peer's check or clear for this controller's voter.
func (e *Elector) serveDup(w http.ResponseWriter, r *http.Request) {
	if !e.running.Load() || e.silent() {
		http.Error(w, "not running", http.StatusServiceUnavailable)
		return
	}
	q, err := readDupOp(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if q.Op == "state" {
		writeJSON(w, e.stateLocal())
		return
	}
	writeDupAnswer(w, e.dupLocal(q))
}

// fenceNameRefusal is why member refuses a fence on name, "" if it may
// set one: a controller fences only its peer, the witness only the
// pair's two controllers (#1164 review, S3: a fence on the witness's own
// name would make a controller's candidate drop every witness reply).
func fenceNameRefusal(member string, name string, allowed ...string) string {
	if mut("fence_any_name") {
		return ""
	}
	for _, n := range allowed {
		if n == name {
			return ""
		}
	}
	return fmt.Sprintf("%s fences only %s, not %q", member, strings.Join(allowed, " and "), name)
}

// dupWriteFloor is the shortest a member waits for a clear's or a fence's
// records to reach disk before it answers. The wait is an operator
// command's, not a lease path's, so it need not shrink with the lease: a
// quarter of a 1 s lease (250 ms) is shorter than an fsync on a slow SD
// card or a loaded CI runner, and the command then ends incomplete for a
// write that only took its time (#1329).
const dupWriteFloor = 2 * time.Second

// dupWriteWait is how long a member waits for that write: its share of the
// lease, or the floor if longer.
func dupWriteWait(share time.Duration) time.Duration {
	if mut("dup_wait_lease_scaled") {
		return share
	}
	return max(share, dupWriteFloor)
}

// dupDurable waits, after a clear or a fence, until v's duplicate records
// are on disk, for up to d: the answer says "cleared" or "fenced" only of
// a record a crash can't lose (#1164 review, S1). It returns why not, ""
// once durable. mu guards v; failing says the writer's last error.
func dupDurable(mu *sync.Mutex, v *quorum.Voter, d time.Duration, failing func() string) string {
	if mut("dup_answered_before_write") {
		return ""
	}
	deadline := time.Now().Add(d)
	for {
		mu.Lock()
		owed, why := v.MarksOwed(), failing()
		mu.Unlock()
		if !owed {
			return ""
		}
		if time.Now().After(deadline) {
			if why == "" {
				why = "the write did not finish in time"
			}
			return "its state file could not be written: " + why
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// dupLocal applies q to this controller's own voter.
func (e *Elector) dupLocal(q dupOp) DupAnswer {
	if q.Op == "fence" {
		if why := fenceNameRefusal(string(e.o.Self), q.Name, string(e.o.Peer)); why != "" {
			return DupAnswer{Member: string(e.o.Self), Refused: why}
		}
	}
	e.mu.Lock()
	a := dupAnswer(e.v, e.now(), string(e.o.Self), q)
	if q.Name == string(e.o.Self) {
		a.HoldGen = e.c.HoldGen()
		a.Held = e.c.Marked()
	}
	p := e.v.MarkWrite() // a clear's records, durable without waiting for the next frame
	e.mu.Unlock()
	e.w.ask(p)
	if (q.Op == "clear" || q.Op == "fence") && a.Refused == "" {
		if why := dupDurable(&e.mu, e.v, dupWriteWait(e.o.Lease/2), func() string { return e.writeErr }); why != "" {
			a.Err = string(e.o.Self) + ": " + why
		}
	}
	if a.Cleared {
		e.event(Event{Kind: EventDuplicateCleared, Detail: q.Name})
		e.log.Warn("redundancy: the operator cleared the duplicate mark on "+q.Name, "by", q.By)
	}
	return a
}

// serveDup answers a pair member's check or clear for the cluster's voter
// at the witness. A cluster this witness hasn't answered since it started
// has no voter running, so it marks nothing and hears no boot.
func (w *Witness) serveDup(rw http.ResponseWriter, r *http.Request) {
	name := r.PathValue("cluster")
	q, err := readDupOp(r)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	w.mu.Lock()
	p, found := w.pairs[name]
	w.mu.Unlock()
	if !found {
		// No voter running for the pair since this witness started: it
		// can't say what boots ran (#1135 review).
		http.Error(rw, "this witness has not heard the pair since it started: try again once the pair's controllers have voted", http.StatusServiceUnavailable)
		return
	}
	if members := r.Header.Get(MembersHeader); members != p.members {
		http.Error(rw, "not this cluster's members", http.StatusForbidden)
		return
	}
	if q.Op == "state" {
		p.mu.Lock()
		m := voterState(p.v, w.now(), "witness")
		p.mu.Unlock()
		writeJSON(rw, m)
		return
	}
	if q.Op == "fence" {
		if why := fenceNameRefusal("the witness", q.Name, strings.Split(p.members, ",")...); why != "" {
			writeDupAnswer(rw, DupAnswer{Member: "witness", Refused: why})
			return
		}
	}
	p.mu.Lock()
	a := dupAnswer(p.v, w.now(), "witness", q)
	mw := p.v.MarkWrite()
	p.mu.Unlock()
	p.w.ask(mw)
	if (q.Op == "clear" || q.Op == "fence") && a.Refused == "" {
		if why := dupDurable(&p.mu, p.v, dupWriteWait(w.o.Lease/4), func() string { return p.failing }); why != "" {
			a.Err = "the witness: " + why
		}
	}
	if a.Cleared {
		p.log.Warn("witness: the operator cleared the duplicate mark on "+q.Name, "by_as_the_controller_says", q.By, "from", r.Header.Get(MembersHeader))
	}
	writeDupAnswer(rw, a)
}

// dupRemote asks the member at base (the peer or the witness). It waits
// out the member's wait for its write (dupWriteWait), and a lease more.
func (e *Elector) dupRemote(ctx context.Context, member, base string, q dupOp) DupAnswer {
	b, _ := json.Marshal(q)
	timeout := e.o.Lease
	if !mut("dup_wait_lease_scaled") {
		timeout += dupWriteWait(e.o.Lease / 2)
	}
	body, err := e.call.call(ctx, timeout, http.MethodPost, base+Prefix+e.o.Cluster+"/dup", b)
	if err != nil {
		var se *statusError
		msg := member + " is not reachable: " + err.Error()
		if errors.As(err, &se) && se.code == http.StatusNotFound {
			msg = member + " runs a release that can't clear a mark: upgrade it, or restart it to clear its marks"
		} else if errors.As(err, &se) && se.code == http.StatusServiceUnavailable {
			msg = member + " is not answering (stopped, starting, or a spare): " + strings.TrimSpace(se.msg)
		}
		return DupAnswer{Member: member, Err: msg}
	}
	var a DupAnswer
	if err := json.Unmarshal(body, &a); err != nil {
		return DupAnswer{Member: member, Err: fmt.Sprintf("%s: unreadable answer: %v", member, err)}
	}
	a.Member = member
	return a
}

// dupAll asks the three members in turn: this controller, its peer, the
// witness.
func (e *Elector) dupAll(ctx context.Context, q dupOp) []DupAnswer {
	return []DupAnswer{
		e.dupLocal(q),
		e.dupRemote(ctx, string(e.o.Peer), e.peer, q),
		e.dupRemote(ctx, "witness", e.wit, q),
	}
}

// checkPhase decides the clear's check (all or nothing) from every
// member's answer: the boot to keep, the boots to retire (every boot any
// member knew but that one), or why the command is refused.
func checkPhase(name string, check []DupAnswer) (keep uint64, retire []quorum.BootSeq, gen uint64, refused string) {
	var silent, hearing []string
	var answers [][]uint64
	var known [][]quorum.BootSeq
	var retired [][]uint64
	marked := false
	for _, a := range check {
		if a.Err != "" || a.Refused != "" {
			silent = append(silent, a.Err+a.Refused)
			continue
		}
		answers = append(answers, a.Boots)
		known = append(known, a.Known)
		retired = append(retired, a.Retired)
		if len(a.Boots) > 0 {
			hearing = append(hearing, fmt.Sprintf("%s hears %d", a.Member, len(a.Boots)))
		}
		marked = marked || a.Marked || (a.Held && !mut("clear_ignores_held"))
		gen = max(gen, a.Gen, a.HoldGen)
	}
	gen++
	if mut("clear_gen_ignores_holds") {
		gen = 1
	}
	keep = quorum.KeptBoot(answers...)
	for _, b := range quorum.UnionBoots(known...) {
		if b.Nonce != keep {
			retire = append(retire, b)
		}
	}
	// A boot still to be retired at some member (a member the last clear
	// missed) lets the command run again with no mark left.
	pending := false
	for _, b := range retire {
		for _, rs := range retired {
			pending = pending || !slices.Contains(rs, b.Nonce)
		}
	}
	if mut("clear_rerun_refused") {
		pending = false
	}
	switch {
	case len(silent) > 0 && !mut("clear_partial_check"):
		return 0, nil, 0, "every member must answer before anything is cleared: " + strings.Join(silent, "; ")
	case quorum.CopyLive(answers...):
		return 0, nil, 0, fmt.Sprintf("two processes named %s were heard within the last lease (%s): stop the copy, confirm it is down, and try again a lease later", name, strings.Join(hearing, ", "))
	case !marked && !pending:
		return 0, nil, 0, "no member marks " + name + ", and every member has retired the other processes it knew: nothing to clear"
	case keep == 0 && !mut("clear_without_keep"):
		// A clear that keeps nothing has no use, and would retire a real
		// controller that is only paused (naut-lead's ruling on #1135).
		return 0, nil, 0, "nothing running under " + name + " to keep: no member heard a process named " + name + " in the last lease and a quarter. Start the real controller, or heal its links if it is cut off from the other members, then clear"
	}
	return keep, retire, gen, ""
}

// ClearDuplicateAll is the operator's clear-duplicate command (see the top
// of this file): check every member, clear every member, re-check a lease
// later. by names the person, for the members' logs. It returns
// ErrClearRefused when the check refused (nothing cleared), and
// ErrClearIncomplete when a member still marks the name, or could not be
// asked, at the re-check. A spare refuses it: a spare takes no part in the
// pair until recommission --from-spare.
func (e *Elector) ClearDuplicateAll(ctx context.Context, name, by string) (ClearReport, error) {
	rep := ClearReport{Name: name}
	if !memberName.MatchString(name) {
		return rep, fmt.Errorf("redundancy: %q is not a member name", name)
	}
	if e.Spare() {
		rep.Refused = "this controller is a spare: it takes no part in the pair (recommission --from-spare turns it into a member)"
		return rep, ErrClearRefused
	}
	rep.Check = e.dupAll(ctx, dupOp{Op: "check", Name: name, By: by})
	var retire []quorum.BootSeq
	var gen uint64
	rep.Keep, retire, gen, rep.Refused = checkPhase(name, rep.Check)
	rep.Retired = retire
	if rep.Refused != "" {
		return rep, ErrClearRefused
	}
	rep.Clear = e.dupAll(ctx, dupOp{Op: "clear", Name: name, By: by, Keep: rep.Keep, Retire: retire, Gen: gen})
	select {
	case <-ctx.Done():
		return rep, ctx.Err()
	case <-time.After(e.o.Lease):
	}
	rep.Recheck = e.dupAll(ctx, dupOp{Op: "check", Name: name, By: by})
	var again [][]uint64
	for _, a := range rep.Recheck {
		again = append(again, a.Boots)
	}
	if quorum.CopyLive(again...) && !mut("recheck_marks_only") {
		// No member may have marked it yet (a copy behind a split), but
		// the members together heard two processes since the clear.
		rep.CopyHeardAfter = true
	}
	if !rep.Done() {
		return rep, ErrClearIncomplete
	}
	return rep, nil
}
