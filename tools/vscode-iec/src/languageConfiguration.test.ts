// The language configurations' patterns, built as VS Code builds them: a
// JavaScript RegExp, from a string or from { pattern, flags }. JavaScript has
// no inline flags such as (?i); VS Code drops a pattern it cannot build, with
// only a console warning, and indentation silently stops working (#903).
import { test } from "node:test";
import * as assert from "node:assert/strict";
import * as fs from "node:fs";
import * as path from "node:path";

const root = path.join(__dirname, "..");
type Pattern = string | { pattern: string; flags?: string };
const build = (p: Pattern): RegExp => (typeof p === "string" ? new RegExp(p) : new RegExp(p.pattern, p.flags));

const pkg = JSON.parse(fs.readFileSync(path.join(root, "package.json"), "utf8"));
const files = [...new Set<string>(pkg.contributes.languages.map((l: { configuration?: string }) => l.configuration).filter(Boolean))];

test("every language configuration's patterns build as JavaScript RegExps", () => {
  assert.ok(files.length > 0);
  for (const f of files) {
    const conf = JSON.parse(fs.readFileSync(path.join(root, f), "utf8"));
    const patterns: [string, Pattern | undefined][] = [
      ["wordPattern", conf.wordPattern],
      ["folding.markers.start", conf.folding?.markers?.start],
      ["folding.markers.end", conf.folding?.markers?.end],
      ["indentationRules.increaseIndentPattern", conf.indentationRules?.increaseIndentPattern],
      ["indentationRules.decreaseIndentPattern", conf.indentationRules?.decreaseIndentPattern],
    ];
    for (const [name, p] of patterns) {
      if (p !== undefined) assert.doesNotThrow(() => build(p), `${f} ${name}`);
    }
  }
});

test("Structured Text indents after IF and outdents at END_IF, in any case", () => {
  const conf = JSON.parse(fs.readFileSync(path.join(root, "language-configuration.json"), "utf8"));
  const increase = build(conf.indentationRules.increaseIndentPattern);
  const decrease = build(conf.indentationRules.decreaseIndentPattern);
  for (const line of ["IF x THEN", "if x then", "FUNCTION_BLOCK Motor", "  var_input"]) {
    assert.ok(increase.test(line), `indent after ${JSON.stringify(line)}`);
  }
  assert.ok(!increase.test("IF x THEN y := 1; END_IF;"), "no indent after a one-line IF");
  for (const line of ["END_IF;", "  end_if;", "End_Var"]) {
    assert.ok(decrease.test(line), `outdent at ${JSON.stringify(line)}`);
  }
  assert.ok(!decrease.test("x := 1;"));
});
