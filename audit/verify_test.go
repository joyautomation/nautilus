package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// journal writes six entries over two days (two files) and returns the dir
// and the head hash as recorded.
func journal(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	c := &clock{t0()}
	j := open(t, dir, c, FileOptions{})
	for i, target := range []string{"A", "B", "C"} {
		write(t, j, "tag.write", target)
		c.t = c.t.Add(time.Duration(i+1) * time.Minute)
	}
	c.t = c.t.Add(24 * time.Hour)
	for _, target := range []string{"D", "E", "F"} {
		write(t, j, "tag.write", target)
	}
	head, _ := j.Head()
	j.Close()
	return dir, head
}

// rewrite applies fn to one file's lines.
func rewrite(t *testing.T, dir, name string, fn func([]string) []string) {
	t.Helper()
	p := filepath.Join(dir, name)
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	ls := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	ls = fn(ls)
	out := strings.Join(ls, "\n")
	if len(ls) > 0 {
		out += "\n"
	}
	if err := os.WriteFile(p, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
}

const (
	day1 = "audit-20261118.jsonl"
	day2 = "audit-20261119.jsonl"
)

// Every way of changing the journal's past that Verify must catch, and the
// two it cannot (changes at the very end), which an anchor catches.
func TestVerifyFindsTampering(t *testing.T) {
	cases := []struct {
		name      string
		tamper    func(t *testing.T, dir string)
		wantBreak string // file of the break; "" = the chain holds
		headMoves bool   // the head no longer matches the recorded one
		reason    string // a word the break's reason must contain, when set
	}{
		{"untouched", func(*testing.T, string) {}, "", false, ""},
		{"a value edited", func(t *testing.T, d string) {
			rewrite(t, d, day1, func(ls []string) []string { ls[1] = strings.Replace(ls[1], `"after":55`, `"after":56`, 1); return ls })
		}, day1, false, ""},
		{"a line removed", func(t *testing.T, d string) {
			rewrite(t, d, day1, func(ls []string) []string { return append(ls[:1], ls[2:]...) })
		}, day1, false, ""},
		{"a line inserted", func(t *testing.T, d string) {
			rewrite(t, d, day1, func(ls []string) []string { return append(ls[:2], append([]string{ls[1]}, ls[2:]...)...) })
		}, day1, false, ""},
		{"two lines swapped", func(t *testing.T, d string) {
			rewrite(t, d, day1, func(ls []string) []string { ls[0], ls[1] = ls[1], ls[0]; return ls })
		}, day1, false, ""},
		{"the first line of a day removed", func(t *testing.T, d string) {
			rewrite(t, d, day2, func(ls []string) []string { return ls[1:] })
		}, day2, false, ""},
		{"the oldest file removed, no prune record", func(t *testing.T, d string) {
			os.Remove(filepath.Join(d, day1))
		}, day2, false, ""},
		{"the newest file removed", func(t *testing.T, d string) {
			os.Remove(filepath.Join(d, day2))
		}, "", true, ""},
		{"a line that is not an entry", func(t *testing.T, d string) {
			rewrite(t, d, day1, func(ls []string) []string { ls[1] = `{"note":"hello"}`; return ls })
		}, day1, false, "not an audit entry"},
		{"a partial line in an older file", func(t *testing.T, d string) {
			f, _ := os.OpenFile(filepath.Join(d, day1), os.O_APPEND|os.O_WRONLY, 0)
			f.WriteString(`{"id":"01X`)
			f.Close()
		}, day1, false, "partial line"},
		// The end of the chain: Verify can't tell, the anchor can.
		{"the last line edited", func(t *testing.T, d string) {
			rewrite(t, d, day2, func(ls []string) []string { ls[2] = strings.Replace(ls[2], `"after":55`, `"after":99`, 1); return ls })
		}, "", true, ""},
		{"the last line removed", func(t *testing.T, d string) {
			rewrite(t, d, day2, func(ls []string) []string { return ls[:2] })
		}, "", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, head := journal(t)
			tc.tamper(t, dir)
			r, err := Verify(dir)
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case tc.wantBreak == "" && !r.OK():
				t.Errorf("break %v, want the chain to hold", r.Break)
			case tc.wantBreak != "" && r.OK():
				t.Errorf("the chain holds, want a break in %s", tc.wantBreak)
			case tc.wantBreak != "" && r.Break.File != tc.wantBreak:
				t.Errorf("break in %s (%v), want %s", r.Break.File, r.Break, tc.wantBreak)
			case tc.reason != "" && !strings.Contains(r.Break.Reason, tc.reason):
				t.Errorf("break %v, want its reason to say %q", r.Break, tc.reason)
			}
			if r.OK() && (r.Head != head) != tc.headMoves {
				t.Errorf("head moved %v, want %v", r.Head != head, tc.headMoves)
			}
		})
	}
}

// A chain that starts mid-way verifies only when an audit.prune entry
// names its start; forging the start of the oldest file does not pass.
func TestVerifyAcceptsAStartOnlyWhereAPruneRecordsIt(t *testing.T) {
	dir, _ := journal(t)
	os.Remove(filepath.Join(dir, day1))
	r, _ := Verify(dir)
	if r.OK() || !strings.Contains(r.Break.Reason, "no audit.prune entry records it") {
		t.Fatalf("an unrecorded start: %v, want the break that says no audit.prune records it", r.Break)
	}
}
