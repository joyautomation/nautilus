package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The install one-liners in the README and the getting-started page (#559).
// Piping curl into tar meant a 404 or a dropped connection gave tar an HTML
// page or half an archive, and `grep -m1` on the version lookup exited
// early, so curl printed "(23) Failure writing output" on every install.
// The commands download to a file with -f first, then extract.
func TestInstallDocsDownloadBeforeExtracting(t *testing.T) {
	archive := regexp.MustCompile(`releases/download/\$v/nautilus_\$\{v#v\}_(darwin|linux)_(amd64|arm64)\.tar\.gz"`)
	for _, page := range []string{"README.md", "website/src/content/docs/getting-started.md"} {
		b, err := os.ReadFile(filepath.Join("..", "..", page))
		if err != nil {
			t.Fatal(err)
		}
		downloads := 0
		for i, line := range strings.Split(string(b), "\n") {
			if !strings.Contains(line, "curl ") {
				continue
			}
			at := page + ":" + strconv.Itoa(i+1)
			if p := strings.Index(line, "|"); p >= 0 {
				rest := line[p:]
				if strings.Contains(rest, "tar ") {
					t.Errorf("%s pipes curl into tar; download to a file first:\n%s", at, line)
				}
				if strings.Contains(rest, "grep -m") || strings.Contains(rest, "| head") {
					t.Errorf("%s pipes curl into a reader that stops early, so curl reports (23):\n%s", at, line)
				}
			}
			if strings.Contains(line, "-o naut.tar.gz") {
				downloads++
				if !strings.Contains(line, "curl -fsSL ") {
					t.Errorf("%s downloads without -f, so a 404 saves an HTML page as the archive:\n%s", at, line)
				}
				if !archive.MatchString(line) {
					t.Errorf("%s does not name a release archive as goreleaser names it (nautilus_<version>_<os>_<arch>.tar.gz):\n%s", at, line)
				}
			}
		}
		if downloads < 2 {
			t.Errorf("%s: %d curl -o naut.tar.gz downloads, want the macOS and Linux ones", page, downloads)
		}
	}
}
