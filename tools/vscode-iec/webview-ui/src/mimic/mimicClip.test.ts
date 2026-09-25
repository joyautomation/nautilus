/// <reference types="node" />
// Mimic copy/paste: the clip keeps props/bindings/ports and only the pipes
// wholly between copied equipment; the paste batch is accepted by the REAL
// host reducer (src/mimicOps.ts) and re-anchors those pipes to the copies.
import { strict as assert } from 'node:assert';
import { test } from 'node:test';
import { clipEquipment, pasteOps } from './mimicClip.ts';
import { applyMimicOp, parseMimic } from '../../../src/mimicOps.ts';

const doc = {
	canvas: { width: 800, height: 600 },
	equipment: [
		{ id: 'tank1', component: 'Tank', x: 100, y: 100, label: 'T-1', props: { max: 100 }, bind: { level: 'Level' } },
		{ id: 'pump1', component: 'Pump', x: 300, y: 100, width: 60, ports: [{ name: 'in', x: 0, y: 0.5 }] },
		{ id: 'valve1', component: 'Valve', x: 500, y: 100 }
	],
	pipes: [
		{ id: 'pipe1', points: [[200, 120]] as [number, number][], from: { equip: 'tank1', port: 'right' }, to: { equip: 'pump1', port: 'in' }, bind: { flowing: 'Run' }, routing: 'orthogonal' as const },
		{ id: 'pipe2', points: [], from: { equip: 'pump1', port: 'out' }, to: { equip: 'valve1', port: 'in' } }
	]
};

test('clipEquipment: selected equipment + pipes anchored at both ends inside', () => {
	const clip = clipEquipment(doc, ['tank1', 'pump1'])!;
	assert.deepEqual(clip.equipment.map((e) => e.id), ['tank1', 'pump1']);
	assert.deepEqual(clip.pipes.map((p) => p.id), ['pipe1'], 'pipe2 reaches valve1, which was not copied');
	assert.equal(clipEquipment(doc, ['nope']), undefined);
});

test('pasteOps: fresh ids, offset, props/bindings/ports, pipes re-anchored — via the real reducer', () => {
	const clip = clipEquipment(doc, ['tank1', 'pump1'])!;
	const { op, ids } = pasteOps(doc, clip, 20, 20);
	assert.deepEqual(ids, ['tank2', 'pump2']);
	const res = applyMimicOp(JSON.stringify(doc), op as never);
	assert.ok('text' in res, 'error' in res ? res.error : '');
	const out = parseMimic((res as { text: string }).text);
	const tank2 = out.equipment!.find((e) => e.id === 'tank2')!;
	assert.deepEqual([tank2.x, tank2.y, tank2.label], [120, 120, 'T-1']);
	assert.deepEqual(tank2.props, { max: 100 });
	assert.deepEqual(tank2.bind, { level: 'Level' });
	const pump2 = out.equipment!.find((e) => e.id === 'pump2')!;
	assert.equal(pump2.width, 60);
	assert.deepEqual(pump2.ports, [{ name: 'in', x: 0, y: 0.5 }]);
	const pipes = out.pipes!.filter((p) => !['pipe1', 'pipe2'].includes(p.id));
	assert.equal(pipes.length, 1);
	assert.deepEqual(pipes[0].from, { equip: 'tank2', port: 'right' });
	assert.deepEqual(pipes[0].to, { equip: 'pump2', port: 'in' });
	assert.deepEqual(pipes[0].points, [[220, 140]]);
	assert.deepEqual(pipes[0].bind, { flowing: 'Run' });
	assert.equal(pipes[0].routing, 'orthogonal');
	// Originals untouched.
	assert.equal(out.equipment!.find((e) => e.id === 'tank1')!.x, 100);
});

test('pasteOps: pasting twice keeps finding free ids', () => {
	const clip = clipEquipment(doc, ['tank1'])!;
	const first = applyMimicOp(JSON.stringify(doc), pasteOps(doc, clip, 20, 20).op as never) as { text: string };
	const d2 = parseMimic(first.text);
	const { ids } = pasteOps(d2 as never, clip, 40, 40);
	assert.deepEqual(ids, ['tank3']);
});
