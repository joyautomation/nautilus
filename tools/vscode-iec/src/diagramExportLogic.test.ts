// "Export Diagram…" / "Print Diagram…": which diagram a file or panel is,
// what the export is called, and which paper it lands on. (The drawing is
// tested in webview-ui/src/export/export.test.ts.)

import { strict as assert } from "node:assert";
import { test } from "node:test";
import {
  diagramLangOf,
  diagramLangOfLanguage,
  diagramLangOfView,
  exportFileName,
  liveValueMap,
  orientationFor,
  paperFor,
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

test("the preview panels and diagram editors name their diagram", () => {
  assert.equal(diagramLangOfView("nautilus.ldPreview"), "ld");
  assert.equal(diagramLangOfView("mainThreadWebview-nautilus.fbdPreview"), "fbd");
  assert.equal(diagramLangOfView("nautilus.sfcDiagram"), "sfc");
  assert.equal(diagramLangOfView("nautilus.l5xDiagram"), "ld");
  assert.equal(diagramLangOfView("nautilus.mimicEditor"), undefined);
  assert.equal(diagramLangOfView(undefined), undefined);
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
