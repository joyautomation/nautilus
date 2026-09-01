package server

import (
	"bytes"
	"io/fs"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

// Options.HMI turns the controller into a one-process HMI deploy: a built
// SPA takes "/" (with SPA-fallback routing for client-side routes), and the
// built-in dashboard moves to "/_nautilus/" so it stays reachable without
// colliding with whatever the HMI's own build puts under "/assets/".

func testHMI() fstest.MapFS {
	return fstest.MapFS{
		"index.html":       {Data: []byte("<html>hmi shell</html>")},
		"favicon.png":      {Data: []byte("PNGDATA")},
		"assets/index.js":  {Data: []byte("console.log('hmi')")},
		"tanks/index.html": {Data: []byte("<html>nested route file</html>")},
	}
}

// A real file in the HMI build is served as-is at its own path.
func TestHMIServesStaticFile(t *testing.T) {
	h := New(newTestRuntime(t), Options{HMI: testHMI()}).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/favicon.png", nil))
	if rec.Code != 200 {
		t.Fatalf("GET /favicon.png = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "PNGDATA" {
		t.Fatalf("GET /favicon.png body = %q", rec.Body.String())
	}
}

// The HMI's own "/assets/…" (its Vite/SvelteKit build output) must be
// reachable at "/assets/…" — the built-in dashboard's assets are the ones
// that moved, not the HMI's.
func TestHMIOwnAssetsServed(t *testing.T) {
	h := New(newTestRuntime(t), Options{HMI: testHMI()}).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/assets/index.js", nil))
	if rec.Code != 200 || rec.Body.String() != "console.log('hmi')" {
		t.Fatalf("GET /assets/index.js = %d %q", rec.Code, rec.Body.String())
	}
}

// A client-side route the HMI's build doesn't have a matching file for
// (a SvelteKit page reached by a deep link, not a full navigation) falls
// back to the bundle's index.html so its router can take over.
func TestHMISPAFallback(t *testing.T) {
	h := New(newTestRuntime(t), Options{HMI: testHMI()}).Handler()
	for _, p := range []string{"/", "/dashboard", "/tanks/101/trend"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != 200 {
			t.Fatalf("GET %s = %d, want 200 (SPA fallback)", p, rec.Code)
		}
		if rec.Body.String() != "<html>hmi shell</html>" {
			t.Fatalf("GET %s body = %q, want the HMI's index.html", p, rec.Body.String())
		}
	}
}

// A path that resolves to a real file one directory down (not the SPA
// fallback) is served as that file, not index.html.
func TestHMINestedRealFileWins(t *testing.T) {
	h := New(newTestRuntime(t), Options{HMI: testHMI()}).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/tanks/", nil))
	if rec.Code != 200 || rec.Body.String() != "<html>nested route file</html>" {
		t.Fatalf("GET /tanks/ = %d %q, want the nested index.html", rec.Code, rec.Body.String())
	}
}

// /api/* must keep working exactly as before — an HMI build is never
// allowed to shadow the tag API, even though it owns "/".
func TestHMIDoesNotShadowAPI(t *testing.T) {
	h := New(newTestRuntime(t), Options{HMI: testHMI()}).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/state", nil))
	if rec.Code != 200 {
		t.Fatalf("GET /api/state = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("GET /api/state Content-Type = %q, want application/json (not the HMI's index.html)", ct)
	}
}

// The built-in dashboard moves to "/_nautilus/" (and its assets to
// "/_nautilus/assets/") once an HMI is configured, rather than
// disappearing — it's still the fastest way to see the raw tag table.
func TestHMIMovesBuiltinDashboardToNautilusPrefix(t *testing.T) {
	h := New(newTestRuntime(t), Options{HMI: testHMI()}).Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/_nautilus/", nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("GET /_nautilus/ = %d %q, want the built-in dashboard", rec.Code, rec.Header().Get("Content-Type"))
	}
	if !bytes.Equal(rec.Body.Bytes(), indexHTML) {
		t.Fatal("GET /_nautilus/ did not serve the built-in dashboard's HTML")
	}

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest("GET", "/", nil))
	if rec2.Body.String() != "<html>hmi shell</html>" {
		t.Fatal("GET / must serve the HMI, not the built-in dashboard, once server.hmi is set")
	}
}

// Once the dashboard moves, plain "/assets/…" belongs to the HMI's build,
// not the embedded dashboard assets — those only answer under
// "/_nautilus/assets/…" now.
func TestHMIBuiltinAssetsMoveWithDashboard(t *testing.T) {
	h := New(newTestRuntime(t), Options{HMI: testHMI()}).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/_nautilus/assets/logo.svg", nil))
	if rec.Code != 200 {
		t.Fatalf("GET /_nautilus/assets/logo.svg = %d, want 200 (the embedded dashboard asset)", rec.Code)
	}
}

// A controller built by `nautilus build` serves the HMI from an archive
// whose files are NOT seekable — fstest.MapFS's are, which is exactly how
// the ReadSeeker-dependent draft of the cache policy passed its tests while
// 404ing every deep link in production. This FS strips Seek.
type noSeekFS struct{ inner fs.FS }

type noSeekFile struct{ inner fs.File }

func (f noSeekFS) Open(name string) (fs.File, error) {
	file, err := f.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return noSeekFile{file}, nil
}
func (f noSeekFile) Stat() (fs.FileInfo, error) { return f.inner.Stat() }
func (f noSeekFile) Read(b []byte) (int, error) { return f.inner.Read(b) }
func (f noSeekFile) Close() error               { return f.inner.Close() }

// Deep links and the cache policy must survive a non-seekable HMI FS.
func TestHMINonSeekableFS(t *testing.T) {
	h := New(newTestRuntime(t), Options{HMI: noSeekFS{testHMI()}}).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/tanks/101", nil))
	if rec.Code != 200 {
		t.Fatalf("deep link on non-seekable FS = %d, want 200", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("hmi shell")) {
		t.Fatalf("deep link body = %q, want the SPA shell", rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", cc)
	}
	if rec.Header().Get("Etag") == "" {
		t.Fatal("no ETag on non-seekable FS")
	}
}

// The stale-tab regression: an embedded FS has no mod times, so before the
// explicit cache policy every file went out "Last-Modified: 1979" and a
// browser's If-Modified-Since revalidation was answered 304 forever — a tab
// open across a redeploy never saw the new build.
func TestHMICachePolicy(t *testing.T) {
	hmi := testHMI()
	hmi["_app/immutable/entry/app.test.js"] = &fstest.MapFile{Data: []byte("js")}
	h := New(newTestRuntime(t), Options{HMI: hmi}).Handler()

	// index.html (and the SPA fallback): no-cache, a content ETag, and NO
	// Last-Modified — the hash is the only validator.
	for _, p := range []string{"/", "/tanks/101"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Fatalf("%s Cache-Control = %q, want no-cache", p, cc)
		}
		if rec.Header().Get("Etag") == "" {
			t.Fatalf("%s: no ETag", p)
		}
		if lm := rec.Header().Get("Last-Modified"); lm != "" {
			t.Fatalf("%s Last-Modified = %q, want none (zero mod time)", p, lm)
		}
	}

	// A correct ETag revalidates to 304…
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	etag := rec.Header().Get("Etag")
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 304 {
		t.Fatalf("If-None-Match %s: code %d, want 304", etag, rec.Code)
	}

	// …but If-Modified-Since alone — what a tab cached before the redeploy
	// sends — must NOT 304: the mod time is not a validator here.
	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set("If-Modified-Since", "Fri, 30 Nov 1979 00:00:00 GMT")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("If-Modified-Since-only revalidation: code %d, want 200", rec.Code)
	}

	// Content-hashed files are immutable for a year.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/_app/immutable/entry/app.test.js", nil))
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Fatalf("immutable asset Cache-Control = %q", cc)
	}
}
