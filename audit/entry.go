// Package audit is the runtime audit journal (#709): who did what, when,
// from where, and whether it was done. Every action that changes something
// on a controller, accepted or refused, is one Entry.
//
// The journal's durable form is an append-only JSONL file per day
// (FileJournal): each entry is fsynced before the action is answered, and
// each line carries the SHA-256 of the line before it, so an edited,
// removed, inserted or reordered line, or a missing file, breaks the chain
// that Verify walks. A Ring keeps the recent entries in memory for a view
// on a box with no file.
//
// The package imports only the standard library; the server and the other
// surfaces hand it entries. Design: docs/design/identity-audit.md §4.
package audit

import "time"

// Entry is one audit record. Its shape is the one docs/design/redundancy.md
// §7.1 fixed for #709, plus From, Session and Prev.
type Entry struct {
	// ID is a ULID: unique, sortable by time, and the replay key when an
	// entry is copied (to Postgres, or through oplog).
	ID string `json:"id"`
	// TS is when the action was decided, UTC.
	TS time.Time `json:"ts"`
	// User is the principal: "anonymous", "token", "token:<name>",
	// "user:<name>", or "system".
	User string `json:"user"`
	// Source is the surface the action came through (Source* constants).
	Source string `json:"source"`
	// Action is dotted: "tag.write", "force.set", "alarm.ack", …
	Action string `json:"action"`
	// Target is what it acted on: a tag, an alarm id, a program, …
	Target string `json:"target,omitempty"`
	// Before and After are the target's value or state around the action.
	Before any `json:"before,omitempty"`
	After  any `json:"after,omitempty"`
	// Result is ResultOK, ResultRefused or ResultError.
	Result string `json:"result"`
	// Detail holds what is particular to the action: a refusal's reason,
	// an area, a takeover's terms.
	Detail map[string]any `json:"detail,omitempty"`
	// From is where the request came from: a remote address, or an OPC UA
	// client certificate's thumbprint.
	From string `json:"from,omitempty"`
	// Session is a hash of the session id, never the id itself.
	Session string `json:"session,omitempty"`
	// Prev is the chain: "sha256:<hex>" of the previous line's bytes in
	// this controller's journal, empty on the very first line. The file
	// journal sets it; it is not part of an entry's identity (ID is), so
	// it is not carried when an entry is replicated.
	Prev string `json:"prev,omitempty"`
}

// Results.
const (
	ResultOK      = "ok"
	ResultRefused = "refused"
	ResultError   = "error"
)

// Sources: the surface an action came through.
const (
	SourceHMI       = "hmi"
	SourceAPI       = "api"
	SourceOPCUA     = "opcua"
	SourceSparkplug = "sparkplug"
	SourceCLI       = "cli"
	SourceIDE       = "ide"
	SourceSystem    = "system"
)

// Recorder takes entries. Record fills ID and TS when empty and returns
// the entry as recorded; an error means it was not recorded.
type Recorder interface {
	Record(Entry) (Entry, error)
}

// fill sets an entry's ID and TS when the caller left them empty.
func fill(e Entry, now time.Time) Entry {
	if e.TS.IsZero() {
		e.TS = now
	}
	e.TS = e.TS.UTC()
	if e.ID == "" {
		e.ID = NewID(e.TS)
	}
	return e
}
