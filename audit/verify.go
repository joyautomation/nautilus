package audit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Report is what Verify found.
type Report struct {
	Files   int    // journal files walked
	Entries int    // complete lines checked
	Head    string // hash of the last line: compare it with an anchor kept elsewhere
	HeadSeq uint64 // its Seq
	// Start is the hash the oldest remaining file chains from: "" for an
	// unpruned journal, else the last hash of a pruned file.
	Start string
	// Torn is set when the newest file ends in a partial line (a crash
	// mid-write); the next OpenFile drops it and records the drop. Not a
	// break.
	Torn bool
	// TailDrops are the audit.tail_dropped entries: each a partial line
	// OpenFile cut off, with its length. A crash leaves one; so does
	// someone stripping the last newline to make a whole entry vanish,
	// which is why they are listed rather than counted as nothing.
	TailDrops []TailDrop
	// Break is the first place the chain does not hold, nil when it does.
	Break *Break
}

// TailDrop is one recorded cut of a partial final line.
type TailDrop struct {
	File  string // the file it was cut from
	Bytes int
	At    string // the file and line of the audit.tail_dropped entry
}

// Break is where the chain fails: at File's line Line (1-based), whose
// prev does not match the line before it, or which is not an entry.
type Break struct {
	File   string
	Line   int
	Reason string
}

func (b *Break) Error() string { return fmt.Sprintf("%s:%d: %s", b.File, b.Line, b.Reason) }

// OK reports an unbroken chain.
func (r Report) OK() bool { return r.Break == nil }

// Verify walks the journal in dir, oldest file first, and checks that
// every line is an entry whose prev is the hash of the line before it,
// across files. The oldest remaining file may start mid-chain only where an
// audit.prune entry recorded that file's predecessor's last hash; anything
// else means a file or lines were removed.
//
// What it cannot see, on its own: the journal's last lines cut off (the
// chain that is left still holds), or the whole journal rewritten with the
// chain recomputed, which anyone who can write the files can do. An anchor
// (a Seq and its hash kept somewhere the controller's service user cannot
// rewrite) closes that: see docs/design/identity-audit.md §4.
func Verify(dir string) (Report, error) {
	var r Report
	files, err := listFiles(dir)
	if err != nil {
		return r, err
	}
	prev := ""
	first := true
	pruned := map[string]uint64{} // last hash -> its Seq, recorded by audit.prune
	var startFile string
	var startSeq, seq uint64
	for fi, name := range files {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return r, err
		}
		r.Files++
		if good := bytes.LastIndexByte(data, '\n') + 1; good < len(data) {
			if fi == len(files)-1 {
				r.Torn = true
			} else {
				r.Break = &Break{name, bytes.Count(data[:good], []byte{'\n'}) + 1, "a partial line in a file that is not the newest"}
				return r, nil
			}
			data = data[:good]
		}
		if len(data) == 0 {
			continue
		}
		for i, line := range bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'}) {
			var e Entry
			if err := json.Unmarshal(line, &e); err != nil || e.ID == "" || e.Action == "" {
				r.Break = &Break{name, i + 1, "not an audit entry"}
				return r, nil
			}
			if first {
				r.Start, startFile, startSeq = e.Prev, name, e.Seq
				first = false
				if e.Prev == "" && e.Seq != 1 {
					r.Break = &Break{name, i + 1, fmt.Sprintf("the journal's first entry has seq %d, want 1", e.Seq)}
					return r, nil
				}
			} else if e.Prev != prev {
				r.Break = &Break{name, i + 1, "prev does not match the line before: a line was edited, removed, inserted or moved, or a file is missing"}
				return r, nil
			} else if e.Seq != seq+1 {
				r.Break = &Break{name, i + 1, fmt.Sprintf("seq %d after %d", e.Seq, seq)}
				return r, nil
			}
			switch e.Action {
			case "audit.prune":
				if last, ok := e.Detail["last"].(string); ok && last != "" {
					n, _ := e.Detail["seq"].(float64)
					pruned[last] = uint64(n)
				}
			case "audit.tail_dropped":
				n, _ := e.Detail["bytes"].(float64)
				r.TailDrops = append(r.TailDrops, TailDrop{File: e.Target, Bytes: int(n), At: fmt.Sprintf("%s:%d", name, i+1)})
			}
			prev, seq = lineHash(line), e.Seq
			r.Entries++
		}
	}
	r.Head, r.HeadSeq = prev, seq
	if r.Start != "" {
		last, ok := pruned[r.Start]
		switch {
		case !ok:
			r.Break = &Break{startFile, 1, "the journal starts mid-chain and no audit.prune entry records it: an earlier file was removed"}
		case last+1 != startSeq:
			r.Break = &Break{startFile, 1, fmt.Sprintf("the journal starts at seq %d, but the audit.prune entry for its start records seq %d", startSeq, last)}
		}
	}
	return r, nil
}
