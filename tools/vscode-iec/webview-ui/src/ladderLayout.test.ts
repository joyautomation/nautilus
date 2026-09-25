/// <reference types="node" />
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { L, layoutRung, fitArgs, splitArgs, fbWidth, FB_ARGS_MAX } from './ladderLayout.ts';
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
