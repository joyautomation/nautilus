// Package writable is the one rule for which tags a protocol may write:
// sparkplug.writable (NCMD/DCMD) and opcua.writable (an OPC UA client's
// Write) each hold a list of these patterns, matched the same way, so the
// two lists cannot drift in meaning (docs/design/opcua-server-writes.md §1).
//
// A pattern is a glob over a tag name or a member's path ("TempSP",
// "Pump*.SpeedSP", "Motor1.START", "Drives[0].Speed", "Drives[*].Speed"):
//
//   - `*` matches any run of characters but `/`, `?` any one character
//     but `/` (as path.Match's);
//   - `[n]`, an integer index, matches that array index literally, so
//     "Counts[2]" names the element Counts[2];
//   - `[*]` matches any one array index: "Counts[*]" names every element
//     of Counts, "Drives[*].Speed" every element's Speed;
//   - `\` escapes the next character, so `\[2\]` is the same as `[2]`;
//   - any other `[` is malformed: a pattern has no character classes.
//
// A pattern that matches a tag opens all of its members; one that matches
// a member opens that member and everything below it. Below means through
// struct members and array elements alike: "Drives" opens
// "Drives[0].Speed". An empty list opens nothing.
package writable

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// Patterns is a writable list.
type Patterns []string

// Validate rejects a malformed pattern, so it is refused when the project
// loads rather than silently matching nothing.
func (ps Patterns) Validate() error {
	for _, p := range ps {
		if _, err := compile(p); err != nil {
			return fmt.Errorf("writable pattern %q: %w", p, err)
		}
	}
	return nil
}

// Allows reports whether the tag or member at p may be written: p itself,
// or any ancestor of it, matches a pattern. An ancestor ends at a member
// separator or an array index: "Drives[0].Speed", "Drives[0]", "Drives".
func (ps Patterns) Allows(p string) bool {
	for {
		if ps.matchOne(p) {
			return true
		}
		i := strings.LastIndexAny(p, ".[")
		if i < 0 {
			return false
		}
		p = p[:i]
	}
}

// Matches reports whether pattern pat matches p exactly (no ancestor rule):
// what `naut check` asks to find a pattern that names nothing. A malformed
// pattern matches nothing.
func Matches(pat, p string) bool {
	re, err := compile(pat)
	return err == nil && re.MatchString(p)
}

func (ps Patterns) matchOne(p string) bool {
	for _, pat := range ps {
		if Matches(pat, p) {
			return true
		}
	}
	return false
}

// compiled caches each pattern's regexp: Allows runs per written item and
// per ancestor, and a list is a handful of patterns that never change.
var compiled sync.Map // string -> *regexp.Regexp

// compile turns a pattern into an anchored regexp, by the rules in the
// package comment.
func compile(pat string) (*regexp.Regexp, error) {
	if re, ok := compiled.Load(pat); ok {
		return re.(*regexp.Regexp), nil
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pat); i++ {
		switch c := pat[i]; c {
		case '*':
			b.WriteString(`[^/]*`)
		case '?':
			b.WriteString(`[^/]`)
		case '\\':
			if i+1 == len(pat) {
				return nil, fmt.Errorf("a trailing \\ escapes nothing")
			}
			i++
			b.WriteString(regexp.QuoteMeta(pat[i : i+1]))
		case '[':
			end := strings.IndexByte(pat[i:], ']')
			if end < 0 {
				return nil, fmt.Errorf("[ at %d has no ]: an array index is [n] or [*]", i)
			}
			idx := pat[i+1 : i+end]
			switch {
			case idx == "*":
				b.WriteString(`\[-?[0-9]+\]`)
			case isIndex(idx):
				b.WriteString(regexp.QuoteMeta("[" + idx + "]"))
			default:
				return nil, fmt.Errorf("[%s] is not an array index: write [n] for one index or [*] for any (a pattern has no character classes)", idx)
			}
			i += end
		default:
			b.WriteString(regexp.QuoteMeta(pat[i : i+1])) // byte by byte: UTF-8 passes through
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, err
	}
	compiled.Store(pat, re)
	return re, nil
}

// isIndex reports whether s is an integer array index as an address spells
// it: digits, with a leading minus for a negative lower bound.
func isIndex(s string) bool {
	s = strings.TrimPrefix(s, "-")
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
