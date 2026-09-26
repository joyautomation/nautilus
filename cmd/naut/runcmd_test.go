package main

// hmiBannerSuffix is `naut run`'s (and a built binary's own runProject's)
// half of the fix this file's sibling, build_test.go, covers for `naut
// build`: server.hmi naming a directory that doesn't exist yet must not
// silently 404 every request — it warns once, on stderr, and the built-in
// dashboard keeps "/". runProject itself blocks on Ctrl+C, so this
// decision is split out where a test can drive it directly.

import (
	"bytes"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/internal/project"
)

// A project with no server.hmi at all: no warning, plain dashboard suffix.
func TestHMIBannerSuffixNoHMIConfigured(t *testing.T) {
	var stderr bytes.Buffer
	proj := &project.Project{}
	suffix := hmiBannerSuffix(proj, "localhost:8080", &stderr)
	if suffix != " — dashboard + tag API on http://localhost:8080" {
		t.Fatalf("suffix = %q", suffix)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want no warning when server.hmi is unset", stderr.String())
	}
}

// server.hmi configured and its directory built: the "hmi (...)" suffix,
// no warning — unchanged from before this fix.
func TestHMIBannerSuffixHMIPresent(t *testing.T) {
	var stderr bytes.Buffer
	proj := &project.Project{HMIDir: "hmi/build"}
	suffix := hmiBannerSuffix(proj, "localhost:8080", &stderr)
	if suffix != " — hmi (hmi/build) + tag API on http://localhost:8080" {
		t.Fatalf("suffix = %q", suffix)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want no warning when the HMI is built", stderr.String())
	}
}

// server.hmi configured but not built yet: the dashboard suffix (same as
// unset — the built-in dashboard keeps "/"), plus a one-line warning
// naming the missing directory and the npm step to fix it.
func TestHMIBannerSuffixHMIMissingWarns(t *testing.T) {
	var stderr bytes.Buffer
	proj := &project.Project{HMIDir: "hmi/build", HMIMissing: true}
	suffix := hmiBannerSuffix(proj, "localhost:8080", &stderr)
	if suffix != " — dashboard + tag API on http://localhost:8080" {
		t.Fatalf("suffix = %q, want the dashboard suffix (the built-in dashboard keeps \"/\")", suffix)
	}
	warning := stderr.String()
	if !strings.Contains(warning, "naut run: server.hmi: hmi/build is not built yet") ||
		!strings.Contains(warning, "built-in dashboard") ||
		!strings.Contains(warning, "npm run build in hmi/") {
		t.Fatalf("stderr = %q, missing the expected warning", warning)
	}
}
