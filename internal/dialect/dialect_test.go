package dialect

import (
	"testing"

	"github.com/joyautomation/nautilus/lang/st"
)

func TestLogixLibraryParsesAndIsDeclarationsOnly(t *testing.T) {
	libs, err := Sources("logix")
	if err != nil || len(libs) == 0 {
		t.Fatalf("logix: %v %d", err, len(libs))
	}
	for _, l := range libs {
		prog, err := st.Parse(l.ST)
		if err != nil {
			t.Errorf("%s: %v", l.Path, err)
			continue
		}
		if prog.TopKeyword == "PROGRAM" || len(prog.Statements) > 0 {
			t.Errorf("%s: a dialect library declares blocks only", l.Path)
		}
	}
	if libs, _ := Sources("nautilus"); len(libs) != 0 {
		t.Errorf("the nautilus dialect adds nothing")
	}
	if libs, err := Sources("siemens"); err != nil || len(libs) != 0 {
		t.Errorf("siemens is reserved and empty: %v %d", err, len(libs))
	}
	if _, err := Sources("modicon"); err == nil {
		t.Errorf("an unknown dialect is an error")
	}
}
