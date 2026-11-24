package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"

	"github.com/joyautomation/nautilus/internal/peertls"
	"github.com/joyautomation/nautilus/quorum/node"
)

// The texts of `naut redundancy unpin` (#710 2b).
const (
	// unpinLiftedText is naut-lead's sentence for a second unpin, which
	// lifts the refusal of the old key.
	unpinLiftedText = "the next box to contact with the token is pinned: do this only with the real box ready to connect"
)

// unpinReport is `naut redundancy unpin <name>`'s answer: what each store
// of pins found (peertls.DropOutcome), whether a second unpin lifted the
// refusal of the old key, and one sentence for the operator.
type unpinReport struct {
	Name       string `json:"name"`
	Here       string `json:"here,omitempty"`
	Witness    string `json:"witness,omitempty"`
	WitnessErr string `json:"witnessErr,omitempty"`
	Lifted     bool   `json:"lifted,omitempty"`
	Message    string `json:"message"`
}

// Unpin is `naut redundancy unpin <name>` on the surviving controller
// (#710 2b).
//
// name the other controller: its box was replaced or its state directory
// lost, so its new key is refused wherever its old one is pinned. It drops
// the pin here and at the witness (over this controller's own pinned
// connection); the old key stays refused, in both pins files, so a box
// still holding it never comes back as name. Before the local drop it
// records, durably, that the witness's leg is pending: a retry (the
// witness not reached, or its answer lost) only finishes that leg, never
// drops here again (a replacement pinned meanwhile stays) and never lifts.
// A second unpin, with no leg pending and both stores answering that the
// pin is already dropped (or the witness never pinned it), lifts the
// refusal.
//
// name "witness": a new witness key (the witness box replaced, or its
// whole state directory lost) is refused here until this allows the next
// one, once; it goes on both controllers.
//
// outcome is "done", "refused" (nothing changed) or "incomplete" (the
// witness was not reached: run it again).
func (m *pairMember) Unpin(ctx context.Context, name, by string) (any, string, string) {
	rep := unpinReport{Name: name}
	refused := func(why string) (any, string, string) { rep.Message = why; return rep, "refused", why }
	pt := m.peerTLS
	switch {
	case pt == nil:
		return refused("peer TLS is off on this controller (redundancy.peer-tls: off): nothing is pinned, so there is nothing to unpin")
	case name == peertls.Witness:
		return m.allowWitness(by)
	case name == string(m.o.Self) && !mutNaut("unpin_self"):
		return refused(fmt.Sprintf("this controller is %s: run naut redundancy unpin %s on the other controller, the surviving one", name, name))
	case name != string(m.o.Peer) && name != string(m.o.Self):
		return refused(fmt.Sprintf("unpin takes the other controller (%s), or witness for a new witness box", m.o.Peer))
	}
	why := "naut redundancy unpin " + name + ", by " + nonEmpty(by, "a person")
	el := m.q.Node()
	incomplete := func(err error) (any, string, string) {
		rep.WitnessErr = err.Error()
		rep.Message = fmt.Sprintf("%s's pin here: %s; the witness was not reached (%v). Run naut redundancy unpin %s again once it answers: it finishes the witness's part, and never lifts the refusal here", name, rep.Here, err, name)
		return rep, "incomplete", rep.Message
	}
	if key := pt.UnpinPending(name); key != "" && !mutNaut("unpin_lifts_on_retry") {
		// A retry: finish an earlier unpin, about the key it dropped (a
		// replacement pinned here since stays): here, in case that run
		// stopped before its local drop, then at the witness.
		here := peertls.AlreadyDropped
		if !mutNaut("retry_skips_local_drop") {
			var err error
			if here, err = pt.DropKey("", name, why, key); err != nil {
				rep.Message = "the pin here could not be dropped: " + err.Error()
				return rep, "incomplete", rep.Message
			}
		}
		rep.Here = string(here)
		wa, err := el.WitnessUnpin(ctx, "drop", name, key)
		if err != nil {
			return incomplete(err)
		}
		if err := pt.SetUnpinPending(name, ""); err != nil {
			rep.Message = "the witness's part is done, but this controller could not record it: " + err.Error()
			return rep, "incomplete", rep.Message
		}
		rep.Witness = wa.Outcome
		rep.Message = fmt.Sprintf("the earlier unpin of %s (key %s) is finished: here %s, at the witness %s%s. That key stays refused here and there; start the replacement now", name, key, here, wa.Outcome, otherAt(name, wa))
		if cur := pt.Pinned("")[name]; cur != "" && cur != key {
			rep.Message += fmt.Sprintf(". %s is pinned here now to another key, %s (a replacement): left as it is; run naut redundancy unpin %s again to unpin that one too", name, cur, name)
		}
		return rep, "done", ""
	}
	key := pt.Pinned("")[name]
	if key == "" {
		key = pt.Refused("")[name]
	}
	if key == "" {
		rep.Here = string(peertls.NotPinned)
		rep.Message = fmt.Sprintf("nothing is pinned for %s here, and no old key of it is refused: its next contact is pinned here. If the witness refuses %s's new key, its stale pin is dropped on the witness box (naut witness unpin, with the witness stopped)", name, name)
		return rep, "done", ""
	}
	if pt.Pinned("")[name] != "" {
		if err := pt.SetUnpinPending(name, key); err != nil {
			rep.Message = "this controller could not record the unpin: " + err.Error()
			return rep, "incomplete", rep.Message
		}
	}
	here, err := pt.DropKey("", name, why, key)
	if err != nil {
		rep.Message = "the pin here could not be dropped: " + err.Error()
		return rep, "incomplete", rep.Message
	}
	rep.Here = string(here)
	if here == peertls.OtherKey {
		_ = pt.SetUnpinPending(name, "")
		rep.Message = fmt.Sprintf("another key was pinned for %s here while this ran: nothing was dropped; run it again if that key is not %s's", name, name)
		return rep, "refused", rep.Message
	}
	wa, werr := el.WitnessUnpin(ctx, "drop", name, key)
	rep.Witness = wa.Outcome
	if werr != nil {
		return incomplete(werr)
	}
	if here == peertls.Dropped {
		if err := pt.SetUnpinPending(name, ""); err != nil {
			rep.Message = "the witness dropped the pin, but this controller could not record it: " + err.Error()
			return rep, "incomplete", rep.Message
		}
	}
	if here == peertls.AlreadyDropped && (wa.Outcome == string(peertls.AlreadyDropped) || wa.Outcome == string(peertls.NotPinned)) {
		// A second unpin, both stores agreeing: lift the refusal of the old
		// key, at the witness first (a rerun after it failed still finds
		// the refusal here, and lifts again).
		if mutNaut("lift_here_first") {
			_, _ = pt.LiftKey("", name, why, key)
		}
		la, err := el.WitnessUnpin(ctx, "lift", name, key)
		if err != nil {
			rep.WitnessErr = err.Error()
			rep.Message = fmt.Sprintf("second unpin of %s: the witness was not reached (%v), so nothing was lifted: run naut redundancy unpin %s again once it answers", name, err, name)
			return rep, "incomplete", rep.Message
		}
		if _, err := pt.LiftKey("", name, why+" (second unpin)", key); err != nil {
			rep.Message = "the witness lifted its refusal, but the one here could not be lifted: " + err.Error() + "; run it again"
			return rep, "incomplete", rep.Message
		}
		rep.Lifted, rep.Witness = true, la.Outcome
		rep.Message = fmt.Sprintf("second unpin of %s: the refusal of its old key is lifted here and at the witness (%s). %s", name, la.Outcome, unpinLiftedText)
		return rep, "done", ""
	}
	rep.Message = fmt.Sprintf("%s's pin is dropped here (%s) and at the witness (%s)%s. Its old key stays refused, so a box still holding it never comes back as %s. Start the replacement now: its new key is pinned at its first contact; check that key's fingerprint against naut redundancy status on that box. (Unpinned by mistake, the same box? Run naut redundancy unpin %s again: a second unpin lifts the refusal.)", name, here, wa.Outcome, otherAt(name, wa), name, name)
	return rep, "done", ""
}

// otherAt is what an unpin's report adds when the witness holds another
// key for name than the one dropped here.
func otherAt(name string, wa node.UnpinAnswer) string {
	if wa.Outcome != node.UnpinOther {
		return ""
	}
	return "; the witness holds another key for " + name + " (" + nonEmpty(wa.Pinned, "a newer refusal") + ") and left it as it is: check it against naut redundancy status on " + name + "'s box, or drop it on the witness box (naut witness unpin)"
}

// allowWitness is `naut redundancy unpin witness`: this controller takes
// the next new witness key, once (#710 2b).
func (m *pairMember) allowWitness(by string) (any, string, string) {
	rep := unpinReport{Name: peertls.Witness}
	waiting, err := m.peerTLS.AllowWitnessKey("naut redundancy unpin witness, by " + nonEmpty(by, "a person"))
	if errors.Is(err, peertls.ErrNoWitnessWaiting) {
		rep.Message = err.Error()
		return rep, "refused", rep.Message
	}
	if err != nil {
		rep.Message = "the allowance could not be written: " + err.Error()
		return rep, "incomplete", rep.Message
	}
	rep.Here = "allowed"
	rep.Message = fmt.Sprintf("this controller takes the new witness key %s, once: it must be the key the witness prints at its start. Run naut redundancy unpin witness on the other controller too", waiting)
	return rep, "done", ""
}

// witnessUnpin is the witness's side of an unpin (#710 2b): a controller
// of the cluster, by its certificate (Bind put it in the request), asks
// to drop the other controller's pin (op "drop") or to lift the refusal
// of its dropped key (op "lift"). Only from a member pinned no later than
// the one it drops: after `unpin a`, a token holder who wins a's first
// contact must not unpin b, the survivor. bound is node.Witness.Bound.
func witnessUnpin(pt *peertls.Peer, bound func(cluster string) (string, bool)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")
		var ask node.UnpinAsk
		if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&ask); err != nil || (ask.Op != "drop" && ask.Op != "lift") {
			http.Error(w, `want {"op": "drop" or "lift", "name": "<the other controller>"}`, http.StatusBadRequest)
			return
		}
		c, from, ok := node.VerifiedPeer(r)
		if !ok || (c != cluster && !mutNaut("unpin_other_cluster")) {
			http.Error(w, "only a controller of this cluster, by its certificate, unpins here", http.StatusForbidden)
			return
		}
		members, ok := bound(cluster)
		ms := strings.Split(members, ",")
		switch {
		case !ok:
			http.Error(w, "this witness has no pair of that cluster", http.StatusNotFound)
			return
		case !slices.Contains(ms, from) && !mutNaut("unpin_other_cluster"):
			http.Error(w, "only a controller of this cluster, by its certificate, unpins here", http.StatusForbidden)
			return
		case ask.Name == from && !mutNaut("witness_unpin_self"):
			http.Error(w, fmt.Sprintf("controller %s can't unpin itself: run naut redundancy unpin %s on the other controller, the surviving one", from, from), http.StatusForbidden)
			return
		case !slices.Contains(ms, ask.Name):
			http.Error(w, fmt.Sprintf("%q is not a controller of this cluster (%s)", ask.Name, members), http.StatusForbidden)
			return
		}
		ec, _ := pt.PinEpoch(cluster, from)
		if et, known := pt.PinEpoch(cluster, ask.Name); known && ec > et && !mutNaut("unpin_newer_caller") {
			http.Error(w, fmt.Sprintf("controller %s's key was pinned here after an unpin, later than %s's: the witness takes an unpin only from a controller pinned no later than the one it drops (a token holder who won %s's first contact must not unpin %s). If %s really is the survivor (both boxes were replaced in turn), run naut witness unpin --cluster %s %s on the witness box, with the witness stopped", from, ask.Name, from, ask.Name, from, cluster, ask.Name), http.StatusForbidden)
			return
		}
		why := "naut redundancy unpin " + ask.Name + ", asked by controller " + from
		if ask.Key == "" {
			http.Error(w, "an unpin names the key it drops", http.StatusBadRequest)
			return
		}
		// Only on the asking controller's key, checked under the pins'
		// lock: one re-pinned here since (a replacement that reached the
		// witness first, an unpin on the witness box) stays.
		var out string
		if ask.Op == "drop" {
			o, err := pt.DropKey(cluster, ask.Name, why, ask.Key)
			if err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			out = string(o)
		} else {
			lifted, err := pt.LiftKey(cluster, ask.Name, why+" (second unpin)", ask.Key)
			if err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			out = map[bool]string{true: "lifted", false: "not-refused"}[lifted]
		}
		writeJSONTo(w, node.UnpinAnswer{Outcome: out, Pinned: pt.Pinned(cluster)[ask.Name]})
	})
}

func writeJSONTo(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

const witnessUnpinUsage = `naut witness unpin: drop a controller's pin at a stopped witness

Usage: naut witness unpin [--state dir] --cluster <id> <a|b>

The way out when the witness refuses naut redundancy unpin (both
controllers of a pair were replaced in turn, so the survivor's own key is
newer than the one it drops), or a pin at the witness must go while no
controller can ask. Run it on the witness box with the witness stopped:
that takes the third vote from EVERY pair this witness serves, so do it
while both controllers of each of those pairs are up and healthy, and
start the witness again straight after. Like naut redundancy unpin, it
keeps the old key refused; run it a second time to lift that refusal
(` + unpinLiftedText + `).

  --state dir     the witness's state directory (default $STATE_DIRECTORY)
  --cluster id    the pair's cluster ID (NAUTILUS_CLUSTER in its env files)
`

// witnessUnpinMain is `naut witness unpin`.
func witnessUnpinMain(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("witness unpin", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, witnessUnpinUsage) }
	stateDefault, _, _ := strings.Cut(getenv("STATE_DIRECTORY"), ":")
	state := fs.String("state", stateDefault, "")
	cluster := fs.String("cluster", "", "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 || (fs.Arg(0) != "a" && fs.Arg(0) != "b") || !node.ValidCluster(*cluster) || *state == "" {
		fmt.Fprint(stderr, witnessUnpinUsage)
		return 2
	}
	name := fs.Arg(0)
	free, err := node.LockWitnessDir(*state)
	if err != nil {
		fmt.Fprintf(stderr, "naut witness unpin: the witness is running (%v): stop it first, with both controllers of each pair it serves up and healthy, and start it again straight after\n", err)
		return 1
	}
	defer free()
	path := filepath.Join(*state, *cluster+"."+peerPinsFile)
	why := "naut witness unpin " + name + " (on the witness box)"
	out, err := peertls.DropFile(path, name, why)
	if err != nil {
		fmt.Fprintln(stderr, "naut witness unpin:", err)
		return 1
	}
	switch out {
	case peertls.Dropped:
		fmt.Fprintf(stdout, "dropped %s's pin for cluster %s; its old key stays refused. Start the witness again now: until it runs, the pairs it serves have no third vote. %s's new key is pinned at its first contact.\n", name, *cluster, name)
	case peertls.AlreadyDropped:
		if _, err := peertls.LiftFile(path, name, why+" (second unpin)"); err != nil {
			fmt.Fprintln(stderr, "naut witness unpin:", err)
			return 1
		}
		fmt.Fprintf(stdout, "second unpin of %s for cluster %s: the refusal of its old key is lifted. %s. Start the witness again now.\n", name, *cluster, unpinLiftedText)
	default:
		fmt.Fprintf(stdout, "nothing is pinned for %s of cluster %s at this witness. Start the witness again now.\n", name, *cluster)
	}
	return 0
}
