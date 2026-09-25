/// <reference types="node" />
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
	attachedTransitions,
	cascadeDeleteOps,
	clipSteps,
	deleteStepsOp,
	pasteStepsOp,
	commentId,
	computeRanksAndColumns,
	connectHandlePos,
	diffSfc,
	layoutSfc,
	stepAtPoint,
	stepId,
	type SfcModel
} from './sfc.ts';

// A minimal linear chart: Idle -> Fill -> Drain -> Idle (a back edge).
function linearModel(): SfcModel {
	return {
		name: 'Linear',
		steps: [
			{ id: 'st:Idle', name: 'Idle', initial: true, line: 1, endLine: 2 },
			{ id: 'st:Fill', name: 'Fill', initial: false, line: 3, endLine: 4 },
			{ id: 'st:Drain', name: 'Drain', initial: false, line: 5, endLine: 6 }
		],
		trans: [
			{ id: 'tr:t1', from: ['Idle'], to: ['Fill'], cond: 'Start', kind: 'normal', line: 7, endLine: 8 },
			{ id: 'tr:t2', from: ['Fill'], to: ['Drain'], cond: 'Full', kind: 'normal', line: 9, endLine: 10 },
			{ id: 'tr:t3', from: ['Drain'], to: ['Idle'], cond: 'Empty', kind: 'normal', line: 11, endLine: 12 }
		]
	};
}

// The docs/design/sfc.md §1.2 TankBatch chart, in render-model shape: an
// alternative divergence (Fill -> Idle vs Fill -> (Heat, Mix)), a
// simultaneous divergence + matching convergence.
function tankBatchModel(): SfcModel {
	const steps: SfcModel['steps'] = [
		{ id: 'st:Idle', name: 'Idle', initial: true, line: 1, endLine: 2 },
		{ id: 'st:Fill', name: 'Fill', initial: false, line: 3, endLine: 4 },
		{ id: 'st:Heat', name: 'Heat', initial: false, line: 5, endLine: 6 },
		{ id: 'st:Mix', name: 'Mix', initial: false, line: 7, endLine: 8 },
		{ id: 'st:Drain', name: 'Drain', initial: false, line: 9, endLine: 10 }
	];
	const trans: SfcModel['trans'] = [
		{ id: 'tr:t_start', name: 't_start', from: ['Idle'], to: ['Fill'], cond: 'Start', kind: 'normal', line: 11, endLine: 12 },
		{ id: 'tr:t_abort', name: 't_abort', from: ['Fill'], to: ['Idle'], cond: 'Abort', kind: 'alt', line: 13, endLine: 14 },
		{ id: 'tr:t_full', name: 't_full', from: ['Fill'], to: ['Heat', 'Mix'], cond: 'Level >= FillSP', kind: 'simDiverge', line: 15, endLine: 16 },
		{ id: 'tr:t_done', name: 't_done', from: ['Heat', 'Mix'], to: ['Drain'], cond: 'TempC >= HeatSP', kind: 'simConverge', line: 17, endLine: 18 },
		{ id: 'tr:t_empty', name: 't_empty', from: ['Drain'], to: ['Idle'], cond: 'Level <= EmptySP', kind: 'normal', line: 19, endLine: 20 }
	];
	return { name: 'TankBatch', steps, trans };
}

test('computeRanksAndColumns: linear chart ranks steps 0,1,2 and the back edge does not affect rank', () => {
	const { rankOf } = computeRanksAndColumns(linearModel());
	assert.equal(rankOf.get('st:Idle'), 0);
	assert.equal(rankOf.get('st:Fill'), 1);
	assert.equal(rankOf.get('st:Drain'), 2);
});

test('computeRanksAndColumns: TankBatch ranks the simultaneous divergence/convergence correctly', () => {
	const { rankOf } = computeRanksAndColumns(tankBatchModel());
	assert.equal(rankOf.get('st:Idle'), 0);
	assert.equal(rankOf.get('st:Fill'), 1);
	// Heat and Mix are both discovered from Fill via the SAME simDiverge
	// transition, at the same rank.
	assert.equal(rankOf.get('st:Heat'), 2);
	assert.equal(rankOf.get('st:Mix'), 2);
	// Drain is discovered via the simConverge transition, one past both.
	assert.equal(rankOf.get('st:Drain'), 3);
});

test('computeRanksAndColumns: parallel branches get distinct, ordered columns', () => {
	const { colOf } = computeRanksAndColumns(tankBatchModel());
	const heat = colOf.get('st:Heat')!;
	const mix = colOf.get('st:Mix')!;
	assert.notEqual(heat, mix);
	// Heat is declared (and appears in the transition's To list) before Mix.
	assert.ok(heat < mix);
});

test('computeRanksAndColumns: an unreachable step still gets a slot, not dropped', () => {
	const m = linearModel();
	m.steps.push({ id: 'st:Stray', name: 'Stray', initial: false, line: 20, endLine: 21 });
	const { rankOf, colOf } = computeRanksAndColumns(m);
	assert.ok(rankOf.has('st:Stray'));
	assert.ok(colOf.has('st:Stray'));
	// Placed below every reachable step's rank.
	assert.ok(rankOf.get('st:Stray')! > rankOf.get('st:Drain')!);
});

test('layoutSfc: every step gets a unique, non-overlapping auto position (no @layout pins)', () => {
	const layout = layoutSfc(tankBatchModel());
	assert.equal(layout.steps.length, 5);
	const seen = new Set<string>();
	for (const p of layout.steps) {
		const key = `${p.x},${p.y}`;
		assert.ok(!seen.has(key), `duplicate position at ${key}`);
		seen.add(key);
	}
});

test('layoutSfc: a pinned step (@layout) overrides its auto position', () => {
	const m = tankBatchModel();
	m.layout = { 'st:Fill': { x: 999, y: 888 } };
	const layout = layoutSfc(m);
	const fill = layout.steps.find((p) => p.id === 'st:Fill')!;
	assert.equal(fill.x, 999);
	assert.equal(fill.y, 888);
	assert.equal(fill.pinned, true);
	const idle = layout.steps.find((p) => p.id === 'st:Idle')!;
	assert.equal(idle.pinned, false);
});

test('layoutSfc: forward transitions (t_start) get bar+stem geometry, not a jump', () => {
	const layout = layoutSfc(tankBatchModel());
	const tStart = layout.trans.find((r) => r.t.id === 'tr:t_start')!;
	assert.equal(tStart.jump, undefined);
	assert.equal(tStart.legsIn.length, 1);
	assert.equal(tStart.legsOut.length, 1);
});

test('layoutSfc: the simultaneous divergence fans one bar out to two legs, marked double', () => {
	const layout = layoutSfc(tankBatchModel());
	const tFull = layout.trans.find((r) => r.t.id === 'tr:t_full')!;
	assert.equal(tFull.double, true);
	assert.equal(tFull.legsIn.length, 1);
	assert.equal(tFull.legsOut.length, 2);
	assert.ok(tFull.barX2 > tFull.barX1); // spans both target columns
});

test('layoutSfc: the simultaneous convergence gathers two legs into one bar, marked double', () => {
	const layout = layoutSfc(tankBatchModel());
	const tDone = layout.trans.find((r) => r.t.id === 'tr:t_done')!;
	assert.equal(tDone.double, true);
	assert.equal(tDone.legsIn.length, 2);
	assert.equal(tDone.legsOut.length, 1);
});

test('layoutSfc: a back edge (t_abort, t_empty) renders as a jump instead of a long forward line', () => {
	const layout = layoutSfc(tankBatchModel());
	const tAbort = layout.trans.find((r) => r.t.id === 'tr:t_abort')!;
	assert.ok(tAbort.jump);
	assert.equal(tAbort.legsIn.length, 0);
	assert.ok(tAbort.jump!.label.includes('Idle'));

	const tEmpty = layout.trans.find((r) => r.t.id === 'tr:t_empty')!;
	assert.ok(tEmpty.jump);
});

test('layoutSfc: a dangling transition reference (post deleteStep) is NOT dropped from trans, and NOT rendered as a normal route — it becomes an anchored orphan chip', () => {
	const m = linearModel();
	m.trans.push({ id: 'tr:bad', from: ['Ghost'], to: ['Idle'], cond: 'X', kind: 'normal', line: 30, endLine: 31 });
	assert.doesNotThrow(() => layoutSfc(m));
	const layout = layoutSfc(m);
	// Not drawn as a normal bar+stem route (its FROM doesn't resolve)...
	assert.equal(layout.trans.some((r) => r.t.id === 'tr:bad'), false);
	// ...but visible, selectable, deletable as an orphan chip, anchored near
	// its surviving endpoint (TO Idle resolves).
	assert.equal(layout.orphans.length, 1);
	const chip = layout.orphans[0];
	assert.equal(chip.t.id, 'tr:bad');
	assert.ok(chip.anchor);
	assert.equal(chip.anchor!.id, 'st:Idle');
});

test('layoutSfc: a transition where NEITHER end resolves goes into the unanchored bottom strip', () => {
	const m = linearModel();
	m.trans.push({ id: 'tr:bad', from: ['Ghost1'], to: ['Ghost2'], cond: 'X', kind: 'normal', line: 30, endLine: 31 });
	const layout = layoutSfc(m);
	assert.equal(layout.orphans.length, 1);
	assert.equal(layout.orphans[0].anchor, undefined);
	// Placed below every step, not overlapping the chart.
	const maxStepBottom = Math.max(...layout.steps.map((p) => p.y + p.h));
	assert.ok(layout.orphans[0].y > maxStepBottom);
});

test('layoutSfc: two orphans anchored to the same surviving step stack, not overlap', () => {
	const m = linearModel();
	m.trans.push({ id: 'tr:bad1', from: ['Ghost1'], to: ['Idle'], cond: 'X', kind: 'normal', line: 30, endLine: 31 });
	m.trans.push({ id: 'tr:bad2', from: ['Ghost2'], to: ['Idle'], cond: 'Y', kind: 'normal', line: 32, endLine: 33 });
	const layout = layoutSfc(m);
	assert.equal(layout.orphans.length, 2);
	assert.notEqual(layout.orphans[0].y, layout.orphans[1].y);
});

test('layoutSfc: a comment reserves a top notes band that pushes step rank 0 down', () => {
	const withoutNotes = layoutSfc(linearModel());
	const m = linearModel();
	m.comments = [{ line: 1, endLine: 1, text: 'a note' }];
	const withNotes = layoutSfc(m);
	const idleWithout = withoutNotes.steps.find((p) => p.id === 'st:Idle')!;
	const idleWith = withNotes.steps.find((p) => p.id === 'st:Idle')!;
	assert.ok(idleWith.y > idleWithout.y);
	assert.equal(withNotes.notes.length, 1);
	assert.equal(withNotes.notes[0].index, 0);
});

test('stepId mirrors the Go id convention (st:<Name>)', () => {
	assert.equal(stepId('Idle'), 'st:Idle');
});

// ── drag-to-connect hit testing ─────────────────────────────────────────

test('stepAtPoint finds the step box containing a canvas point, else undefined', () => {
	const layout = layoutSfc(linearModel());
	const idle = layout.steps.find((p) => p.id === 'st:Idle')!;
	const inside = stepAtPoint(layout, idle.x + 5, idle.y + 5);
	assert.equal(inside?.id, 'st:Idle');
	const outside = stepAtPoint(layout, -1000, -1000);
	assert.equal(outside, undefined);
});

test('connectHandlePos sits at the bottom-center of the step box', () => {
	const layout = layoutSfc(linearModel());
	const idle = layout.steps.find((p) => p.id === 'st:Idle')!;
	const pos = connectHandlePos(idle);
	assert.equal(pos.x, idle.x + idle.w / 2);
	assert.equal(pos.y, idle.y + idle.h);
});

// ── delete-step cascade ──────────────────────────────────────────────────

test('attachedTransitions finds transitions naming the step in FROM or TO, case-insensitively', () => {
	const m = linearModel();
	const attached = attachedTransitions(m, 'fill');
	assert.deepEqual(
		attached.map((t) => t.id).sort(),
		['tr:t1', 'tr:t2'] // t1: Idle->Fill, t2: Fill->Drain
	);
	assert.equal(attachedTransitions(m, 'NoSuchStep').length, 0);
});

test('cascadeDeleteOps deletes strictly bottom-of-file-first, so an unnamed transition (line-addressed) never goes stale mid-cascade, and the step is always last', () => {
	const step = { id: 'st:Fill', name: 'Fill', initial: false, line: 3, endLine: 4 };
	const transitions = [
		{ id: 'tr:1', from: ['Idle'], to: ['Fill'], cond: 'X', kind: 'normal' as const, line: 10, endLine: 11 },
		{ id: 'tr:2', from: ['Fill'], to: ['Drain'], cond: 'Y', kind: 'normal' as const, line: 20, endLine: 21 }
	];
	const ops = cascadeDeleteOps(step, transitions);
	// Highest line (tr:2 @ 20) first, then tr:1 @ 10, then the step @ 3 last.
	assert.deepEqual(ops, [
		{ type: 'deleteTransition', transition: 'tr:2' },
		{ type: 'deleteTransition', transition: 'tr:1' },
		{ type: 'deleteStep', step: 'st:Fill' }
	]);
});

test('cascadeDeleteOps: a step with no attached transitions is just the one deleteStep op', () => {
	const step = { id: 'st:Lonely', name: 'Lonely', initial: false, line: 3, endLine: 4 };
	assert.deepEqual(cascadeDeleteOps(step, []), [{ type: 'deleteStep', step: 'st:Lonely' }]);
});

// ── comment layout pinning (drag/pin parity with FBD's diagram notes) ────

test('commentId mirrors the Go id convention (cm:<index>)', () => {
	assert.equal(commentId(0), 'cm:0');
	assert.equal(commentId(3), 'cm:3');
});

test('layoutSfc: a pinned comment (@layout cm:N) overrides its auto position', () => {
	const m = linearModel();
	m.comments = [{ line: 1, endLine: 1, text: 'a note' }];
	m.layout = { [commentId(0)]: { x: 500, y: 700 } };
	const layout = layoutSfc(m);
	assert.equal(layout.notes.length, 1);
	assert.equal(layout.notes[0].x, 500);
	assert.equal(layout.notes[0].y, 700);
	assert.equal(layout.notes[0].pinned, true);
});

test('layoutSfc: an unpinned comment auto-positions and reports pinned=false', () => {
	const m = linearModel();
	m.comments = [{ line: 1, endLine: 1, text: 'a note' }];
	const layout = layoutSfc(m);
	assert.equal(layout.notes[0].pinned, false);
});

// ── diffSfc (visual diff overlay) ─────────────────────────────────────────

test('diffSfc: a step present only in head is marked added', () => {
	const base = linearModel();
	const head = linearModel();
	head.steps.push({ id: 'st:New', name: 'New', initial: false, line: 30, endLine: 31 });
	const merged = diffSfc(base, head);
	const added = merged.steps.find((s) => s.id === 'st:New')!;
	assert.equal(added.status, 'added');
	// Untouched steps carry no status (rendered as plain/"same").
	const idle = merged.steps.find((s) => s.id === 'st:Idle')!;
	assert.equal(idle.status, undefined);
});

test('diffSfc: a step present only in base is spliced back in, marked removed', () => {
	const base = linearModel();
	const head = linearModel();
	head.steps = head.steps.filter((s) => s.id !== 'st:Fill');
	head.trans = head.trans.filter((t) => t.from[0] !== 'Fill' && t.to[0] !== 'Fill');
	const merged = diffSfc(base, head);
	const removed = merged.steps.find((s) => s.id === 'st:Fill');
	assert.ok(removed);
	assert.equal(removed!.status, 'removed');
});

test('diffSfc: a step whose action associations changed is marked changed, not added/removed', () => {
	const base = linearModel();
	const head = linearModel();
	head.steps = head.steps.map((s) =>
		s.id === 'st:Idle' ? { ...s, actions: [{ qualifier: 'N', target: 'Lamp', line: 2 }] } : s
	);
	const merged = diffSfc(base, head);
	const idle = merged.steps.find((s) => s.id === 'st:Idle')!;
	assert.equal(idle.status, 'changed');
});

test('diffSfc: an unchanged transition carries no status; a changed condition is "changed"', () => {
	const base = linearModel();
	const head = linearModel();
	head.trans = head.trans.map((t) => (t.id === 'tr:t1' ? { ...t, cond: 'Different' } : t));
	const merged = diffSfc(base, head);
	assert.equal(merged.trans.find((t) => t.id === 'tr:t1')!.status, 'changed');
	assert.equal(merged.trans.find((t) => t.id === 'tr:t2')!.status, undefined);
});

test('diffSfc: an added transition is "added"; a removed one is spliced back in as "removed"', () => {
	const base = linearModel();
	const head = linearModel();
	head.trans = head.trans.filter((t) => t.id !== 'tr:t3');
	head.trans.push({ id: 'tr:extra', from: ['Idle'], to: ['Drain'], cond: 'X', kind: 'normal', line: 40, endLine: 41 });
	const merged = diffSfc(base, head);
	assert.equal(merged.trans.find((t) => t.id === 'tr:extra')!.status, 'added');
	assert.equal(merged.trans.find((t) => t.id === 'tr:t3')!.status, 'removed');
});

test('diffSfc: comments match by text — identical notes stay unchanged, a new one is added, a dropped one is removed', () => {
	const base: SfcModel = { ...linearModel(), comments: [{ line: 1, endLine: 1, text: 'keep me' }, { line: 2, endLine: 2, text: 'drop me' }] };
	const head: SfcModel = { ...linearModel(), comments: [{ line: 1, endLine: 1, text: 'keep me' }, { line: 2, endLine: 2, text: 'brand new' }] };
	const merged = diffSfc(base, head);
	const kept = merged.comments!.find((c) => c.text === 'keep me')!;
	assert.equal(kept.status, undefined);
	const added = merged.comments!.find((c) => c.text === 'brand new')!;
	assert.equal(added.status, 'added');
	const removed = merged.comments!.find((c) => c.text === 'drop me')!;
	assert.equal(removed.status, 'removed');
});

test('diffSfc: an identical model diffs with no added/removed/changed elements', () => {
	const m = linearModel();
	const merged = diffSfc(m, m);
	for (const s of merged.steps) assert.equal(s.status, undefined);
	for (const t of merged.trans) assert.equal(t.status, undefined);
});

// The shipped example (examples/tank-batch-sfc): abort goes FORWARD to its
// own Aborted step, so Fill has two forward exits — an alternative
// divergence — and Drain carries three actions.
function tankBatchWithAborted(): SfcModel {
	const m = tankBatchModel();
	m.steps.push({ id: 'st:Aborted', name: 'Aborted', initial: false, line: 21, endLine: 22 });
	m.trans.find((t) => t.id === 'tr:t_abort')!.to = ['Aborted'];
	m.steps.find((s) => s.id === 'st:Drain')!.actions = [
		{ qualifier: 'N', target: 'RunLamp' },
		{ qualifier: 'N', target: 'DrainValve' },
		{ qualifier: 'P1', target: 'CountBatch' }
	] as SfcModel['steps'][number]['actions'];
	return m;
}

test('layoutSfc: alternative exits from one step get their own bars, priority on top', () => {
	const layout = layoutSfc(tankBatchWithAborted());
	const abort = layout.trans.find((r) => r.t.id === 'tr:t_abort')!;
	const full = layout.trans.find((r) => r.t.id === 'tr:t_full')!;
	assert.equal(abort.jump, undefined);
	assert.ok(abort.barY < full.barY, `t_abort (declared first) bar ${abort.barY} should sit above t_full's ${full.barY}`);
	assert.ok(full.barY - abort.barY >= 20, 'the two bars must not draw on top of each other');
	// A leftward-only bar labels its own run, not the shared stem on the right.
	assert.ok(abort.condX < abort.barX2, `t_abort's label at x=${abort.condX} sits on the shared stem`);
});

test('layoutSfc: a jump glyph clears the step\'s action table', () => {
	const layout = layoutSfc(tankBatchWithAborted());
	const drain = layout.steps.find((p) => p.id === 'st:Drain')!;
	const tEmpty = layout.trans.find((r) => r.t.id === 'tr:t_empty')!;
	// three rows + the "+ action" row, 16 px each
	assert.ok(tEmpty.jump!.y - 9 >= drain.y + 4 * 16, `jump at ${tEmpty.jump!.y} overlaps Drain's action rows`);
});

// ── clipboard ────────────────────────────────────────────────────────────────

test('clipSteps: steps with their associations, and only the transitions wholly inside', () => {
	const m = linearModel();
	m.steps[1].actions = [{ qualifier: 'L', target: 'Valve', time: 'T#2S', line: 3 }];
	const lay = layoutSfc(m);
	const clip = clipSteps(m, lay, ['st:Fill', 'st:Drain'])!;
	assert.deepEqual(clip.steps.map((s) => s.name), ['Fill', 'Drain']);
	assert.deepEqual(clip.steps[0].actions, [{ qualifier: 'L', target: 'Valve', time: 'T#2S' }]);
	// t2 (Fill→Drain) comes along; t1/t3 each have an end outside.
	assert.deepEqual(clip.trans, [{ from: ['Fill'], to: ['Drain'], cond: 'Full' }]);
	assert.equal(clipSteps(m, lay, ['st:Nope']), undefined);
});

test('pasteStepsOp: relative arrangement kept, placed clear of the chart', () => {
	const m = linearModel();
	const lay = layoutSfc(m);
	const clip = clipSteps(m, lay, ['st:Fill', 'st:Drain'])!;
	const op = pasteStepsOp(clip, lay) as { type: string; steps: { x: number; y: number }[] };
	assert.equal(op.type, 'pasteSteps');
	const [a, b] = op.steps;
	assert.equal(b.y - a.y, clip.steps[1].y - clip.steps[0].y);
	for (const s of op.steps) assert.ok(s.x >= lay.width, 'lands right of the existing chart');
});

test('deleteStepsOp: the steps plus the transitions between them, one op', () => {
	const op = deleteStepsOp(linearModel(), ['st:Fill', 'st:Drain']);
	assert.deepEqual(op, { type: 'deleteSelection', nodes: ['st:Fill', 'st:Drain', 'tr:t2'] });
});
