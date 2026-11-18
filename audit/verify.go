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
	// Start is the hash the oldest remaining file chains from: "" for an
	// unpruned journal, else the last hash of a pruned file.
	Start string
	// Torn is set when the newest file ends in a partial line (a crash
	// mid-write); the next OpenFile drops it. Not a break.
	Torn bool
	// Break is the first place the chain does not hold, nil when it does.
	Break *Break
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
// What it cannot see: lines removed from the very end of the newest file
// leave a shorter chain that still holds. Comparing Head with an anchor
// kept elsewhere (the Postgres copy, the peer) closes that.
func Verify(dir string) (Report, error) {
	var r Report
	files, err := listFiles(dir)
	if err != nil {
		return r, err
	}
	prev := ""
	first := true
	pruned := map[string]bool{} // last hashes recorded by audit.prune
	var startFile string
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
				r.Start, startFile = e.Prev, name
				first = false
			} else if e.Prev != prev {
				r.Break = &Break{name, i + 1, "prev does not match the line before: a line was edited, removed, inserted or moved, or a file is missing"}
				return r, nil
			}
			if e.Action == "audit.prune" {
				if last, ok := e.Detail["last"].(string); ok && last != "" {
					pruned[last] = true
				}
			}
			prev = lineHash(line)
			r.Entries++
		}
	}
	r.Head = prev
	if r.Start != "" && !pruned[r.Start] {
		r.Break = &Break{startFile, 1, "the journal starts mid-chain and no audit.prune entry records it: an earlier file was removed"}
	}
	return r, nil
}
