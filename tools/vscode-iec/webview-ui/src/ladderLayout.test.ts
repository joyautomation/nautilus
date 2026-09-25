/// <reference types="node" />
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { L, layoutRung } from './ladderLayout.ts';
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
