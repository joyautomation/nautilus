package writer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/l5x"
)

// mnemonicsIn collects every instruction name in an export's RLL rungs.
func mnemonicsIn(t *testing.T, f *l5x.File) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	var walk func([]l5x.Term)
	walk = func(terms []l5x.Term) {
		for _, tm := range terms {
			if tm.Instr != nil {
				out[strings.ToUpper(tm.Instr.Mnemonic)] = true
				continue
			}
			for _, leg := range tm.Legs {
				walk(leg)
			}
		}
	}
	for _, p := range f.Controller.Programs {
		for _, r := range p.Routines {
			for _, rg := range r.Rungs {
				terms, err := l5x.ParseRung(rg.Text)
				if err != nil {
					t.Fatalf("%s/%s rung %d: %v", p.Name, r.Name, rg.Number, err)
				}
				walk(terms)
			}
		}
	}
	return out
}

// Every mnemonic the writer can emit must be one a real export contains.
// The importer does not validate names (logix-target.md §21), so this is
// the only pre-build check there is.
func TestMnemonicsAreCorpusVocabulary(t *testing.T) {
	vocab := map[string]bool{}
	for _, m := range CorpusVocabulary {
		vocab[m] = true
	}
	for _, m := range Mnemonics {
		if !vocab[m] {
			t.Errorf("writer emits %s, which no surveyed export contains", m)
		}
	}
	// The editor captions that are NOT neutral text must never creep in.
	for _, caption := range []string{"GEQ", "GRT", "LEQ", "LES", "EQU", "NEQ", "MOV", "LIM"} {
		for _, m := range Mnemonics {
			if m == caption {
				t.Errorf("writer emits the editor caption %s, not a neutral-text mnemonic", m)
			}
		}
	}
}

// The committed fixtures are real exports (and one hand-built file whose
// shapes came from real ones); the mnemonics they contain must agree with
// the recorded vocabulary.
func TestFixturesAgreeWithVocabulary(t *testing.T) {
	vocab := map[string]bool{}
	for _, m := range CorpusVocabulary {
		vocab[m] = true
	}
	dir := filepath.Join("..", "..", "lang", "l5x", "testdata")
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".L5X") {
			continue
		}
		f, err := l5x.ParseFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for m := range mnemonicsIn(t, f) {
			seen[m] = true
			if !vocab[m] && m != "PUMP_CONTROL" { // variety.L5X's AOI call
				t.Errorf("%s contains %s, which the recorded vocabulary lacks", e.Name(), m)
			}
		}
	}
	for _, m := range []string{"XIC", "XIO", "OTE", "OTL", "OTU", "GE", "TON", "ONS", "MOVE"} {
		if !seen[m] {
			t.Errorf("no committed fixture exercises %s", m)
		}
	}
}

// With the private corpus on disk, re-derive the vocabulary from it and
// hold the writer to that instead of the recorded list:
//
//	NAUTILUS_L5X_CORPUS=/path/to/exports go test ./logix/writer/
func TestMnemonicsInCorpus(t *testing.T) {
	dir := os.Getenv("NAUTILUS_L5X_CORPUS")
	if dir == "" {
		t.Skip("set NAUTILUS_L5X_CORPUS to a directory of .L5X exports")
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range ents {
		if !strings.EqualFold(filepath.Ext(e.Name()), ".L5X") {
			continue
		}
		f, err := l5x.ParseFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for m := range mnemonicsIn(t, f) {
			seen[m] = true
		}
	}
	for _, m := range Mnemonics {
		if !seen[m] {
			t.Errorf("writer emits %s, which no export in %s contains", m, dir)
		}
	}
}
