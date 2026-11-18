package runtime

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/retain"
)

// An unusable retain.json is logged as an error at either load site
// (LoadRetained before Run, or the first scan's takeover), and the first
// save after it does not overwrite the evidence: the file store kept it
// aside (#845).
func TestRetainLoadFailureIsLoggedAndTheFileKept(t *testing.T) {
	for _, site := range []string{"LoadRetained", "first scan"} {
		t.Run(site, func(t *testing.T) {
			var buf strings.Builder
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
			t.Cleanup(func() { slog.SetDefault(old) })

			path := filepath.Join(t.TempDir(), "retain.json")
			os.WriteFile(path, []byte("broken"), 0o644)
			rt := newRetained(t, retain.New("", path), nil)
			if site == "LoadRetained" {
				rt.LoadRetained()
			}
			rt.Scan()
			if !strings.Contains(buf.String(), "retained values could not be loaded") {
				t.Errorf("no error logged for an unusable retain.json: %q", buf.String())
			}
			rt.saveRetained(nil) // the saver's first tick
			aside, _ := filepath.Glob(path + ".corrupt-*")
			if len(aside) != 1 {
				t.Fatalf("want the corrupt file kept aside, got %q", aside)
			}
			if b, _ := os.ReadFile(aside[0]); string(b) != "broken" {
				t.Errorf("the kept file = %q, want the corrupt original", b)
			}
			if b, _ := os.ReadFile(path); !strings.Contains(string(b), "SP") {
				t.Errorf("the first save should write a fresh retain.json, got %q", b)
			}
		})
	}
}
