package server

import (
	"encoding/json"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// dashboardHarness runs the dashboard's REAL inline script under node with a
// DOM that absorbs every call, feeds it the controller's real /api/meta and a
// tag snapshot, and prints how the tag table would render each row's value
// cell. Nothing about the page's logic is re-implemented here.
const dashboardHarness = `
const vm = require("vm");
const fs = require("fs");
const input = JSON.parse(fs.readFileSync(0, "utf8"));
process.on("unhandledRejection", () => {});
const absorb = new Proxy(function () {}, {
  get: (_t, p) => (p === Symbol.toPrimitive ? () => "" : p === "then" ? undefined : absorb),
  apply: () => absorb,
  construct: () => absorb,
  set: () => true,
});
const never = new Promise(() => {});
// Elements fetched by id record their event listeners, so a click can be
// dispatched to the page's real tag-table handler.
const absorbing = (get) => new Proxy(function () {}, {
  get: (_t, p) => get(p) ?? (p === Symbol.toPrimitive ? () => "" : p === "then" ? undefined : absorb),
  apply: () => absorb,
  construct: () => absorb,
  set: () => true,
});
const listeners = {};
const elems = {};
const element = (id) => elems[id] || (elems[id] = absorbing((p) => p === "addEventListener"
  ? (type, fn) => (listeners[id + ":" + type] = listeners[id + ":" + type] || []).push(fn)
  : undefined));
const posts = [];
const ctx = {
  document: absorbing((p) => (p === "getElementById" ? element : undefined)),
  window: absorb, localStorage: absorb, navigator: absorb,
  EventSource: function () { return absorb; },
  setTimeout: () => 0, setInterval: () => 0, clearTimeout: () => {}, requestAnimationFrame: () => 0,
  matchMedia: () => absorb, console, Math, JSON, Object, Array, Number, String, Set, Map, Promise, Date,
  fetch: (url, opts) => {
    if (url.startsWith("/api/meta")) {
      return Promise.resolve({ ok: true, json: () => Promise.resolve(input.meta), text: () => Promise.resolve("") });
    }
    if (opts && opts.method === "POST") {
      posts.push({ url, body: JSON.parse(opts.body) });
      return Promise.resolve({ ok: true, json: () => Promise.resolve({}), text: () => Promise.resolve("") });
    }
    return never;
  },
};
vm.createContext(ctx);
vm.runInContext(input.script, ctx);
setImmediate(() => setImmediate(() => {
  const out = vm.runInContext(
    "lastTags = " + JSON.stringify(input.tags) + "; " +
    JSON.stringify(input.expand || []) + ".forEach((p) => expanded.add(p)); " +
    "tableModel().map((m) => ({ key: m.key, kind: m.kind, write: m.write, cell: valueCell(m) }))",
    ctx);
  // Click each requested row's value cell: the event target's closest()
  // answers from the cell's real rendered HTML, and the event goes to the
  // handlers the page registered on the tag table.
  const clicks = [];
  for (const key of input.clicks || []) {
    const row = out.find((r) => r.key === key);
    const html = row ? row.cell : "";
    const write = (html.match(/data-write="([^"]*)"/) || [])[1];
    const target = {
      closest: (sel) => {
        if (write === undefined) return null;
        if (sel === ".toggle[data-write]" && /class="toggle/.test(html)) {
          const on = /class="toggle on"/.test(html);
          return { dataset: { write }, classList: { contains: (c) => c === "on" && on } };
        }
        if (sel === "td.val.writable[data-write]" && /class="val writable/.test(html)) {
          return absorbing((p) => (p === "dataset" ? { write } : undefined));
        }
        return null;
      },
    };
    posts.length = 0;
    vm.runInContext("editing = null", ctx);
    for (const fn of listeners["tags:click"] || []) fn({ target });
    clicks.push({ key, posts: posts.slice(), editing: vm.runInContext("editing", ctx) });
  }
  process.stdout.write(JSON.stringify({ rows: out, clicks }));
}));
`

type dashClick struct {
	Key   string `json:"key"`
	Posts []struct {
		URL  string         `json:"url"`
		Body map[string]any `json:"body"`
	} `json:"posts"`
	Editing *string `json:"editing"`
}

type dashRow struct {
	Key   string  `json:"key"`
	Kind  string  `json:"kind"`
	Write *string `json:"write"`
	Cell  string  `json:"cell"`
}

// dashboardRows renders tags through the real dashboard script against a
// runtime's real /api/meta, with the given struct paths expanded, and
// returns the table rows keyed by their path.
func dashboardRows(t *testing.T, rt *runtime.Runtime, tags map[string]any, expand ...string) map[string]dashRow {
	t.Helper()
	rows, _ := dashboardRun(t, rt, tags, expand, nil)
	return rows
}

// dashboardRun is dashboardRows plus a click on the value cell of each row
// in clicks, reporting what each click POSTed and whether it opened the
// inline editor.
func dashboardRun(t *testing.T, rt *runtime.Runtime, tags map[string]any, expand, clicks []string) (map[string]dashRow, map[string]dashClick) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed — cannot run the dashboard script")
	}
	rec := httptest.NewRecorder()
	New(rt).Handler().ServeHTTP(rec, httptest.NewRequest("GET", "http://localhost/api/meta", nil))
	var meta any
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	var js strings.Builder
	for _, m := range scriptRe.FindAllSubmatch(indexHTML, -1) {
		js.Write(m[1])
		js.WriteByte('\n')
	}
	in, _ := json.Marshal(map[string]any{"script": js.String(), "meta": meta, "tags": tags, "expand": expand, "clicks": clicks})
	cmd := exec.Command(node, "-e", dashboardHarness)
	cmd.Stdin = strings.NewReader(string(in))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dashboard harness: %v\n%s", err, out)
	}
	var res struct {
		Rows   []dashRow   `json:"rows"`
		Clicks []dashClick `json:"clicks"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("harness output: %v\n%s", err, out)
	}
	byKey := map[string]dashRow{}
	for _, r := range res.Rows {
		byKey[r.Key] = r
	}
	clicked := map[string]dashClick{}
	for _, c := range res.Clicks {
		clicked[c.Key] = c
	}
	return byKey, clicked
}

// Getting started: in the dashboard setpoints are click-to-set (BOOLs by
// toggle) while inputs and outputs stay read-only, because the driver
// rewrites an input and the logic an output within a scan.
func TestDashboardSetpointsEditableInputsAndOutputsReadOnly(t *testing.T) {
	rt, err := runtime.New(runtime.Options{
		Program: `PROGRAM Main
VAR_EXTERNAL Level : REAL; Pump1Run : BOOL; Out : REAL; TankSP : REAL; AutoMode : BOOL; Latch : BOOL; END_VAR
Out := Level;
END_PROGRAM`,
		Driver: nio.NewMemory(),
		Tags: []runtime.TagDef{
			runtime.Input("Level"),
			runtime.Output("Pump1Run", runtime.Init(false)),
			runtime.Output("Out", runtime.Init(0.0)),
			runtime.Setpoint("TankSP", 50.0),
			runtime.Setpoint("AutoMode", true),
			runtime.State("Latch", false),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, clicks := dashboardRun(t, rt, map[string]any{
		"Level": 12.5, "Pump1Run": true, "Out": 12.5,
		"TankSP": 50.0, "AutoMode": true, "Latch": false,
	}, nil, []string{"TankSP", "AutoMode", "Level", "Pump1Run", "Out"})

	// Setpoints and state: a number is click-to-set, a BOOL is a toggle.
	if r := rows["TankSP"]; r.Kind != "num" || !strings.Contains(r.Cell, `title="click to set"`) || !strings.Contains(r.Cell, `data-write="TankSP"`) {
		t.Errorf("setpoint TankSP row = %+v, want a click-to-set number", r)
	}
	if r := rows["AutoMode"]; r.Kind != "toggle" || !strings.Contains(r.Cell, `class="toggle on"`) || !strings.Contains(r.Cell, `data-write="AutoMode"`) {
		t.Errorf("BOOL setpoint AutoMode row = %+v, want a toggle", r)
	}
	if r := rows["Latch"]; r.Kind != "toggle" {
		t.Errorf("BOOL state tag Latch row = %+v, want a toggle", r)
	}
	// Driver-owned inputs and logic-owned outputs: read-only, with the owner named.
	for name, owner := range map[string]string{"Level": "driver-owned input", "Pump1Run": "logic-owned output", "Out": "logic-owned output"} {
		r := rows[name]
		if r.Kind != "plain" {
			t.Errorf("%s row kind = %q, want plain (read-only)", name, r.Kind)
		}
		if strings.Contains(r.Cell, "data-write") || strings.Contains(r.Cell, "click to") {
			t.Errorf("%s has an edit affordance: %s", name, r.Cell)
		}
		if !strings.Contains(r.Cell, owner) {
			t.Errorf("%s cell does not say who owns it (%q): %s", name, owner, r.Cell)
		}
	}

	// Clicking, through the tag table's real click handler: a number opens
	// the inline editor on its tag, a BOOL toggle POSTs the flipped value,
	// and a click on an input or output does neither.
	if c := clicks["TankSP"]; c.Editing == nil || *c.Editing != "TankSP" || len(c.Posts) != 0 {
		t.Errorf("click on setpoint TankSP = %+v, want the inline editor opened on TankSP", c)
	}
	if c := clicks["AutoMode"]; len(c.Posts) != 1 || c.Posts[0].URL != "/api/tags" ||
		c.Posts[0].Body["name"] != "AutoMode" || c.Posts[0].Body["value"] != false {
		t.Errorf("click on BOOL setpoint AutoMode posted %+v, want one POST /api/tags {AutoMode: false}", c.Posts)
	}
	for _, name := range []string{"Level", "Pump1Run", "Out"} {
		if c := clicks[name]; c.Editing != nil || len(c.Posts) != 0 {
			t.Errorf("click on read-only %s = %+v, want no editor and no write", name, c)
		}
	}
}

// Tag-model: the built-in dashboard's tag table makes each leaf of a writable
// struct tag editable, addressed by its dotted path; a driver- or
// logic-owned struct's leaves stay read-only.
func TestDashboardWritableStructLeavesAreEditableByDottedPath(t *testing.T) {
	rt, err := runtime.New(runtime.Options{
		Program:   udtProgram,
		Libraries: []string{udtLib},
		Driver:    nio.NewMemory(),
		Tags: []runtime.TagDef{
			runtime.Typed("Feed", runtime.RoleInput, "Motor"),
			runtime.Typed("P101", runtime.RoleState, "Motor"),
			runtime.Typed("Cmd", runtime.RoleOutput, "Motor", runtime.Init(map[string]any{})),
			runtime.Setpoint("SP", 65.0),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	motor := func() map[string]any {
		return map[string]any{"START": false, "Speed": 1.5, "LVL": map[string]any{"HSP": 80.0, "LSP": 20.0}}
	}
	rows := dashboardRows(t, rt,
		map[string]any{"Feed": motor(), "P101": motor(), "Cmd": motor(), "SP": 65.0},
		"P101", "P101.LVL", "Feed", "Cmd")

	for path, kind := range map[string]string{
		"P101.START": "toggle", "P101.Speed": "num", "P101.LVL.HSP": "num", "P101.LVL.LSP": "num",
	} {
		r, ok := rows[path]
		if !ok {
			t.Errorf("no row for %s; the struct did not expand", path)
			continue
		}
		if r.Kind != kind || r.Write == nil || *r.Write != path {
			t.Errorf("%s = %+v, want an editable %s addressed by %q", path, r, kind, path)
		}
	}
	for _, path := range []string{"Feed.START", "Feed.Speed", "Cmd.START", "Cmd.Speed"} {
		r, ok := rows[path]
		if !ok {
			t.Errorf("no row for %s; the struct did not expand", path)
			continue
		}
		if r.Kind != "plain" || r.Write != nil {
			t.Errorf("%s = %+v, want a read-only leaf (its root is driver/logic-owned)", path, r)
		}
	}
}
