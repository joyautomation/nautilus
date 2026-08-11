package server

import (
	"net/http/httptest"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

var scriptRe = regexp.MustCompile(`(?s)<script[^>]*>(.*?)</script>`)

// TestDashboardScriptParses guards the built-in dashboard's inline JavaScript
// against syntax errors. The page is a single hand-authored HTML file with no
// build step, so a broken script (a duplicate declaration, an unbalanced
// brace) ships silently and the whole dashboard dies at load — the browser
// just sits at "connecting…". This runs `node --check` over the extracted
// script when node is available, and skips otherwise (so it never blocks a
// node-less CI, but catches the regression wherever node exists).
func TestDashboardScriptParses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed — skipping dashboard JS syntax check")
	}

	var js strings.Builder
	for _, m := range scriptRe.FindAllSubmatch(indexHTML, -1) {
		js.Write(m[1])
		js.WriteByte('\n')
	}
	if js.Len() == 0 {
		t.Fatal("no <script> block found in index.html")
	}

	cmd := exec.Command(node, "--check", "-")
	cmd.Stdin = strings.NewReader(js.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dashboard inline script has a syntax error:\n%s", out)
	}
}

var assetRefRe = regexp.MustCompile(`/assets/[A-Za-z0-9._/-]+`)

// TestDashboardAssetsResolve walks every /assets/… URL the page names — the
// logo mask, the favicon, the four @font-face sources — and fetches it
// through the real handler. A renamed or unstaged asset breaks the page
// silently: the mark disappears and the type falls back to a system face,
// neither of which fails a build.
func TestDashboardAssetsResolve(t *testing.T) {
	srv := New(newTestRuntime(t))
	h := srv.Handler()

	refs := assetRefRe.FindAllString(string(indexHTML), -1)
	if len(refs) < 5 { // logo + favicon + 4 fonts, minus duplicates
		t.Fatalf("expected the page to reference the embedded assets, found %d", len(refs))
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		if seen[ref] {
			continue
		}
		seen[ref] = true
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", ref, nil))
		if rec.Code != 200 {
			t.Errorf("GET %s = %d, want 200", ref, rec.Code)
			continue
		}
		if rec.Body.Len() == 0 {
			t.Errorf("GET %s served an empty body", ref)
		}
		if ct := rec.Header().Get("Content-Type"); strings.HasSuffix(ref, ".woff2") && ct != "font/woff2" {
			t.Errorf("GET %s Content-Type = %q, want font/woff2", ref, ct)
		}
	}
}

// TestUnknownAssetNotFound keeps the asset route from becoming a way to read
// anything outside the embedded tree.
func TestUnknownAssetNotFound(t *testing.T) {
	h := New(newTestRuntime(t)).Handler()
	for _, p := range []string{"/assets/nope.svg", "/assets/../server.go", "/assets/"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code == 200 {
			t.Errorf("GET %s = 200, want a 404 or a redirect", p)
		}
	}
}
