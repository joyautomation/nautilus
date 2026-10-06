// Plain-Node tests for the declared-type lookup behind enum live values
// (#246). Run via `npm test`.

import { test } from "node:test";
import * as assert from "node:assert/strict";
import {
  enumPickItems,
  enumText,
  enumValueName,
  flattenMetaTypes,
  isEnum,
  isStringType,
  parseEnumWrite,
  typedEnumPick,
  typeFor,
  typeKey,
  typeLabel,
} from "./tagTypes";
import { formatValue, formatValueHover } from "./scan";

const MODE = [
  { name: "Idle", value: 0 },
  { name: "Run", value: 10 },
  { name: "Fault", value: 11 },
];

// The /api/meta shape server/tagtypes.go writes.
const META = {
  tags: {
    Cmd: { desc: "Operating mode", type: "Mode", enum: MODE },
    Label: { type: "STRING" },
    Speed: { unit: "Hz", type: "REAL" },
    Undocumented: { desc: "no type" },
    P101: { type: "Pump", members: { State: { type: "Mode", enum: MODE } } },
    Line: { type: "ARRAY OF Pump", elem: { type: "Pump", members: { State: { type: "Mode", enum: MODE } } } },
  },
  locals: { last: { type: "Mode", enum: MODE }, label: { type: "INT" } },
};

test("flattenMetaTypes: tags, locals, struct members and array elements, case-insensitive", () => {
  const t = flattenMetaTypes(META);
  assert.equal(typeFor(t, "cmd")?.t, "Mode");
  assert.deepEqual(typeFor(t, "CMD")?.e, MODE);
  assert.equal(typeFor(t, "Label")?.t, "STRING", "a tag beats a same-named local");
  assert.equal(typeFor(t, "Label")?.e, undefined);
  assert.equal(typeFor(t, "Undocumented"), undefined, "no type, no entry");
  assert.equal(typeFor(t, "P101")?.t, "Pump");
  assert.equal(typeFor(t, "p101.state")?.t, "Mode");
  assert.equal(typeFor(t, "Line[3].State")?.t, "Mode");
  assert.equal(typeFor(t, "last")?.t, "Mode");
  assert.equal(typeKey("Line[i + 1].State"), "line[].state");
});

test("flattenMetaTypes: an older controller (no types) or junk gives an empty lookup", () => {
  assert.deepEqual(flattenMetaTypes({ tags: { Level: { desc: "x", unit: "%" } } }), {});
  assert.deepEqual(flattenMetaTypes(null), {});
  assert.deepEqual(flattenMetaTypes("nope"), {});
  assert.deepEqual(flattenMetaTypes({ tags: { A: null, B: { type: 3 } } }), {});
});

test("enumText: an enumerated value reads bare; a STRING keeps its quotes", () => {
  const t = flattenMetaTypes(META);
  assert.equal(enumText("Run", typeFor(t, "Cmd")), "Run");
  assert.equal(enumText(10, typeFor(t, "Cmd")), "Run", "an integer (a force by number) shows as its member");
  assert.equal(enumText(99, typeFor(t, "Cmd")), undefined, "no member has 99: the number stands");
  assert.equal(enumText("Run", typeFor(t, "Label")), undefined, "a STRING tag is not an enum");
  assert.equal(enumText("Run", undefined), undefined, "unknown type: render as before");
  // What every surface shows, end to end:
  const show = (v: unknown, p: string) => enumText(v, typeFor(t, p)) ?? formatValue(v);
  assert.equal(show("Run", "Cmd"), "Run");
  assert.equal(show("Run", "Label"), '"Run"');
});

test("typeLabel: `Mode · enum` for an enumeration, the name otherwise", () => {
  const t = flattenMetaTypes(META);
  assert.equal(typeLabel(typeFor(t, "Cmd")), "Mode · enum");
  assert.equal(typeLabel(typeFor(t, "Speed")), "REAL");
  assert.equal(typeLabel(undefined), undefined);
});

test("formatValueHover: an enumerated member of a struct reads bare", () => {
  const t = flattenMetaTypes(META);
  const out = formatValueHover({ State: "Run", Hz: 50.5, Note: "Run" }, (v, p) => enumText(v, typeFor(t, "P101" + p)));
  assert.equal(out, ["{", "  State: Run", "  Hz: 50.500", '  Note: "Run"', "}"].join("\n"));
  assert.equal(formatValueHover("Run"), '"Run"', "no leaf formatter: unchanged");
});

test("enumPickItems: every member, its integer, the current one marked", () => {
  const ft = typeFor(flattenMetaTypes(META), "Cmd");
  assert.ok(isEnum(ft));
  const items = enumPickItems(ft, "Run");
  assert.deepEqual(
    items.map((i) => [i.label, i.description, i.value, !!i.current]),
    [
      ["Idle", "= 0", 0, false],
      ["Run", "= 10 · current", 10, true],
      ["Fault", "= 11", 11, false],
    ]
  );
});

test("parseEnumWrite: a member (any case, Type#-qualified) or an integer; nothing else", () => {
  const ft = typeFor(flattenMetaTypes(META), "Cmd");
  assert.ok(isEnum(ft));
  assert.equal(parseEnumWrite("Run", ft), 10);
  assert.equal(parseEnumWrite(" fault ", ft), 11);
  assert.equal(parseEnumWrite("Mode#Idle", ft), 0);
  assert.equal(parseEnumWrite("Other#Idle", ft), undefined, "another type's qualifier");
  assert.equal(parseEnumWrite("10", ft), 10);
  assert.equal(parseEnumWrite("-3", ft), -3, "an integer no member has is still the operator's to send");
  assert.equal(parseEnumWrite("Running", ft), undefined);
  assert.equal(parseEnumWrite("1.5", ft), undefined);
  assert.equal(parseEnumWrite("TRUE", ft), undefined);
  assert.equal(parseEnumWrite("", ft), undefined);
});

test("typedEnumPick: a typed integer or qualified name gets its own row; a plain member does not", () => {
  const ft = typeFor(flattenMetaTypes(META), "Cmd");
  assert.ok(isEnum(ft));
  assert.deepEqual(typedEnumPick("10", ft), { label: "10", description: "→ Run", value: 10, alwaysShow: true });
  assert.equal(typedEnumPick("42", ft)?.description, "integer, no member of Mode has it");
  assert.equal(typedEnumPick("Mode#Fault", ft)?.value, 11);
  assert.equal(typedEnumPick("run", ft), undefined, "already a row");
  assert.equal(typedEnumPick("Ru", ft), undefined, "a filter prefix is not a value");
  assert.equal(enumValueName(11, ft), "Fault");
  assert.equal(enumValueName(42, ft), "42");
});

test("parseTypedWrite: the Set/Force box by the target's type — a quoted STRING, an enum member or integer, else number/BOOL", async () => {
  const { parseTypedWrite, typedWriteHint } = await import("./scan");
  const t = flattenMetaTypes(META);
  const str = typeFor(t, "Label");
  assert.equal(parseTypedWrite('"Run"', str), "Run", "the quotes the pill shows");
  assert.equal(parseTypedWrite("'two words'", str), "two words");
  assert.equal(parseTypedWrite('""', str), "", "an empty STRING");
  assert.equal(parseTypedWrite("Run", str), undefined, "unquoted text is refused: a quote says it is text");
  assert.equal(parseTypedWrite("12", str), undefined);
  assert.equal(parseTypedWrite("'mismatched\"", str), undefined);
  assert.equal(typedWriteHint(str), "a quoted string, 'text'");
  const en = typeFor(t, "Cmd");
  assert.equal(parseTypedWrite("run", en), "Run", "a member by name, sent as its name");
  assert.equal(parseTypedWrite("Mode#Fault", en), "Fault");
  assert.equal(parseTypedWrite("10", en), 10, "or its integer");
  assert.equal(parseTypedWrite("Running", en), undefined);
  assert.match(typedWriteHint(en), /member of Mode \(Idle, Run, Fault\)/);
  const real = typeFor(t, "Speed");
  assert.equal(parseTypedWrite("12.5", real), 12.5);
  assert.equal(parseTypedWrite("TRUE", undefined), true, "unknown type: as before");
  assert.equal(parseTypedWrite('"x"', real), undefined);
  assert.equal(isStringType({ t: "STRING[20]" }), true);
  assert.equal(isStringType({ t: "WSTRING" }), true);
  assert.equal(isStringType({ t: "Mode", e: [] }), false);
});
