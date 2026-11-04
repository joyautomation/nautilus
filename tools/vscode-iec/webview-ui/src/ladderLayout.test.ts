/// <reference types="node" />
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { L, layoutRung, fitArgs, splitArgs, fbWidth, FB_ARGS_MAX, assignLines, ASSIGN_LINE_MAX, ASSIGN_LINE_H } from './ladderLayout.ts';
import type { Ann } from './ladder.ts';

// The rung's SVG height must reserve enough room below a changed/removed
// element for its "was …" (or "removed") diff label — otherwise the label
// draws past the rung's own box and collides with whatever renders next
// (a comment note, or the following rung). See LadderView.svelte's
// `.dwas` text at local y = n.h + 24.

function contact(overrides: Partial<Ann['el']> = {}): Ann {
	return { el: { kind: 'contact', ref: 'X', ...overrides }, in: true, out: true, val: true };
}

function ton(overrides: Partial<Ann['el']> = {}): Ann {
	return {
		el: { kind: 'fb', inst: 't1', type: 'TON', args: 'PT := T#3S, ET => HiSecs', ...overrides },
		in: true,
		out: true
	};
}

test('ladderLayout: a plain rung reserves no extra room for a diff label', () => {
	const plain = layoutRung([contact()], [], 800);
	const changed = layoutRung([contact({ _diff: 'changed', _was: 'GT(TempC, 90.0)' })], [], 800);
	assert.ok(changed.height > plain.height, 'a changed contact should grow the rung to fit its "was" label');
});

test('ladderLayout: the "was" label under a changed FB fits inside the rung box', () => {
	const plain = layoutRung([ton()], [], 800);
	const changed = layoutRung([ton({ _diff: 'changed', _was: 't1:TON(PT:=T#3S...T=>HiSecs)' })], [], 800);
	assert.ok(
		changed.height >= plain.height + L.LABEL_WAS - 10,
		`expected the changed rung (${changed.height}) to grow by roughly LABEL_WAS to fit the "was" label under the FB box (plain: ${plain.height})`
	);
	// The node's own box bottom, plus the label's y-offset (+24) and its own
	// line height, must land at or before the rung's bottom edge.
	const node = changed.nodes.find((n) => n.kind === 'fb')!;
	const labelBottom = node.y + node.h + 24 + 10;
	assert.ok(labelBottom <= changed.height, `label bottom (${labelBottom}) should fit within rung height (${changed.height})`);
});

test('ladderLayout: a removed coil also reserves room for its "removed" label', () => {
	const plain = layoutRung([], [contact({ kind: 'coil' })], 800);
	const removed = layoutRung([], [contact({ kind: 'coil', _diff: 'removed' })], 800);
	assert.ok(removed.height > plain.height, 'a removed coil should grow the rung to fit its "removed" label');
});

// The FB box's args line: whole arguments or none — the old middle cut drew
// `PT := T#5S…T => HiSecs` (half of `ET` spliced onto the first argument).
test('fitArgs: a TON args line that fits is drawn whole', () => {
	assert.equal(fitArgs('PT := T#5S, ET => HiSecs'), 'PT := T#5S, ET => HiSecs');
	assert.equal(fitArgs('PT:=T#5S ,  ET=>HiSecs'), 'PT:=T#5S, ET=>HiSecs');
	assert.equal(fitArgs(undefined), '');
});

test('fitArgs: too long → whole arguments then ", …", never a mid-argument cut', () => {
	const args = 'PT := T#5S, ET => HiSecs, Q => Done_Flag_Long, IN := Start_Request';
	const got = fitArgs(args, 30);
	assert.equal(got, 'PT := T#5S, ET => HiSecs, …');
	assert.ok(got.length <= 30);
	for (const part of got.replace(/, …$/, '').split(', ')) assert.ok(args.includes(part), part);
});

test('fitArgs: a lone argument longer than the line is cut at its end', () => {
	const got = fitArgs('PT := SomeVeryLongConfigStruct.Timers.PumpDelay', 20);
	assert.equal(got, 'PT := SomeVeryLongC…');
	assert.equal(got.length, 20);
});

test('splitArgs: commas inside a nested call or a string stay in their argument', () => {
	assert.deepEqual(splitArgs("IN := MAX(A, B), MSG := 'a, b', PT := T#1S"), [
		'IN := MAX(A, B)',
		"MSG := 'a, b'",
		'PT := T#1S'
	]);
});

test('fbWidth: the box is sized for the args line it draws (capped)', () => {
	const short = fbWidth(ton({ args: 'PT := T#5S, ET => HiSecs' }));
	const huge = fbWidth(ton({ args: Array.from({ length: 20 }, (_, i) => `A${i} := ${i}`).join(', ') }));
	assert.ok(short >= Math.ceil('PT := T#5S, ET => HiSecs'.length * 9 * 0.62) + 20);
	assert.ok(huge <= Math.ceil(FB_ARGS_MAX * 9 * 0.62) + 20 + 1);
});

// #563: a multi-statement `{ … }` box stacks one statement per line and is
// sized to contain them — it used to cap at 260px and let the one long line
// spill over its neighbours and off the rung's left edge.
const assign = (text: string): Ann => ({ el: { kind: 'assign', text }, in: true, out: true });
const CHAR_W = 10 * 0.62; // textW's monospace estimate at the 10px assign font

test('ladderLayout: assignLines splits on top-level semicolons only', () => {
	assert.deepEqual(assignLines('a := 1'), ['a := 1']);
	assert.deepEqual(assignLines('a := 1; b := 2;'), ['a := 1;', 'b := 2']);
	assert.deepEqual(assignLines("s := 'x;y'; n := 1"), ["s := 'x;y';", 'n := 1']);
	const long = assignLines('x := ' + 'y + '.repeat(30) + 'z');
	assert.equal(long[0].length, ASSIGN_LINE_MAX);
	assert.ok(long[0].endsWith('…'));
});

test('ladderLayout: a multi-statement assign box contains every line and stays on the rung', () => {
	const text = 'Count := Count + 1; Target := 5; Big := ABS(Big); Level := (Level + HiSP) / 2.0';
	const lay = layoutRung([contact({ ref: 'Start' }), assign(text), contact({ ref: 'Stop' })], [contact({ kind: 'coil', ref: 'Ready' })], 900);
	const box = lay.nodes.find((n) => n.kind === 'assign')!;
	const lines = assignLines(text);
	assert.equal(lines.length, 4);
	assert.ok(box.w >= Math.max(...lines.map((l) => l.length)) * CHAR_W, `box ${box.w}px too narrow for its longest line`);
	assert.ok(box.h >= L.FN_H + (lines.length - 1) * ASSIGN_LINE_H, `box ${box.h}px too short for ${lines.length} lines`);
	assert.ok(box.x >= L.RAIL_LEFT, 'box must not start left of the rung');
	assert.ok(box.y >= L.RUNG_PAD_Y, 'box top must stay inside the rung');
	assert.ok(box.y + box.h <= lay.height, 'box bottom must stay inside the rung');
	for (const n of lay.nodes) {
		if (n === box) continue;
		assert.ok(n.x + n.w <= box.x || n.x >= box.x + box.w, `${n.kind} overlaps the assign box`);
	}
});
