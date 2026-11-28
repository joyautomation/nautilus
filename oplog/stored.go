package oplog

import (
	"bytes"
	"encoding/json"
)

// stored is one entry as the node keeps it in memory (#1455): its number
// and term, which the log's own rules read, and the entry as JSON, decoded
// when it is applied, sent or compared. A decoded Entry kept about 500
// bytes of heap (a 304-byte struct, most of its fields empty, and its
// strings); the JSON is about 200, in one allocation with no pointers in
// it. raw is what this node wrote for the entry (json.Marshal of it) or,
// read back from the file, the line's entry as it stands there.
type stored struct {
	Seq, Term uint64
	raw       []byte
}

// storedOf keeps e. raw is json.Marshal(e), which is also what its line in
// the file holds.
func storedOf(e Entry) stored {
	b, _ := json.Marshal(e)
	return stored{Seq: e.Seq, Term: e.Term, raw: b}
}

// entry is the stored entry, decoded. Every raw was decoded once before it
// was kept (proposed, received or read from the file), so it decodes.
func (s stored) entry() Entry {
	var e Entry
	_ = json.Unmarshal(s.raw, &e)
	return e
}

// same is Entry.same for a stored entry: the bytes first, then, when they
// differ, as Entry.same compares (a line another build wrote may order or
// carry fields differently and still be the same entry).
func (s stored) same(e Entry) bool {
	b, _ := json.Marshal(e)
	return bytes.Equal(s.raw, b) || s.entry().same(e)
}

// line is the entry's line in the file, newline included: what
// appendLine(fileLine{E: &e}) writes for it.
func (s stored) line(buf []byte) []byte {
	buf = append(buf, `{"e":`...)
	buf = append(buf, s.raw...)
	return append(buf, '}', '\n')
}

// mayLapse: raw may be a KindLapse entry. Only those are decoded to find
// the entries they lapse (applyRange): "lapse" appears, written plainly or
// behind a \u escape, in every entry that is one.
func (s stored) mayLapse() bool {
	return bytes.Contains(s.raw, []byte("lapse")) || bytes.Contains(s.raw, []byte(`\u`))
}

// storedLine reads one line of the file as open does: an entry (e), a
// commit mark (c), or an entry of the first format (bare, applied as
// written: old). ok false: the line does not parse, or its entry does not
// decode as an Entry (the same lines the whole-entry decode refused).
func storedLine(line []byte) (e *stored, c *uint64, old, ok bool) {
	var d Entry
	// The line this node writes, {"e":<entry>}: the entry decoded in place.
	// It decodes only when it is one JSON value, so the line was exactly
	// that.
	if raw, cut := bytes.CutPrefix(line, []byte(`{"e":`)); cut && len(raw) > 0 && raw[len(raw)-1] == '}' {
		raw = raw[:len(raw)-1]
		if json.Unmarshal(raw, &d) == nil && bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
			return &stored{Seq: d.Seq, Term: d.Term, raw: bytes.Clone(bytes.TrimSpace(raw))}, nil, false, true
		}
		d = Entry{}
	}
	var l struct {
		E json.RawMessage `json:"e,omitempty"`
		C *uint64         `json:"c,omitempty"`
	}
	if err := json.Unmarshal(line, &l); err != nil {
		return nil, nil, false, false
	}
	switch {
	case len(l.E) > 0 && !bytes.Equal(l.E, []byte("null")):
		if json.Unmarshal(l.E, &d) != nil {
			return nil, nil, false, false
		}
		return &stored{Seq: d.Seq, Term: d.Term, raw: l.E}, nil, false, true
	case l.C != nil:
		return nil, l.C, false, true
	}
	// The first format wrote one bare Entry per line.
	if json.Unmarshal(line, &d) == nil && d.Seq > 0 {
		return &stored{Seq: d.Seq, Term: d.Term, raw: bytes.Clone(line)}, nil, true, true
	}
	return nil, nil, false, true
}
