package webapi

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// The CPU's diagnostic buffer, read with DiagnosticBuffer.Browse (Web
// Server function manual 11/2023 §5.9.5; FW ≥ 3.1 on standard, T- and F-
// CPUs, not on R/H CPUs). The user needs the read_diagnostics permission,
// which TIA grants as the web right "diagnostics" (WebAllowDiagnostics).
//
// Not sent: the request's filters object. The manual's table names it
// "filters" and its example "entries", so until a bench read settles the
// key, every read asks for all three texts (the CPU's default).

// DiagStatus is "incoming" for an event that came, "outgoing" for one that
// went.
const (
	DiagIncoming = "incoming"
	DiagOutgoing = "outgoing"
)

// DiagEvent is an entry's event ID: the text list and the text in it.
type DiagEvent struct {
	TextList int `json:"textlist_id"`
	Text     int `json:"text_id"`
}

// DiagEntry is one diagnostic buffer entry.
type DiagEntry struct {
	// Timestamp is the CPU's ISO 8601 time stamp in UTC, as it sent it
	// (nanoseconds, "Z"). It is kept as text so one odd stamp does not
	// lose the whole read; Time parses it.
	Timestamp string    `json:"timestamp"`
	Status    string    `json:"status"` // DiagIncoming or DiagOutgoing
	Event     DiagEvent `json:"event"`
	ShortText string    `json:"short_text,omitempty"`
	LongText  string    `json:"long_text,omitempty"`
	HelpText  string    `json:"help_text,omitempty"`
}

// Time parses the entry's time stamp.
func (e DiagEntry) Time() (time.Time, error) { return time.Parse(time.RFC3339Nano, e.Timestamp) }

// DiagBuffer is a read of the diagnostic buffer.
type DiagBuffer struct {
	// Entries are in the order the CPU returned them (newest first is
	// expected but not measured). Empty on a status read.
	Entries []DiagEntry `json:"entries,omitempty"`
	// LastModified is when the buffer last changed (ISO 8601, UTC, as
	// sent): a new entry changes it, so comparing two status reads tells
	// whether anything happened in between.
	LastModified string `json:"last_modified"`
	Count        int    `json:"count_current"` // entries the buffer holds
	Max          int    `json:"count_max"`     // entries it can hold
	// Language of the texts; the CPU leaves it out of a status read.
	Language string `json:"language,omitempty"`
}

// DefaultDiagLanguage is the text language when none is given.
const DefaultDiagLanguage = "en-US"

// DiagnosticBuffer reads up to count entries (count ≥ 1) with their texts
// in language (RFC 4647, "en-US"; "" for DefaultDiagLanguage). The CPU
// answers in a language its project holds; which error a missing one
// gives is not yet measured.
func (c *Client) DiagnosticBuffer(ctx context.Context, language string, count int) (DiagBuffer, error) {
	if count < 1 {
		return DiagBuffer{}, fmt.Errorf("webapi: diagnostic buffer count %d: ask for at least one entry, or use DiagnosticBufferStatus", count)
	}
	return c.diagBrowse(ctx, language, count)
}

// DiagnosticBufferStatus reads how many entries the buffer holds and when
// it last changed, without the entries (count 0 on the wire).
func (c *Client) DiagnosticBufferStatus(ctx context.Context) (DiagBuffer, error) {
	return c.diagBrowse(ctx, "", 0)
}

// diagParams is the request. Count has no omitempty: 0 means "status
// only" to the CPU, and leaving it out would mean the default of 50.
type diagParams struct {
	Language string `json:"language"`
	Count    int    `json:"count"`
}

func (c *Client) diagBrowse(ctx context.Context, language string, count int) (DiagBuffer, error) {
	if language == "" {
		language = DefaultDiagLanguage
	}
	if strings.TrimSpace(language) != language || strings.ContainsAny(language, " \"") {
		return DiagBuffer{}, fmt.Errorf("webapi: language %q: want an RFC 4647 tag such as en-US", language)
	}
	var b DiagBuffer
	err := c.call(ctx, "DiagnosticBuffer.Browse", diagParams{Language: language, Count: count}, &b)
	return b, err
}
