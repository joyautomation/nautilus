// Package writable is the one rule for which tags a protocol may write:
// sparkplug.writable (NCMD/DCMD) and opcua.writable (an OPC UA client's
// Write) each hold a list of these patterns, matched the same way, so the
// two lists cannot drift in meaning (docs/design/opcua-server-writes.md §1).
//
// A pattern is a glob (path.Match) over a tag name or a member's dotted
// path ("TempSP", "Pump*.SpeedSP", "Motor1.START"). A pattern that matches
// a tag opens all of its members; one that matches a member opens that
// member and everything below it. An empty list opens nothing.
package writable

import (
	"fmt"
	"path"
	"strings"
)

// Patterns is a writable list.
type Patterns []string

// Validate rejects a malformed pattern, so it is refused when the project
// loads rather than silently matching nothing.
func (ps Patterns) Validate() error {
	for _, p := range ps {
		if _, err := path.Match(p, ""); err != nil {
			return fmt.Errorf("writable pattern %q: %w", p, err)
		}
	}
	return nil
}

// Allows reports whether the tag or member at p may be written: p itself,
// or any dotted ancestor of it, matches a pattern.
func (ps Patterns) Allows(p string) bool {
	for {
		if ps.matchOne(p) {
			return true
		}
		i := strings.LastIndexByte(p, '.')
		if i < 0 {
			return false
		}
		p = p[:i]
	}
}

// Matches reports whether pattern pat matches p exactly (no ancestor rule):
// what `naut check` asks to find a pattern that names nothing.
func Matches(pat, p string) bool {
	ok, _ := path.Match(pat, p)
	return ok
}

func (ps Patterns) matchOne(p string) bool {
	for _, pat := range ps {
		if Matches(pat, p) {
			return true
		}
	}
	return false
}
