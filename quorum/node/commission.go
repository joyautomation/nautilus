package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// The operator's commissioning commands (#1029 3c-3c): commission a new
// pair, re-commission a controller that lost its state, and turn a spare
// back into the member (recommission --from-spare). Each asks the other
// members over the peer port (POST Prefix + cluster + "/dup", the clear's
// route) before it acts:
//
//   - op "state": each member's voter says whether it is commissioned and
//     the highest term it knows (MemberState). commission needs all three
//     fresh; recommission's floor must be above every term a member that
//     answered knows, not only this controller's.
//   - ops "check" and "clear" (clear.go): --from-spare needs both other
//     voters to hear no process of its name, then clears the name keeping
//     no boot, and moves its own state file aside. The restart after it
//     starts uncommissioned and re-adopts from the peer (#976), as after a
//     box change.

// MemberState is a member's answer to the "state" op.
type MemberState struct {
	Member string `json:"member"` // "a", "b" or "witness"
	// Commissioned: a controller that adopted an incarnation. The witness
	// is never "commissioned": its Incarnation is the pair's to adopt.
	Commissioned bool   `json:"commissioned"`
	Incarnation  uint64 `json:"incarnation,omitempty"`
	// Promised and Floor are the voter's durable promise and floor;
	// KnownFloor (a controller) also what it heard (quorum.Adoption.KnownFloor).
	Promised   uint64 `json:"promised,omitempty"`
	Floor      uint64 `json:"floor,omitempty"`
	KnownFloor uint64 `json:"knownFloor,omitempty"`
	Spare      bool   `json:"spare,omitempty"`
	BoxChanged string `json:"boxChanged,omitempty"`
	// Settled: the voter started a lease and D ago (quorum.Voter.Settled).
	Settled bool `json:"settled"`
	// Err is why the member could not be asked, "" when it answered.
	Err string `json:"error,omitempty"`
}

// Highest is the highest term the member knows was used or promised.
func (m MemberState) Highest() uint64 { return max(m.Promised, m.Floor, m.KnownFloor) }

func voterState(v *quorum.Voter, now quorum.Instant, member string) MemberState {
	st := v.State()
	return MemberState{Member: member, Incarnation: st.Incarnation, Promised: st.Promised, Floor: st.Floor, Settled: v.Settled(now)}
}

// stateLocal is this controller's own answer to "state".
func (e *Elector) stateLocal() MemberState {
	e.mu.Lock()
	defer e.mu.Unlock()
	m := voterState(e.v, e.now(), string(e.o.Self))
	m.Commissioned = m.Incarnation != 0
	m.KnownFloor = e.a.KnownFloor()
	m.Spare = e.spare.Load()
	m.BoxChanged = e.boxChanged
	return m
}

// stateRemote asks the member at base for its state.
func (e *Elector) stateRemote(ctx context.Context, member, base string) MemberState {
	b, _ := json.Marshal(dupOp{Op: "state"})
	body, err := e.call.call(ctx, e.o.Lease, http.MethodPost, base+Prefix+e.o.Cluster+"/dup", b)
	if err != nil {
		var se *statusError
		msg := member + " is not reachable: " + err.Error()
		switch {
		case errors.As(err, &se) && (se.code == http.StatusNotFound || se.code == http.StatusBadRequest):
			msg = member + " runs an older release that can't answer this command: upgrade it first"
		case errors.As(err, &se) && se.code == http.StatusServiceUnavailable:
			msg = member + " is not answering (stopped, starting, or a spare): " + strings.TrimSpace(se.msg)
		}
		return MemberState{Member: member, Err: msg}
	}
	var m MemberState
	if err := json.Unmarshal(body, &m); err != nil {
		return MemberState{Member: member, Err: fmt.Sprintf("%s: unreadable answer: %v", member, err)}
	}
	m.Member = member
	return m
}

// membersState asks the three members in turn: this controller, its
// peer, the witness.
func (e *Elector) membersState(ctx context.Context) []MemberState {
	return []MemberState{
		e.stateLocal(),
		e.stateRemote(ctx, string(e.o.Peer), e.peer),
		e.stateRemote(ctx, "witness", e.wit),
	}
}

// ErrCommandRefused: a commissioning command refused; nothing was changed.
var ErrCommandRefused = errors.New("redundancy: refused")

// ErrBoxChanged: a re-commission of a controller whose state was written
// on another box, without the operator's word that the box was replaced.
var ErrBoxChanged = errors.New("quorum: this controller's state was written on another box")

// commissionMargin is how far above every term the members know a new
// pair's floor goes. Any margin works for a pair that has used no term;
// it must stay under MaxTermJump/2 (RecommissionBy refuses a floor more
// than that above what this controller knows), which 1000 is far below.
const commissionMargin = 1000

// CommissionReport is what commission did.
type CommissionReport struct {
	Cluster     string        `json:"cluster"`
	Members     []MemberState `json:"members"`
	Incarnation uint64        `json:"incarnation,omitempty"`
	Floor       uint64        `json:"floor,omitempty"`
	// VouchRefused is the operator-intent log's refusal of the vouch: the
	// pair is commissioned all the same, its log read-only until synced.
	VouchRefused string `json:"vouchRefused,omitempty"`
	// Leader is who leads once the pair settles ("" if none did within
	// the wait: it may still).
	Leader  string `json:"leader,omitempty"`
	Refused string `json:"refused,omitempty"`
}

// Commission commissions a new pair from this controller (#1029 3c-3c):
// every member answers and neither controller was commissioned
// (commissionCheck);
// this controller adopts the witness's incarnation with a floor above
// every term the members know, and its peer adopts it from here. by names
// the person (the log's vouch). It returns ErrCommandRefused when it
// changed nothing.
func (e *Elector) Commission(ctx context.Context, by string) (CommissionReport, error) {
	rep := CommissionReport{Cluster: e.o.Cluster}
	if e.Spare() {
		rep.Refused = "this controller is a spare (a copy of a member): run recommission --from-spare on it, or commission from the other controller"
		return rep, ErrCommandRefused
	}
	rep.Members = e.membersState(ctx)
	rep.Refused = commissionCheck(rep.Members)
	if rep.Refused != "" {
		return rep, ErrCommandRefused
	}
	var highest uint64
	for _, m := range rep.Members {
		highest = max(highest, m.Highest())
	}
	rep.Incarnation = rep.Members[2].Incarnation
	rep.Floor = highest + commissionMargin
	vr, err := e.recommission(ctx, by, rep.Incarnation, rep.Floor, false)
	if vr != nil {
		rep.VouchRefused = vr.Error()
	}
	if err != nil {
		rep.Refused = err.Error()
		return rep, ErrCommandRefused
	}
	rep.Leader = e.awaitLeader(ctx, 16*e.o.Lease)
	return rep, nil
}

// commissionCheck refuses a commission unless every member answered and
// settled, and neither controller is commissioned or holds a floor. Terms
// don't tell: a new pair's witness promises terms to the controllers'
// campaigns before any commissioning. Nor can the witness tell a new pair
// from one whose two controllers both lost their state; the floor then
// still goes above every term the three report (the residual is a term
// the two controllers' voters granted without the witness, lost with
// both files, which a re-commission's floor can't know either).
func commissionCheck(ms []MemberState) string {
	var why []string
	for _, m := range ms {
		switch {
		case m.Err != "":
			why = append(why, m.Err)
		case !m.Settled:
			why = append(why, m.Member+" started less than a lease and a quarter ago: try again shortly")
		case mut("commission_unchecked"):
		case m.Spare:
			why = append(why, m.Member+" is a spare")
		case m.Commissioned:
			why = append(why, fmt.Sprintf("%s is commissioned (incarnation %x): this is not a new pair; a controller that lost its state re-adopts from it, or is re-commissioned with recommission", m.Member, m.Incarnation))
		case m.BoxChanged != "":
			why = append(why, fmt.Sprintf("%s's state was written on another box (%s): this pair was commissioned before; if %s's box was replaced, re-commission it with recommission --box-replaced", m.Member, m.BoxChanged, m.Member))
		case m.Member != "witness" && m.Floor != 0:
			why = append(why, fmt.Sprintf("%s holds a floor (%d) from an earlier commission or re-commission: wait for that one to finish (naut redundancy status), or, if it was cut short (%s restarted before the pair adopted), run recommission --floor above %d on %s", m.Member, m.Floor, m.Member, m.Floor, m.Member))
		}
	}
	if len(why) > 0 {
		return "commission needs all three members up and new: " + strings.Join(why, "; ")
	}
	if ms[2].Incarnation == 0 {
		return "the witness has no incarnation for this pair yet: try again shortly"
	}
	return ""
}

// awaitLeader waits up to d for a leader, as this controller sees it.
func (e *Elector) awaitLeader(ctx context.Context, d time.Duration) string {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	for {
		if l := e.Status().Leader; l != "" {
			return l
		}
		select {
		case <-ctx.Done():
			return ""
		case <-time.After(e.o.Lease / 4):
		}
	}
}

// RecommissionRequest is the operator's re-commission.
type RecommissionRequest struct {
	// Incarnation is the witness's incarnation to adopt: 0 takes the one
	// the witness answers with; any other must equal it.
	Incarnation uint64 `json:"incarnation,omitempty"`
	// Floor: every term at or below it is never granted again. It must be
	// above every term a member that answered knows; 0 only reports.
	Floor uint64 `json:"floor,omitempty"`
	// BoxReplaced: the operator's word that this controller's box was
	// replaced (its state was written on another box), not cloned.
	BoxReplaced bool `json:"boxReplaced,omitempty"`
}

// RecommissionReport is what recommission did.
type RecommissionReport struct {
	Cluster string        `json:"cluster"`
	Members []MemberState `json:"members"`
	// Evidence is the highest term a member that answered knows, and
	// Suggested a floor above it.
	Evidence    uint64 `json:"evidence"`
	Suggested   uint64 `json:"suggested"`
	Incarnation uint64 `json:"incarnation,omitempty"`
	Floor       uint64 `json:"floor,omitempty"`
	BoxReplaced bool   `json:"boxReplaced,omitempty"`
	// Unheard names the members that did not answer: the terms they used
	// are not in Evidence.
	Unheard      []string `json:"unheard,omitempty"`
	VouchRefused string   `json:"vouchRefused,omitempty"`
	Refused      string   `json:"refused,omitempty"`
}

// RecommissionChecked is the operator's recommission (#1029 3c-3c): the
// members' terms are the floor's evidence (the floor must be above all of
// them), the witness must answer, and a box-changed controller needs
// req.BoxReplaced. It returns ErrCommandRefused when it changed nothing.
func (e *Elector) RecommissionChecked(ctx context.Context, by string, req RecommissionRequest) (RecommissionReport, error) {
	rep := RecommissionReport{Cluster: e.o.Cluster, BoxReplaced: req.BoxReplaced}
	refuse := func(format string, a ...any) (RecommissionReport, error) {
		rep.Refused = fmt.Sprintf(format, a...)
		return rep, ErrCommandRefused
	}
	if e.Spare() {
		return refuse("this controller is a spare (a copy of a member): recommission --from-spare turns it into the member")
	}
	rep.Members = e.membersState(ctx)
	self, wit := rep.Members[0], rep.Members[2]
	var who string
	rep.Evidence, who, rep.Unheard = floorEvidence(rep.Members)
	rep.Suggested = rep.Evidence + commissionMargin
	if why := recommissionRefusal(self, wit, rep.Evidence, who, rep.Unheard, req); why != "" {
		return refuse("%s", why)
	}
	rep.Incarnation, rep.Floor = wit.Incarnation, req.Floor
	vr, err := e.recommission(ctx, by, rep.Incarnation, rep.Floor, req.BoxReplaced)
	if vr != nil {
		rep.VouchRefused = vr.Error()
	}
	if err != nil {
		rep.Refused = err.Error()
		return rep, ErrCommandRefused
	}
	return rep, nil
}

// floorEvidence is the highest term any member that answered knows, who
// knows it, and which members did not answer. ms[0] is this controller.
func floorEvidence(ms []MemberState) (evidence uint64, who string, unheard []string) {
	for _, m := range ms {
		if m.Err != "" {
			unheard = append(unheard, m.Member)
			continue
		}
		if mut("recommission_local_floor_only") && m.Member != ms[0].Member {
			continue
		}
		if h := m.Highest(); h > evidence || who == "" {
			evidence, who = h, m.Member
		}
	}
	return evidence, who, unheard
}

// recommissionRefusal is why the operator's re-commission is refused, ""
// when it may go ahead: evidence (from who) is the highest term an
// answering member knows (floorEvidence), unheard the members that did
// not answer.
func recommissionRefusal(self, wit MemberState, evidence uint64, who string, unheard []string, req RecommissionRequest) string {
	suggested := evidence + commissionMargin
	switch {
	case self.Commissioned:
		return fmt.Sprintf("this controller is commissioned (incarnation %x): re-commissioning is for a controller that lost its state", self.Incarnation)
	case self.BoxChanged != "" && !req.BoxReplaced && !mut("box_replaced_unchecked"):
		return fmt.Sprintf("%v: %s. If this box replaced the controller's old one, say so with --box-replaced; if it is a copy of a running controller, stop it", ErrBoxChanged, self.BoxChanged)
	case wit.Err != "":
		return "the witness must answer: its promise is the floor's main evidence, and the pair can't lead without it: " + wit.Err
	case wit.Incarnation == 0:
		return "the witness has no incarnation for this pair yet: try again shortly"
	case req.Incarnation != 0 && req.Incarnation != wit.Incarnation:
		return fmt.Sprintf("the witness's incarnation for this pair is %x, not %x", wit.Incarnation, req.Incarnation)
	case req.Floor == 0:
		return fmt.Sprintf("give a floor above %d (the highest term %s knows), e.g. --floor %d%s", evidence, who, suggested, unheardNote(unheard))
	case req.Floor <= evidence:
		return fmt.Sprintf("floor %d is not above %d, the highest term %s knows: give one above it, e.g. --floor %d", req.Floor, evidence, who, suggested)
	}
	return ""
}

func unheardNote(unheard []string) string {
	if len(unheard) == 0 {
		return ""
	}
	return fmt.Sprintf(". %s did not answer, so the terms it used are not counted: the floor must be above any term it ever used", strings.Join(unheard, " and "))
}

// FromSpareReport is what recommission --from-spare did.
type FromSpareReport struct {
	Name string `json:"name"`
	// Check and Clear are the peer's and the witness's answers.
	Check []DupAnswer `json:"check"`
	Clear []DupAnswer `json:"clear,omitempty"`
	// Retired are the boots of the name the voters knew, the incumbent's
	// among them, which the clear set aside: one that comes back is marked
	// before any grant.
	Retired []quorum.BootSeq `json:"retired,omitempty"`
	// MovedAside is where this controller's state file went: restart naut
	// on this box, and it starts uncommissioned and re-adopts.
	MovedAside string `json:"movedAside,omitempty"`
	Refused    string `json:"refused,omitempty"`
}

// ErrFromSpareIncomplete: --from-spare cleared at one voter but not both;
// nothing was moved aside, and it can run again.
var ErrFromSpareIncomplete = errors.New("redundancy: from-spare incomplete")

// EventFromSpare: an operator turned this spare back into the member; its
// state file is moved aside until naut restarts (Detail says where).
const EventFromSpare EventKind = "redundancy.from-spare"

// FromSpare is recommission --from-spare (#1029 3c-3c), run on the spare:
// both other voters must answer and hear no process of its name within a
// lease and D (the one the voters held, the incumbent, is gone); then the
// name's marks are cleared there keeping no boot (every boot they knew is
// retired, the incumbent's too), and this controller's state file is
// moved aside. It stays silent until naut restarts on this box, which
// starts uncommissioned and re-adopts from the peer (#976). The spare's
// only messages are these /dup requests, which carry no boot. by names
// the person. ErrCommandRefused: nothing changed.
func (e *Elector) FromSpare(ctx context.Context, by string) (FromSpareReport, error) {
	name := string(e.o.Self)
	rep := FromSpareReport{Name: name}
	if !e.Spare() && !mut("from_spare_not_spare") {
		rep.Refused = "this controller is not a spare: --from-spare turns a spare into the member"
		return rep, ErrCommandRefused
	}
	if a := e.fromSpareAside(); a != "" {
		rep.MovedAside = a
		rep.Refused = "already done: this controller's state was moved aside to " + a + ". Restart naut on this box"
		return rep, ErrCommandRefused
	}
	// The peer must be a commissioned member: it is the one the restarted
	// spare re-adopts from, and with it uncommissioned too, this spare's
	// file may hold the only record of the pair's terms (#1157 review).
	if ps := e.stateRemote(ctx, string(e.o.Peer), e.peer); (ps.Err != "" || !ps.Commissioned) && !mut("from_spare_peer_unchecked") {
		why := ps.Err
		if why == "" {
			why = string(e.o.Peer) + " is not commissioned"
		}
		rep.Refused = "the other controller must be a commissioned member, for this box to re-adopt from it after its restart: " + why +
			". Bring it back first; if both controllers lost their state, recommission one of them"
		return rep, ErrCommandRefused
	}
	q := dupOp{Op: "check", Name: name, By: by}
	switch {
	case mut("from_spare_own_view"):
		rep.Check = []DupAnswer{e.dupLocal(q)}
	case mut("from_spare_one_voter"):
		rep.Check = []DupAnswer{e.dupRemote(ctx, "witness", e.wit, q)}
	default:
		rep.Check = []DupAnswer{e.dupRemote(ctx, string(e.o.Peer), e.peer, q), e.dupRemote(ctx, "witness", e.wit, q)}
	}
	retire, gen, refused := fromSpareCheck(name, rep.Check)
	rep.Retired = retire
	if refused != "" {
		rep.Refused = refused
		return rep, ErrCommandRefused
	}
	c := dupOp{Op: "clear", Name: name, By: by, Retire: retire, Gen: gen}
	// The peer first, the witness only if the peer cleared: a voter that
	// refuses heard a boot of the name since the check (the incumbent
	// back), and a clear at the other would mark that live process there
	// for nothing (#1157 review).
	for _, m := range []struct{ member, base string }{{string(e.o.Peer), e.peer}, {"witness", e.wit}} {
		a := e.dupRemote(ctx, m.member, m.base, c)
		rep.Clear = append(rep.Clear, a)
		if (a.Err != "" || a.Refused != "") && !mut("from_spare_clears_past_refusal") {
			break
		}
	}
	for _, a := range rep.Clear {
		if (a.Err != "" || a.Refused != "" || len(rep.Clear) < 2) && !mut("from_spare_moves_after_partial") {
			// Nothing is moved aside: the spare stays a spare, and running
			// the command again finishes the clear.
			return rep, ErrFromSpareIncomplete
		}
	}
	aside, err := e.file.MoveAside(fmt.Sprintf("%s.previous-spare-%d", e.o.StatePath, time.Now().Unix()))
	if err != nil {
		rep.Refused = "the voters cleared " + name + ", but this controller's state file could not be moved aside: " + err.Error() + ". Run this again"
		return rep, ErrFromSpareIncomplete
	}
	rep.MovedAside = aside
	msg := fmt.Sprintf("this spare is to become %s: its state file was moved aside to %s; restart naut on this box, and it re-adopts from %s", name, aside, e.o.Peer)
	e.mu.Lock()
	e.fromSpare = aside
	e.event(Event{Kind: EventFromSpare, Detail: msg})
	e.mu.Unlock()
	e.log.Warn("redundancy: "+msg, "by", by)
	return rep, nil
}

func (e *Elector) fromSpareAside() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.fromSpare
}

// fromSpareCheck decides --from-spare's check from the other voters'
// answers: both answered, settled, and neither hears a boot of the name.
// It returns the boots to retire (every boot either knew) and the clear's
// generation, above both voters'. Unlike the clear-duplicate command's
// check (checkPhase), it keeps no boot: the next boot of the name, the
// replacement after its restart, is the member (naut-lead's ruling on
// 3c-3c lifts #1135's "nothing to keep" refusal on this path only).
func fromSpareCheck(name string, check []DupAnswer) (retire []quorum.BootSeq, gen uint64, refused string) {
	var silent, hearing []string
	var known [][]quorum.BootSeq
	for _, a := range check {
		if a.Err != "" || a.Refused != "" {
			silent = append(silent, a.Err+a.Refused)
			continue
		}
		if len(a.Boots) > 0 {
			hearing = append(hearing, a.Member)
		}
		known = append(known, a.Known)
		gen = max(gen, a.Gen, a.HoldGen)
	}
	switch {
	case len(silent) > 0:
		return nil, 0, "both other voters must answer: " + strings.Join(silent, "; ")
	case len(hearing) > 0:
		return nil, 0, fmt.Sprintf("%s hear(s) a process named %s within the last lease and a quarter: the controller the voters count as %s still runs. Stop it, confirm it is down (powered off, or its process ended), and run this again a lease and a quarter later", strings.Join(hearing, " and "), name, name)
	}
	return quorum.UnionBoots(known...), gen + 1, ""
}
