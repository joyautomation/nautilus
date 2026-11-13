// node --experimental-strip-types --test src/mimic/liveWrite.test.ts
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { isControlTarget, writeRefusal } from './liveWrite.ts';

const live = { liveEnabled: true, tags: { F2_BackwashReq: false } };
const el = (hit: boolean) => ({ closest: (sel: string) => (hit && sel.includes('button') ? {} : null) }) as unknown as EventTarget;

test('a write goes with live values on and a controller connected', () => {
	assert.equal(writeRefusal(live), null);
});
test('a write is refused, saying why, with live values off', () => {
	assert.match(writeRefusal({ liveEnabled: false, tags: null }) ?? '', /Live values are off/);
});
test('a write is refused, saying why, with no controller', () => {
	assert.match(writeRefusal({ liveEnabled: true, tags: null }) ?? '', /No controller/);
});
test('a press on a control reaches it while live', () => {
	assert.equal(isControlTarget(el(true), live), true);
});
test('a press on the drawing stays the editor\'s', () => {
	assert.equal(isControlTarget(el(false), live), false);
});
test('every press is the editor\'s when nothing can be written', () => {
	assert.equal(isControlTarget(el(true), { liveEnabled: true, tags: null }), false);
	assert.equal(isControlTarget(el(true), { liveEnabled: false, tags: { A: 1 } }), false);
});
test('a null target is no control', () => {
	assert.equal(isControlTarget(null, live), false);
});
