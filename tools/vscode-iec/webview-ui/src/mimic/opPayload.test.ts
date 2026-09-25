// Run with: node --experimental-strip-types --test src/mimic/opPayload.test.ts
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { cloneSafe, opFailureMessage } from './opPayload.ts';

// A Svelte `$state` object is a Proxy; so is this. structuredClone (what
// postMessage does) refuses both with DataCloneError.
const proxied = <T extends object>(v: T): T => new Proxy(v, {});

test('the port-to-port addPipe op, built from $state-like anchors, survives structuredClone once cloneSafe()d', () => {
	const from = proxied({ equip: 'P101', port: 'out' });
	const to = proxied({ equip: 'WW101', port: 'left' });
	const op = { type: 'addPipe', points: [[300, 80], [300, 40]] as [number, number][], from, to, routing: 'orthogonal' };
	assert.throws(() => structuredClone(op), (e: Error) => e.name === 'DataCloneError');
	const safe = cloneSafe(op);
	assert.deepEqual(structuredClone(safe), {
		type: 'addPipe',
		points: [[300, 80], [300, 40]],
		from: { equip: 'P101', port: 'out' },
		to: { equip: 'WW101', port: 'left' },
		routing: 'orthogonal'
	});
});

test('cloneSafe keeps null (an op patch deleting a field) and drops undefined', () => {
	const op = { type: 'updatePipe', id: 'pipe1', patch: proxied({ to: null, from: undefined, points: proxied([proxied([1, 2])]) }) };
	assert.deepEqual(structuredClone(cloneSafe(op)), { type: 'updatePipe', id: 'pipe1', patch: { to: null, points: [[1, 2]] } });
});

test('a failed post names the op and the reason', () => {
	const msg = opFailureMessage('addPipe', new Error('could not be cloned'));
	assert.match(msg, /addPipe/);
	assert.match(msg, /could not be cloned/);
});
