package server

import (
	"net/http"
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

var assetRefRe = regexp.MustCompile(`"(/?assets/[A-Za-z0-9._/-]+)"`)

// TestDashboardAssetsResolve walks every assets/… URL the page names — the
// logo mask, the favicon, the four @font-face sources — and fetches it
// through the real handler, from both places the page is served: "/" and,
// once a project has a screen of its own, "/_nautilus/". A renamed or
// unstaged asset breaks the page silently: the mark disappears and the type
// falls back to a system face, neither of which fails a build.
//
// The URLs must be relative. With a screen configured, "/assets/…" belongs
// to that screen's build and answers a dashboard asset with its index.html
// (200, text/html), which left the moved dashboard with no logo and no fonts.
func TestDashboardAssetsResolve(t *testing.T) {
	var refs []string
	seen := map[string]bool{}
	for _, m := range assetRefRe.FindAllStringSubmatch(string(indexHTML), -1) {
		if strings.HasPrefix(m[1], "/") {
			t.Errorf("index.html names %q; an absolute path misses once the dashboard moves to /_nautilus/", m[1])
			continue
		}
		if !seen[m[1]] {
			seen[m[1]] = true
			refs = append(refs, m[1])
		}
	}
	if len(refs) < 5 { // logo + favicon + 4 fonts, minus duplicates
		t.Fatalf("expected the page to reference the embedded assets, found %d", len(refs))
	}
	for _, c := range []struct {
		base string
		h    http.Handler
	}{
		{"/", New(newTestRuntime(t)).Handler()},
		{"/_nautilus/", New(newTestRuntime(t), Options{HMI: testHMI()}).Handler()},
	} {
		for _, ref := range refs {
			url := c.base + ref
			rec := httptest.NewRecorder()
			c.h.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
			if rec.Code != 200 {
				t.Errorf("GET %s = %d, want 200", url, rec.Code)
				continue
			}
			if rec.Body.Len() == 0 {
				t.Errorf("GET %s served an empty body", url)
			}
			ct := rec.Header().Get("Content-Type")
			if strings.HasPrefix(ct, "text/html") {
				t.Errorf("GET %s Content-Type = %q: that is a page, not the asset", url, ct)
			}
			if strings.HasSuffix(ref, ".woff2") && ct != "font/woff2" {
				t.Errorf("GET %s Content-Type = %q, want font/woff2", url, ct)
			}
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

var programFileRe = regexp.MustCompile(`\bprogram\.(st|ld|fbd|sfc|il)\b`)

// TestDashboardNamesNoProgramFile keeps the page from telling the user which
// file their logic lives in (#582). The dashboard is one static page for
// every project, and it can't know: `naut new --language ld` writes
// program.ld, an FBD project program.fbd, and a manifest may name its files
// anything. A beginner told to edit program.st in a ladder project goes
// looking for a file that isn't there.
func TestDashboardNamesNoProgramFile(t *testing.T) {
	if m := programFileRe.Find(indexHTML); m != nil {
		t.Fatalf("index.html names %q; the dashboard serves every project and can't know its program's file name", m)
	}
}
