// identity.go is a Sparkplug command under a manifest's auth: block (#708
// PR 8b, #709): who it is taken to be, what it may write, and its journal.
//
// MQTT does not say who published a command: anyone who can publish to the
// broker can send an NCMD or a DCMD. So a command is not authenticated
// here. It is NAMED, for the journal, sparkplug:<host>: the primary host
// (sparkplug.primary-host) when the node waits on one, else the broker's
// host. That name is a label, and every journal entry says so. What a
// command may do is one set of grants, auth.sparkplug, the same for every
// command whatever its label: a command write needs sparkplug.writable to
// open the tag AND write on its area. Without auth.sparkplug every command
// write is refused. Node Control/Rebirth is not a write and is always
// honoured.
//
// The node does not keep a policy or a journal itself. `naut run` hands it
// the HTTP server's (SetIdentity, server.Surface("sparkplug")), only under
// an auth: block: the same areas and roles, the same recorder (refusals
// coalesced, entries kept while the journal cannot be written). Without
// one, nothing changes: every write the write gate lets through is
// applied, and nothing is journalled.
package sparkplug

import (
	"net/url"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/auth"
)

// Identity is what the node asks before a command write, and where it
// journals. *server.Surface (the HTTP server's, for source "sparkplug")
// is one.
type Identity interface {
	// May reports whether p may use perm ("write") on target (a tag or
	// member path, in its declared spelling).
	May(p auth.Principal, perm, target string) bool
	// RecordBatch journals entries durable together (one fsync): all of
	// one command's entries.
	RecordBatch([]audit.Entry)
}

// SetIdentity gives the node the auth: block's policy and the journal.
// Call it before Start. Nil (the default) gates commands with
// sparkplug.writable alone and journals nothing, as before, unless the node
// was built WithIdentityRequired.
func (n *Node) SetIdentity(id Identity) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.id = id
}

// WithIdentityRequired makes the node refuse every command write until
// SetIdentity gives it an Identity: what a project with an auth: block
// builds, so a node whose identity was never wired fails closed rather
// than writing what the auth: block would refuse.
func WithIdentityRequired() Option {
	return func(n *Node) { n.needID = true }
}

// identity is the node's Identity, and whether a command write may go on
// without one.
func (n *Node) identity() (Identity, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.id, n.id != nil || !n.needID
}

// Principal is who the node journals a command as: sparkplug:<the primary
// host's id, else the broker's host>. It is a label, not a credential.
func (n *Node) Principal() auth.Principal {
	if n.cfg.PrimaryHostID != "" {
		return auth.Sparkplug(n.cfg.PrimaryHostID)
	}
	if u, err := url.Parse(n.cfg.BrokerURL); err == nil && u.Hostname() != "" {
		return auth.Sparkplug(u.Hostname())
	}
	return auth.Sparkplug("broker")
}

// brokerFrom is the journal's "from" for a command: the broker's host and
// port, never the URL as configured (it may carry a user and password).
func (n *Node) brokerFrom() string {
	if u, err := url.Parse(n.cfg.BrokerURL); err == nil && u.Host != "" {
		return u.Host
	}
	return "broker"
}

// labelNote is what every Sparkplug journal entry says of its principal.
const labelNote = "a label, not a proof: anyone who can publish to the broker can send a Sparkplug command"

// authRefusal is the reason a command write the auth: block does not
// allow is refused. It names the fix.
const authRefusal = "the auth: block grants Sparkplug commands no write on this tag: give auth.sparkplug a role with write (operator, not viewer) whose areas cover it (sparkplug: operator), then restart naut run"

// noIdentityRefusal is the reason every command write is refused by a node
// that needs an Identity and was given none.
const noIdentityRefusal = "the auth: block's policy is not wired to the Sparkplug node, so every command write is refused"

// cmdJournal collects one command's journal entries (#709): one per
// metric, or per template member, written or refused. They are recorded
// together, as one batch, when the command has been applied. A nil
// cmdJournal (no Identity) records nothing.
type cmdJournal struct {
	id      Identity
	who     auth.Principal
	from    string
	device  string
	entries []audit.Entry
}

// newCmdJournal is the journal for one command to device ("" = node);
// nil without an Identity.
func (n *Node) newCmdJournal(id Identity, device string) *cmdJournal {
	if id == nil {
		return nil
	}
	return &cmdJournal{id: id, who: n.Principal(), from: n.brokerFrom(), device: deviceLabel(device)}
}

// entry is one of the command's entries.
func (j *cmdJournal) entry(result, target string) audit.Entry {
	return audit.Entry{User: j.who.String(), Action: "tag.write", Target: target, From: j.from, Result: result,
		Detail: map[string]any{"device": j.device, "label": labelNote}}
}

// refused records a refused write of target. A refusal by the auth:
// block is marked class "auth": the recorder coalesces those per target,
// apart from the write gate's, so a host retrying one closed tag every
// scan cannot hide an auth refusal on another in a coalesced summary.
func (j *cmdJournal) refused(target, reason string) {
	if j == nil {
		return
	}
	e := j.entry(audit.ResultRefused, target)
	e.Detail["reason"] = reason
	if reason == authRefusal || reason == noIdentityRefusal {
		e.Detail["class"] = audit.ClassAuth
	}
	j.entries = append(j.entries, e)
}

// wrote records a write of target that landed: its value before and after.
func (j *cmdJournal) wrote(target string, before, after any) {
	if j == nil {
		return
	}
	e := j.entry(audit.ResultOK, target)
	e.Before, e.After = before, after
	j.entries = append(j.entries, e)
}

// flush records the command's entries as one batch.
func (j *cmdJournal) flush() {
	if j == nil || len(j.entries) == 0 {
		return
	}
	j.id.RecordBatch(j.entries)
}

// mayWrite reports whether a command may write the tag or member at p as
// far as the auth: block goes, judged on its declared spelling, as the
// write gate judges it (canonicalPath): an area folds names with
// strings.ToLower and the store with ir.NameKey, so the host's spelling
// must not decide which area a tag is in. ok is false when the node
// needs an Identity and has none.
func (n *Node) mayWrite(id Identity, ok bool, p string) (bool, string) {
	if !ok {
		return false, noIdentityRefusal
	}
	if id == nil {
		return true, ""
	}
	if id.May(n.Principal(), auth.Write, n.canonicalPath(p)) {
		return true, ""
	}
	return false, authRefusal
}
