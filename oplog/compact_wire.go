//go:build !js

package oplog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Compaction on the wire (#1455 PR 2a-2, docs/design/oplog-compaction.md
// §5-§6): what a copy says of its compaction (its keep-rules version, its
// clock, its base), the 409 below a base, base pages (base=1), and the
// install that refills a copy behind the base, pushed by the leader
// (replicateReq.Install: the witness never pulls) or pulled (CatchUp,
// leaderCatchUp). Not in the browser playground's build: its node is
// standalone and never sees a base (compact_wire_js.go).

// wireBase is a base as the wire names it.
type wireBase struct {
	Through     uint64 `json:"through"`
	ThroughTerm uint64 `json:"throughTerm"`
	Compact     uint64 `json:"compact"`
	Digest      string `json:"digest"`
	Kept        int    `json:"kept"`
	Rules       int    `json:"rules"`
}

func (b baseState) wire() wireBase {
	return wireBase{Through: b.Through, ThroughTerm: b.ThroughTerm, Compact: b.Compact, Digest: b.Digest, Kept: len(b.Kept), Rules: b.Rules}
}

// compactWire is what afterResp and replicateResp carry of compaction.
// Every field is omitted when unset, so a copy with compaction off (no
// rules version) and no base answers exactly as a build from before.
type compactWire struct {
	// CompactRules is the answering copy's keep-rules version
	// (Options.CompactRules); Now its clock (unix ms), for the decision's
	// T (§3). Both only once the rules version is set.
	CompactRules int   `json:"compactRules,omitempty"`
	Now          int64 `json:"now,omitempty"`
	// Base is its base, once it has one.
	Base *wireBase `json:"base,omitempty"`
	// Staged: (an install's answer, or a refusal's) the last kept seq the
	// copy has staged of the base being installed; the sender goes on
	// after it.
	Staged uint64 `json:"staged,omitempty"`
	// BasePage: a base page (base=1): Entries are kept entries of the base
	// after After, never the log; K, on its last page, is the compaction
	// that made the base.
	BasePage bool   `json:"basePage,omitempty"`
	After    uint64 `json:"after,omitempty"`
	K        *Entry `json:"k,omitempty"`
}

// installReq is one page of a pushed install: the kept entries of the
// sender's base after After, and, on its last page, the K that made it.
type installReq struct {
	wireBase
	After   uint64  `json:"after"`
	Entries []Entry `json:"entries"`
	K       *Entry  `json:"k,omitempty"`
}

// staging is the one install a copy is staging (§5): bound to who sends
// it, at what term and history, and which base (K's seq and digest). Any
// other install replaces it only from a newer term, or as a push at the
// same term; a restart (memory only) or a stop drops it.
type staging struct {
	from   string
	term   uint64
	lin    Lineage
	hdr    wireBase
	es     []stored
	last   uint64 // the last kept seq staged
	pushed bool   // a push has fed it
}

func (s *staging) is(from string, term uint64, lin Lineage, h wireBase) bool {
	return s.from == from && s.term == term && s.lin.same(lin) && s.hdr == h
}

// wireNode is the Node's state for the wire's compaction.
type wireNode struct {
	staging *staging
	// behind: the peers that are behind this copy's base and run a build
	// that can't take an install (no rules version): never resent to,
	// named by oplog-peer-behind-base.
	behind map[string]string
	// peerWire is each peer's last word on compaction, for the decision's
	// gate (PR 2b): its rules, clock, base and commit, and when.
	peerWire map[string]peerWire
	// blocked is why a wanted compaction waits, since when (PR 2b's
	// decision sets it: setCompactBlocked).
	blocked      string
	blockedSince time.Time
	decideState  // the decision (compact_decide.go)
}

// peerWire is what a peer last said of compaction.
type peerWire struct {
	rules  int
	now    int64
	base   wireBase
	commit uint64
	at     time.Time
}

// wireStatus is what Status says of the wire's compaction.
type wireStatus struct {
	// BehindBase: copies behind this one's base on a build from before
	// compaction (oplog-peer-behind-base), each "<copy>: <why>".
	BehindBase []string `json:"behindBase,omitempty"`
	// CompactBlocked: why a wanted compaction waits, since
	// CompactBlockedSince (unix ms): oplog-compaction-blocked after 7 days.
	CompactBlocked      string `json:"compactBlocked,omitempty"`
	CompactBlockedSince int64  `json:"compactBlockedSince,omitempty"`
	// CompactDecisionRefused: why the keep decision's check before
	// proposing refused (oplog-compaction-refused): a bug, with evidence.
	CompactDecisionRefused string `json:"compactDecisionRefused,omitempty"`
}

var (
	// errInstallRefused: an install this copy must not take (it holds
	// committed entries, or the base does not check out). The sender
	// stops; nothing of it was applied.
	errInstallRefused = errors.New("oplog: install refused")
	// errInstallBusy: another install, from a newer term or a push, is
	// staged here.
	errInstallBusy = errors.New("oplog: another install is being staged")
)

// wireLocked is what this copy's answers say of compaction. Caller holds
// mu.
func (n *Node) wireLocked() compactWire {
	var w compactWire
	if r := n.o.CompactRules; r > 0 || mutOplog("rules_advertised_unwired") {
		w.CompactRules = max(r, 1)
		w.Now = n.o.Now().UnixMilli()
	}
	if c := n.cut; c.Compact > 0 {
		b := c.wire()
		w.Base = &b
	}
	if s := n.staging; s != nil {
		w.Staged = s.last
	}
	return w
}

// with409 adds to a 409's body what a sender needs of compaction: this
// copy's rules version (a wiped copy's first answer is a gap: without it,
// it would look like a build from before) and how far an install is
// staged. Caller holds mu.
func (n *Node) with409(body map[string]any) map[string]any {
	w := n.wireLocked()
	if w.CompactRules > 0 && !mutOplog("rules_not_in_409") {
		body["compactRules"], body["now"] = w.CompactRules, w.Now
	}
	if w.Staged > 0 {
		body["staged"] = w.Staged
	}
	return body
}

// notePeerWireLocked records a peer's word on compaction, and that it is
// no longer behind this copy's base once it holds it. Caller holds mu.
func (n *Node) notePeerWireLocked(peer string, w compactWire, commit, have uint64) {
	if n.peerWire == nil {
		n.peerWire = map[string]peerWire{}
	}
	pw := peerWire{rules: w.CompactRules, now: w.Now, commit: commit, at: n.o.Now()}
	if w.Base != nil {
		pw.base = *w.Base
	}
	n.peerWire[peer] = pw
	if have >= n.cut.Through || commit >= n.cut.Through {
		delete(n.behind, peer)
	}
}

// wireStatusLocked fills Status's compaction wire fields. Caller holds mu.
func (n *Node) wireStatusLocked(s *Status) {
	for p, why := range n.behind {
		s.BehindBase = append(s.BehindBase, n.name(p)+": "+why)
	}
	sort.Strings(s.BehindBase)
	if n.blocked != "" {
		s.CompactBlocked, s.CompactBlockedSince = n.blocked, n.blockedSince.UnixMilli()
	}
	s.CompactDecisionRefused = n.decideRefused
}

// setCompactBlocked records why a wanted compaction waits ("" once it does
// not), from when it first waited: the decision's seam (PR 2b), which
// derives that time from the log so a restart does not reset it.
func (n *Node) setCompactBlocked(why string, since time.Time) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.blocked, n.blockedSince = why, since
	if why == "" {
		n.blockedSince = time.Time{}
	}
}

// basePageLocked answers a read after a seq below the base: with base=1,
// a page of the base's kept entries after it (resp), taken from the base
// (cut.Kept), never from the entries in memory, which stay dense until the
// file is rewritten; false: a 409. Caller holds mu.
func (n *Node) basePageLocked(resp *afterResp, after uint64, base bool) bool {
	c := n.cut
	if !base || c.Compact == 0 {
		return false
	}
	at := func(seq uint64) (stored, bool) {
		i := sort.Search(len(n.entries), func(i int) bool { return n.entries[i].Seq >= seq })
		if i < len(n.entries) && n.entries[i].Seq == seq {
			return n.entries[i], true
		}
		return stored{}, false
	}
	resp.BasePage, resp.After, resp.More, resp.Entries = true, after, true, nil
	if mutOplog("base_page_from_entries") {
		for _, s := range n.entries {
			if s.Seq > after && s.Seq <= c.Through {
				resp.Entries = append(resp.Entries, s.entry())
			}
		}
	} else {
		size := 0
		for k := sort.Search(len(c.Kept), func(i int) bool { return c.Kept[i] > after }); k < len(c.Kept); k++ {
			s, ok := at(c.Kept[k])
			if !ok {
				return false // can't be: the base's entries are held
			}
			size += len(s.raw) + 1
			if len(resp.Entries) > 0 && (len(resp.Entries) >= n.o.lim.chunkEntries || size > n.o.lim.chunkBytes) {
				return true
			}
			resp.Entries = append(resp.Entries, s.entry())
		}
	}
	if k, ok := at(c.Compact); ok {
		e := k.entry()
		resp.K = &e
		return true
	}
	// An installed base whose tail has not come yet: the K that came with
	// the install (kept in the header), so this copy serves its base too.
	if c.K == nil || mutOplog("installed_k_not_kept") {
		return false
	}
	e := *c.K
	resp.K = &e
	return true
}

// installPageLocked is the push install's page after staged. Caller holds
// mu.
func (n *Node) installPageLocked(staged uint64) *installReq {
	var resp afterResp
	if !n.basePageLocked(&resp, staged, true) {
		return nil
	}
	return &installReq{wireBase: n.cut.wire(), After: staged, Entries: resp.Entries, K: resp.K}
}

// installStep pushes one page of an install to a peer whose answer (resp)
// says it is behind this copy's base, or, when the peer can't take one (it
// advertised no rules version: a build from before compaction), records
// it as behind and stops: never a resend loop. stop: replicateTo returns
// resp and err.
func (n *Node) installStep(ctx context.Context, peer string, resp replicateResp, send func([]Entry, *installReq) (replicateResp, error)) (replicateResp, error, bool) {
	n.mu.Lock()
	through := n.cut.Through
	if resp.CompactRules < 1 && !mutOplog("install_to_old_build") {
		if n.behind == nil {
			n.behind = map[string]string{}
		}
		if _, ok := n.behind[peer]; !ok {
			n.log.Error("oplog: a copy is behind the compacted log and runs a build from before compaction: update it", "peer", peer, "have", resp.Have, "base", through)
		}
		n.behind[peer] = fmt.Sprintf("it runs a build from before log compaction and missed the compacted part (it has %d, the base is at %d): update it", resp.Have, through)
		n.mu.Unlock()
		return resp, fmt.Errorf("%w: it has %d, the base is at %d, and it can't take an install", errBehindBase, resp.Have, through), true
	}
	in := n.installPageLocked(resp.Staged)
	n.mu.Unlock()
	if in == nil || mutOplog("install_not_pushed") {
		return resp, fmt.Errorf("%w: it has %d, the base is at %d", errBehindBase, resp.Have, through), true
	}
	r, err := send(nil, in)
	var he *httpError
	if errors.As(err, &he) && strings.Contains(he.body, `"install"`) && strings.Contains(he.body, "install refused") && !mutOplog("install_refusal_silent") {
		// It can't take this base (it holds committed entries behind it, or
		// the base does not check out there): named, never resent to in a
		// loop. A person looks at that copy.
		var why struct {
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal([]byte(he.body), &why)
		n.mu.Lock()
		if n.behind == nil {
			n.behind = map[string]string{}
		}
		if _, ok := n.behind[peer]; !ok {
			n.log.Error("oplog: a copy behind the compacted log refuses its install", "peer", peer, "why", why.Detail)
		}
		n.behind[peer] = fmt.Sprintf("it is behind the compacted log (it has %d, the base is at %d) and refuses its install: %s", r.Have, through, why.Detail)
		n.mu.Unlock()
		return r, err, true
	}
	return r, err, false
}

// takeInstallLocked takes one pushed install page (the replicate handler,
// once the sender's term and history are checked) and answers it. Caller
// holds mu; it unlocks.
func (n *Node) takeInstallLocked(w http.ResponseWriter, req *replicateReq) {
	in := req.Install
	installed, err := n.stageLocked(req.From, req.Term, req.Lineage, true, in.wireBase, in.After, in.Entries, in.K)
	if l := n.leaderAddr(); l != "" {
		n.lastContact[l] = n.o.Now()
	}
	// Only what was proved (#1472): an install page proves nothing of
	// what this copy holds past its commit index (an old term's suffix
	// may be there), and the leader counts Have as holding its entries;
	// once installed, the base, which the install checked against K.
	have := n.commit
	if mutOplog("install_have_unproved") {
		have = n.lastLocked()
	}
	if installed {
		have = in.Through
	}
	commit := n.commit
	if err != nil {
		body := n.with409(map[string]any{"error": "install", "detail": err.Error(), "have": have, "commit": commit})
		n.mu.Unlock()
		writeJSON(w, http.StatusConflict, body)
		return
	}
	n.leaderHeard.Store(n.o.Now().UnixMilli())
	resp := replicateResp{Have: have, Commit: commit, Unsynced: !n.syncedLocked(), Identity: n.mark.Identity, Lineage: n.mark.Lineage, compactWire: n.wireLocked()}
	n.mu.Unlock()
	writeJSON(w, http.StatusOK, resp)
}

// basePage: p is a base page (a pull install's).
func (n *Node) basePage(p afterResp) bool { return p.BasePage }

// takeBasePageLocked stages a pulled base page from peer. Caller holds mu.
func (n *Node) takeBasePageLocked(peer string, p afterResp) error {
	if p.Base == nil {
		return fmt.Errorf("%w: %s's base page names no base", errInstallRefused, n.name(peer))
	}
	_, err := n.stageLocked(peer, p.Term, p.Lineage, false, *p.Base, p.After, p.Entries, p.K)
	return err
}

// stageLocked stages one page of an install (pushed or pulled), idempotent
// by position: a page that does not start where the staging is (a
// duplicate, a late or reordered one) is ignored, and the answer's Staged
// says where to go on. On its last page (k, the K that made the base) the
// base is checked and installed. A page after the install is harmless.
// Caller holds mu.
func (n *Node) stageLocked(from string, term uint64, lin Lineage, push bool, h wireBase, after uint64, es []Entry, k *Entry) (installed bool, err error) {
	c := n.cut
	switch {
	case n.stoppedLocked():
		return false, n.stopErrLocked()
	case c.Compact == h.Compact && c.Digest == h.Digest && n.commit >= h.Through:
		n.staging = nil
		return true, nil // installed already (or compacted alike): a late page
	case (n.commit > 0 || c.Through > 0) && !mutOplog("install_onto_applied"):
		// Only a copy with nothing applied is ever behind the base (§5): one
		// with committed entries would have them replaced under its
		// consumers.
		return false, fmt.Errorf("%w: this copy holds committed entries (commit %d, base %d)", errInstallRefused, n.commit, c.Through)
	case h.Through == 0 || h.ThroughTerm == 0 || h.Compact <= h.Through || h.Kept < 0:
		return false, fmt.Errorf("%w: the base (through %d at term %d, compact %d) is not one", errInstallRefused, h.Through, h.ThroughTerm, h.Compact)
	}
	s := n.staging
	if s == nil || !s.is(from, term, lin, h) {
		// The newer term wins, the push on a tie; a staging below the term
		// this copy now runs at is stale (its sender's leadership is over:
		// a copy that took the lease after a push began pulls at the old
		// term of a copy no leader has replicated to yet).
		stale := s != nil && s.term < max(n.term, n.currentTerm()) && !mutOplog("staging_stale_kept")
		if s != nil && !stale && (s.term > term || s.term == term && s.pushed && !push) {
			return false, fmt.Errorf("%w (from %s at term %d)", errInstallBusy, n.name(s.from), s.term)
		}
		s = &staging{from: from, term: term, lin: lin, hdr: h}
		n.staging = s
	}
	s.pushed = s.pushed || push
	if after != s.last && !mutOplog("install_pages_unordered") {
		return false, nil // not the next page: Staged says which is
	}
	for _, e := range es {
		if e.Seq <= s.last || e.Seq > h.Through || len(s.es) >= h.Kept {
			n.staging = nil
			return false, fmt.Errorf("%w: its page holds seq %d, out of the base's order", errInstallRefused, e.Seq)
		}
		s.es = append(s.es, storedOf(e))
		s.last = e.Seq
	}
	if k == nil {
		return false, nil
	}
	err = n.installLocked(s, *k)
	n.staging = nil
	return err == nil, err
}

// installLocked checks a staged base against the K that made it, and
// installs it: the file rewritten to it, the copy committed to Through,
// the base applied as one range. Have and Synced are not changed here:
// the answer proves Through; Synced comes from a synced leader's
// replication, as ever. Caller holds mu.
func (n *Node) installLocked(s *staging, k Entry) error {
	h := s.hdr
	refuse := func(f string, a ...any) error {
		err := fmt.Errorf("%w: "+f, append([]any{errInstallRefused}, a...)...)
		n.log.Error("oplog: an install does not check out; nothing of it is applied", "from", s.from, "error", err)
		return err
	}
	var b compactBody
	switch {
	case k.Kind != KindCompact || k.Seq != h.Compact:
		return refuse("its compaction is a %q entry at seq %d, the base names %d", k.Kind, k.Seq, h.Compact)
	case json.Unmarshal(k.Value, &b) != nil || b.V != 1:
		return refuse("its compaction's body can't be read")
	case (b.Through != h.Through || b.ThroughTerm != h.ThroughTerm || b.Kept != h.Kept || b.Digest != h.Digest || b.Rules != h.Rules) && !mutOplog("install_k_unchecked"):
		return refuse("the base (through %d, %d kept, digest %.12s) is not the one its compaction made (through %d, %d kept, digest %.12s)", h.Through, h.Kept, h.Digest, b.Through, b.Kept, b.Digest)
	case len(s.es) != h.Kept:
		return refuse("%d kept entries came, the base keeps %d", len(s.es), h.Kept)
	}
	seqs := make([]uint64, len(s.es))
	terms := make(map[uint64]uint64, len(s.es))
	for i, e := range s.es {
		seqs[i], terms[e.Seq] = e.Seq, e.Term
	}
	if dg := pairDigest(seqs, func(q uint64) uint64 { return terms[q] }); dg != b.Digest && !mutOplog("install_digest_unchecked") {
		return refuse("the kept entries' digest %.12s is not its compaction's %.12s", dg, b.Digest)
	}
	if t, ok := terms[h.Through]; ok && t != h.ThroughTerm {
		return refuse("the entry at Through %d is at term %d, the base names %d", h.Through, t, h.ThroughTerm)
	}
	if n.o.Mark != "" && !n.mark.Held {
		// Held before the base is on disk, as acceptLocked: a log lost after
		// this is seen.
		m := n.mark
		m.Held = true
		if err := n.setMarkLocked(m); err != nil {
			return fmt.Errorf("oplog: the log's mark: %w", err)
		}
	}
	if n.crashAt("install-held") {
		return errCrashed
	}
	before := struct {
		es            []stored
		cut, disk     baseState
		commit, floor uint64
		term          uint64
	}{n.entries, n.cut, n.disk, n.commit, n.floor, n.term}
	kk := k
	base := baseState{Through: h.Through, ThroughTerm: h.ThroughTerm, Compact: h.Compact, Kept: seqs, Digest: h.Digest, Rules: h.Rules, K: &kk}
	n.entries, n.cut, n.disk, n.commit, n.floor = s.es, base, base, h.Through, h.Through
	if err := n.rewrite(); err != nil {
		if !errors.Is(err, errRewritten) {
			n.entries, n.cut, n.disk, n.commit, n.floor, n.term = before.es, before.cut, before.disk, before.commit, before.floor, before.term
			return fmt.Errorf("oplog: writing the installed base: %w", err)
		}
		// The new file is in place (it holds the base) but can't be
		// reopened: memory holds what it does, applied, and nothing more
		// can be written.
		n.baseLocked(h.Through)
		n.applyRange(n.entries, h.Through)
		return fmt.Errorf("oplog: writing the installed base: %w", err)
	}
	for _, e := range s.es {
		if err := n.raiseTermLocked(e.Term); err != nil {
			n.log.Warn("oplog: recording the installed base's term", "error", err)
		}
	}
	if n.crashAt("install-written") {
		return errCrashed
	}
	if n.o.Mark != "" {
		m := n.mark
		m.Base = &MarkBase{Through: h.Through, Digest: h.Digest}
		if err := n.setMarkLocked(m); err != nil {
			n.log.Warn("oplog: can't record the installed base in the log's mark", "error", err)
		}
	}
	n.baseLocked(h.Through)
	n.applyRange(n.entries, h.Through)
	n.log.Info("oplog: installed the compacted base", "from", s.from, "through", h.Through, "kept", h.Kept)
	return nil
}

// fetchFirst is fetchPage for the first page of a read, which may meet a
// base (409 compacted): when this copy holds the entry at the base's
// Through, at its term, it reads on from there (its log is the same up to
// it); else it reads the base's pages (an install, base=1).
func (n *Node) fetchFirst(ctx context.Context, peer string, after uint64) (afterResp, error) {
	got, err := n.fetchPage(ctx, peer, after, false)
	var he *httpError
	if err == nil || !errors.As(err, &he) || he.code != http.StatusConflict || !strings.Contains(he.body, `"compacted"`) {
		return got, err
	}
	var c struct {
		Through     uint64 `json:"through"`
		ThroughTerm uint64 `json:"throughTerm"`
	}
	_ = json.Unmarshal([]byte(he.body), &c)
	n.mu.Lock()
	holds := c.Through > after && c.ThroughTerm != 0 && n.termAtLocked(c.Through) == c.ThroughTerm
	n.mu.Unlock()
	if holds && !mutOplog("reask_from_through_off") {
		return n.fetchPage(ctx, peer, c.Through, false)
	}
	if mutOplog("compacted_read_fails") {
		return got, err
	}
	return n.fetchPage(ctx, peer, after, true)
}

// nextPage is where a page-by-page read goes on after p: a base page's
// next (after its last entry), the tail after the base's last page (after
// Through), or the log's next page; done when p ends the log.
func (n *Node) nextPage(p afterResp) (after uint64, base, done bool) {
	switch {
	case p.BasePage && p.K != nil && p.Base != nil:
		return p.Base.Through, false, false
	case p.BasePage && len(p.Entries) > 0:
		return p.Entries[len(p.Entries)-1].Seq, true, false
	case p.BasePage:
		return 0, false, true // neither entries nor its K: not a page
	case p.More && len(p.Entries) > 0:
		return p.Entries[len(p.Entries)-1].Seq, false, false
	}
	return 0, false, true
}

// pageFollows: got, read after after, is the next page of the log first
// began: the same term, history and base, starting where the last ended.
func (n *Node) pageFollows(first, got afterResp, after uint64, base bool) bool {
	if got.Term != first.Term || !got.Lineage.same(first.Lineage) || !sameBase(first, got) || got.BasePage != base {
		return false
	}
	if len(got.Entries) == 0 {
		return true
	}
	if base {
		return got.Entries[0].Seq > after
	}
	return got.Entries[0].Seq == after+1
}

// sameBase: two answers name the same base (or none).
func sameBase(a, b afterResp) bool {
	switch {
	case a.Base == nil || b.Base == nil:
		return a.Base == nil && b.Base == nil || mutOplog("leader_catchup_pages_unchecked")
	}
	return *a.Base == *b.Base || mutOplog("leader_catchup_pages_unchecked")
}
