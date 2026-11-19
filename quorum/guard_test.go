package quorum

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// VoteTerm and the fencing token Term are two number spaces. Outside
// quorum/ (and its sim), nothing may read VoteTerm, so nothing can compare
// a vote term with a token by mistake (design §3.1, the epoch).
func TestVoteTermStaysInsideQuorum(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	quorumDir := filepath.Join(root, "quorum") + string(filepath.Separator)
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "testdata", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasPrefix(p, quorumDir) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), ".VoteTerm(") {
			rel, _ := filepath.Rel(root, p)
			t.Errorf("%s reads VoteTerm: outside quorum/ use Term (the epoch) only", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
