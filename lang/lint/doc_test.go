package lint

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The rule catalog, the reference page (docs/structure-warnings.md, synced
// to the website) and the VS Code manifest schema's check.disable list
// name the same rules, so a rule cannot ship undocumented or be impossible
// to switch off in the editor.
func TestRulesAreDocumentedAndInTheSchema(t *testing.T) {
	var ids []string
	for _, r := range Rules {
		ids = append(ids, r.ID)
	}
	sort.Strings(ids)

	doc, err := os.ReadFile("../../docs/structure-warnings.md")
	if err != nil {
		t.Fatal(err)
	}
	var documented []string
	for _, m := range regexp.MustCompile(`(?m)^\| \*\*((?:LD|FBD|SFC)\d{3})\*\* \|`).FindAllStringSubmatch(string(doc), -1) {
		documented = append(documented, m[1])
	}
	sort.Strings(documented)
	if strings.Join(documented, ",") != strings.Join(ids, ",") {
		t.Errorf("docs/structure-warnings.md documents %v, the catalog has %v", documented, ids)
	}
	for _, want := range []string{"max-series`, default **6**", "max-branch-depth`, default **1**", "max-nesting`, default **2**"} {
		if !strings.Contains(string(doc), want) {
			t.Errorf("the reference page does not state %q", want)
		}
	}
	if DefaultMaxSeries != 6 || DefaultMaxBranchDepth != 1 || DefaultMaxNesting != 2 {
		t.Error("a default threshold changed: update docs/structure-warnings.md and the schema's defaults")
	}

	raw, err := os.ReadFile("../../tools/vscode-iec/schemas/nautilus.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties struct {
			Check struct {
				Properties struct {
					Disable struct {
						Items struct {
							Enum []string `json:"enum"`
						} `json:"items"`
					} `json:"disable"`
				} `json:"properties"`
			} `json:"check"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	enum := schema.Properties.Check.Properties.Disable.Items.Enum
	sort.Strings(enum)
	if strings.Join(enum, ",") != strings.Join(ids, ",") {
		t.Errorf("the schema's check.disable offers %v, the catalog has %v", enum, ids)
	}
}
