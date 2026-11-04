// "Export Diagram…" / "Print Diagram…": which diagram a file or panel is,
// what the export is called, and which paper it lands on. (The drawing is
// tested in webview-ui/src/export/export.test.ts.)

import { strict as assert } from "node:assert";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { test } from "node:test";
import {
  DIAGRAM_VIEW_TYPES,
  diagramLangOf,
  diagramLangOfLanguage,
  diagramLangOfView,
  exportFileName,
  liveValueMap,
  orientationFor,
  paperFor,
  PRINT_DIR_PREFIX,
  printRoute,
  sweepPrintDirs,
  writePrintFile,
} from "./diagramExportLogic";

test("a file's diagram, by extension — an L5X draws as ladder", () => {
  assert.equal(diagramLangOf("/p/permissives.ld"), "ld");
  assert.equal(diagramLangOf("C:\\p\\Line.L5X"), "ld");
  assert.equal(diagramLangOf("/p/level.fbd"), "fbd");
  assert.equal(diagramLangOf("/p/sequence.SFC"), "sfc");
  assert.equal(diagramLangOf("/p/main.st"), undefined);
  assert.equal(diagramLangOf(undefined), undefined);
});

test("a language id's diagram", () => {
  assert.equal(diagramLangOfLanguage("iec-ld"), "ld");
  assert.equal(diagramLangOfLanguage("logix-l5x"), "ld");
  assert.equal(diagramLangOfLanguage("iec-fbd"), "fbd");
  assert.equal(diagramLangOfLanguage("iec-sfc"), "sfc");
  assert.equal(diagramLangOfLanguage("iec-st"), undefined);
});

// The ids as the code registers them: the "Open With" diagram editors from
// package.json, the preview panels from their createWebviewPanel calls.
// Read, not retyped, so a renamed view fails here instead of silently
// losing Export/Print (the FBD preview's id is "nautilusFbdPreview").
const root = path.join(__dirname, "..");
const pkg = JSON.parse(fs.readFileSync(path.join(root, "package.json"), "utf8"));
const editors: { viewType: string; selector: { filenamePattern: string }[] }[] = pkg.contributes.customEditors;
const diagramEditors = new Map<string, string>();
const otherEditors: string[] = [];
for (const e of editors) {
  const lang = e.selector.map((s) => diagramLangOf(s.filenamePattern)).find(Boolean);
  if (lang) diagramEditors.set(e.viewType, lang);
  else otherEditors.push(e.viewType);
}
const panels = new Map<string, string>();
for (const lang of ["ld", "fbd", "sfc"]) {
  const src = fs.readFileSync(path.join(root, "src", `${lang}Preview.ts`), "utf8");
  for (const m of src.matchAll(/createWebviewPanel\(\s*"([^"]+)"/g)) panels.set(m[1], lang);
}

test("the real diagram editors and preview panels are the ones that name their diagram", () => {
  assert.deepEqual([...diagramEditors.keys()].sort(), [
    "nautilus.fbdDiagram",
    "nautilus.l5xDiagram",
    "nautilus.ldDiagram",
    "nautilus.sfcDiagram",
  ]);
  assert.equal(panels.size, 3, `one preview panel per diagram: ${[...panels.keys()]}`);
  assert.ok(panels.has("nautilusFbdPreview"));
  assert.deepEqual({ ...DIAGRAM_VIEW_TYPES }, Object.fromEntries([...diagramEditors, ...panels]));
  for (const [id, lang] of [...diagramEditors, ...panels]) {
    assert.equal(diagramLangOfView(id), lang, id);
    // Some VS Code versions hand a tab's viewType over prefixed.
    assert.equal(diagramLangOfView("mainThreadWebview-" + id), lang, id);
  }
  for (const id of otherEditors) assert.equal(diagramLangOfView(id), undefined, id);
  assert.equal(diagramLangOfView("nautilus.fbdPreview"), undefined);
  assert.equal(diagramLangOfView(undefined), undefined);
});

test("every Export/Print menu's when-clause matches every real diagram view, and no other", () => {
  const ids: Record<string, string[]> = {
    webviewId: [...diagramEditors.keys(), ...panels.keys()],
    activeCustomEditorId: [...diagramEditors.keys()],
    activeWebviewPanelId: [...panels.keys()],
  };
  let checked = 0;
  const menus = pkg.contributes.menus as Record<string, { command: string; when?: string }[]>;
  for (const [menu, items] of Object.entries(menus)) {
    for (const item of items) {
      if (item.command !== "nautilus.diagram.export" && item.command !== "nautilus.diagram.print") continue;
      for (const m of (item.when ?? "").matchAll(/(\w+) =~ \/((?:[^/\\]|\\.)+)\/(\w*)/g)) {
        const re = new RegExp(m[2], m[3]);
        const where = `${menu} ${item.command} ${m[1]}`;
        if (m[1] === "resourceExtname") {
          for (const ext of [".ld", ".fbd", ".sfc", ".l5x", ".L5X", ".L5x", ".FBD"]) assert.ok(re.test(ext), `${where} misses ${ext}`);
          for (const ext of [".st", ".json", ".l5k"]) assert.ok(!re.test(ext), `${where} matches ${ext}`);
          checked++;
          continue;
        }
        if (!ids[m[1]]) continue;
        for (const id of ids[m[1]]) assert.ok(re.test(id), `${where} misses ${id}`);
        for (const id of otherEditors) assert.ok(!re.test(id), `${where} matches ${id}`);
        checked++;
      }
    }
  }
  // webview/context 2, editor/title 2×2, commandPalette 2×2, explorer/context 2
  assert.equal(checked, 12);
});

test("the export sits next to its source, named after it", () => {
  assert.equal(exportFileName("permissives.ld", "pdf"), "permissives.pdf");
  assert.equal(exportFileName("level.fbd", "svg"), "level.svg");
  // An L5X keeps its extension: a controller export beside the source it
  // came from must not collide with that source's own printout.
  assert.equal(exportFileName("Line.L5X", "pdf"), "Line.L5X.pdf");
  assert.equal(exportFileName("Untitled-1", "pdf"), "Untitled-1.pdf");
});

test("paper: an explicit setting wins; auto follows the locale's region", () => {
  assert.equal(paperFor("a4", "en-US"), "a4");
  assert.equal(paperFor("letter", "de-DE"), "letter");
  assert.equal(paperFor("auto", "en-US"), "letter");
  assert.equal(paperFor(undefined, "es-MX"), "letter");
  assert.equal(paperFor("auto", "fr-CA"), "letter");
  assert.equal(paperFor("auto", "en-GB"), "a4");
  assert.equal(paperFor("auto", "zh-Hans-CN"), "a4");
  assert.equal(paperFor("auto", "de"), "a4");
});

test("orientation: anything unrecognised is auto", () => {
  assert.equal(orientationFor("portrait"), "portrait");
  assert.equal(orientationFor("landscape"), "landscape");
  assert.equal(orientationFor("sideways"), "auto");
  assert.equal(orientationFor(undefined), "auto");
});

test("the live value map holds tags and program locals; a tag wins a name clash", () => {
  assert.deepEqual(liveValueMap([["Start", true]], [["t1", { Q: false }]]), { Start: true, t1: { Q: false } });
  assert.deepEqual(liveValueMap([["Run", true]], [["Run", false]]), { Run: true });
});

test("print: a local window opens the viewer; a remote window (SSH, WSL, Codespaces) saves instead", () => {
  assert.equal(printRoute(undefined), "viewer");
  assert.equal(printRoute(""), "viewer");
  assert.equal(printRoute("ssh-remote"), "save");
  assert.equal(printRoute("wsl"), "save");
  assert.equal(printRoute("codespaces"), "save");
});

test("print: each PDF lands in its own private temp directory, never a fixed shared one", async () => {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "naut-print-test-"));
  try {
    // A directory another user pre-created at the old fixed name, pointing
    // elsewhere, is never used.
    const elsewhere = fs.mkdtempSync(path.join(tmp, "elsewhere-"));
    fs.symlinkSync(elsewhere, path.join(tmp, "nautilus-print"));
    const bytes = new Uint8Array([0x25, 0x50, 0x44, 0x46]);
    const a = await writePrintFile("permissives.pdf", bytes, tmp);
    const b = await writePrintFile("permissives.pdf", bytes, tmp);
    assert.notEqual(path.dirname(a), path.dirname(b));
    for (const f of [a, b]) {
      assert.equal(path.basename(f), "permissives.pdf");
      assert.equal(path.dirname(path.dirname(f)), tmp);
      assert.ok(path.basename(path.dirname(f)).startsWith(PRINT_DIR_PREFIX));
      assert.notEqual(path.dirname(f), path.join(tmp, "nautilus-print"));
      assert.deepEqual(new Uint8Array(fs.readFileSync(f)), bytes);
      if (process.platform !== "win32") assert.equal(fs.statSync(path.dirname(f)).mode & 0o077, 0, "the directory is private");
    }
    assert.deepEqual(fs.readdirSync(elsewhere), []);
    // A name with directories in it still lands inside the private dir.
    const c = await writePrintFile("../../escape.pdf", bytes, tmp);
    assert.equal(path.dirname(path.dirname(c)), tmp);
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
});

test("print: the next print sweeps our own print directories once they are an hour old", async () => {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "naut-print-test-"));
  try {
    const bytes = new Uint8Array([1]);
    const old = path.dirname(await writePrintFile("old.pdf", bytes, tmp));
    const fresh = path.dirname(await writePrintFile("fresh.pdf", bytes, tmp));
    const hourAgo = (Date.now() - 2 * 60 * 60 * 1000) / 1000;
    fs.utimesSync(old, hourAgo, hourAgo);
    // A symlink with the prefix is not followed or removed.
    const target = fs.mkdtempSync(path.join(tmp, "target-"));
    fs.symlinkSync(target, path.join(tmp, PRINT_DIR_PREFIX + "link"));
    const unrelated = fs.mkdtempSync(path.join(tmp, "other-"));
    fs.utimesSync(unrelated, hourAgo, hourAgo);

    assert.deepEqual(await sweepPrintDirs(tmp), [old]);
    assert.ok(!fs.existsSync(old));
    assert.ok(fs.existsSync(path.join(fresh, "fresh.pdf")));
    assert.ok(fs.existsSync(target));
    assert.ok(fs.existsSync(unrelated));
    assert.deepEqual(await sweepPrintDirs(path.join(tmp, "missing")), []);
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
});
