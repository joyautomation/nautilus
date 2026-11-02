package server

import (
	"os/exec"
	"strings"
	"testing"
)

// TestDashboardShowsTimeAsLiteral guards F-11: a TIME tag streams as integer
// milliseconds, and the dashboard's value cell showed 30000 with no unit.
// With the type /api/meta already reports, it must read as an IEC literal.
// The page has no build step, so the test lifts the formatter out of
// index.html (between the begin:fmt / end:fmt markers) and runs it in node,
// skipping when node is absent.
func TestDashboardShowsTimeAsLiteral(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed — skipping dashboard formatter check")
	}
	page := string(indexHTML)
	i := strings.Index(page, "// begin:fmt")
	j := strings.Index(page, "// end:fmt")
	if i < 0 || j < i {
		t.Fatal("index.html has no begin:fmt / end:fmt region around fmt()")
	}
	js := page[i:j] + `
const cases = [
  [30000, "TIME", "T#30s"],
  [28800000, "TIME", "T#8h"],
  [5400000, "TIME", "T#1h30m"],
  [250, "TIME", "T#250ms"],
  [0, "TIME", "T#0ms"],
  [90061001, "TIME", "T#1d1h1m1s1ms"],
  [-1500, "TIME", "T#-1s500ms"],
  [-0.4, "TIME", "T#0ms"],
  [999.6, "TIME", "T#1s"],
  [30000, "DINT", "30000"],
  [30000, undefined, "30000"],
  [12.5, "REAL", "12.500"],
  [null, "TIME", "—"],
];
let bad = 0;
for (const [v, ty, want] of cases) {
  const got = fmt(v, ty);
  if (got !== want) { console.log("fmt(" + v + ", " + ty + ") = " + got + ", want " + want); bad++; }
}
process.exit(bad ? 1 : 0);
`
	cmd := exec.Command(node, "-")
	cmd.Stdin = strings.NewReader(js)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dashboard formatter:\n%s", out)
	}
	// The value cell must hand fmt the row's declared type, or the
	// formatter is never reached.
	if !strings.Contains(page, "fmt(m.value, m.type)") {
		t.Error("value cells do not pass the tag's declared type to fmt()")
	}
}
