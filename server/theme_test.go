package server

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The dashboard's token layer is a hand-copy of the HMI kit's
// (hmi/src/lib/theme.css): the kit is a Svelte/npm package the Go binary
// can't import, and index.html is deliberately a single file with no build
// step. A copy drifts — someone retunes the kit's surfaces for a customer's
// panel and the controller's own landing page quietly stops matching the HMI
// running next to it. This test is the seam: it reads both files and compares
// the tokens they share, per theme.
//
// It does not require the two to define the SAME SET of tokens — the page
// adds --brand and a display face the kit has no use for, and the kit carries
// component tokens the page doesn't. Only tokens present in both must agree.
func TestThemeTokensMatchHMIKit(t *testing.T) {
	kitCSS, err := os.ReadFile("../hmi/src/lib/theme.css")
	if err != nil {
		t.Skip("hmi/src/lib/theme.css not present — skipping token drift check")
	}

	for _, theme := range []string{"dark", "light"} {
		kit := themeTokens(t, string(kitCSS), theme)
		page := themeTokens(t, string(indexHTML), theme)
		if len(kit) == 0 || len(page) == 0 {
			t.Fatalf("%s: parsed %d kit tokens and %d page tokens — the selectors moved", theme, len(kit), len(page))
		}
		shared := 0
		for name, want := range kit {
			got, ok := page[name]
			if !ok {
				continue
			}
			shared++
			if got != want {
				t.Errorf("%s theme: %s is %q on the dashboard but %q in the HMI kit — "+
					"re-copy the block from hmi/src/lib/theme.css, or change both", theme, name, got, want)
			}
		}
		if shared < 10 {
			t.Errorf("%s theme: only %d tokens in common with the kit — the dashboard has stopped tracking it", theme, shared)
		}
	}
}

var declRe = regexp.MustCompile(`(--[a-z0-9-]+)\s*:\s*([^;]+);`)

// themeTokens pulls the custom-property declarations out of the rule that
// defines the named theme. Both files write that rule the same way: a
// selector list mentioning [data-theme='<theme>'] (dark also being the
// bare-:root default), then declarations up to the closing brace.
func themeTokens(t *testing.T, css, theme string) map[string]string {
	t.Helper()
	needle := "[data-theme='" + theme + "']"
	i := strings.Index(css, needle)
	if i < 0 {
		needle = `[data-theme="` + theme + `"]`
		i = strings.Index(css, needle)
	}
	if i < 0 {
		return nil
	}
	open := strings.Index(css[i:], "{")
	closed := strings.Index(css[i:], "}")
	if open < 0 || closed < open {
		return nil
	}
	out := map[string]string{}
	for _, m := range declRe.FindAllStringSubmatch(css[i+open:i+closed], -1) {
		out[m[1]] = strings.TrimSpace(m[2])
	}
	return out
}
