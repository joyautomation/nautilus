// node --test --experimental-strip-types src/lib/bar.test.ts (npm test)
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { BAR_WIRING, KNOB_SWEEP, knobAngle, knobValueAt, simulated, toggled } from './bar.ts';

const close = (a: number, b: number, eps = 1e-9) => assert.ok(Math.abs(a - b) < eps, `${a} != ${b}`);

test('the knob points straight up at mid-range and at the ends of its sweep at min and max', () => {
	close(knobAngle(5), 0);
	close(knobAngle(0), -KNOB_SWEEP / 2);
	close(knobAngle(10), KNOB_SWEEP / 2);
	close(knobAngle(-3), -KNOB_SWEEP / 2); // clamped
	close(knobAngle(42), KNOB_SWEEP / 2);
});

test('the pointer turns the knob clockwise from min to max', () => {
	close(knobValueAt(0, 1, 0), 5); // straight up
	close(knobValueAt(1, 0, 0), 5 + 5 * (90 / 150)); // 3 o'clock
	close(knobValueAt(-1, 0, 0), 5 - 5 * (90 / 150)); // 9 o'clock
	assert.ok(knobValueAt(1, 0.2, 5) > knobValueAt(0.2, 1, 5), 'further clockwise is higher');
});

test('a value is rounded to the knob step', () => {
	const v = knobValueAt(Math.sin(0.123), Math.cos(0.123), 5);
	close(Math.round(v * 10) / 10, v);
});

test('past either end, in the dead zone at the bottom, it holds the nearer end and never jumps across', () => {
	assert.equal(knobValueAt(-0.1, -1, 9.5), 10); // just past max, coming from near max
	assert.equal(knobValueAt(0.1, -1, 9.5), 10); // and across the bottom: still max, not min
	assert.equal(knobValueAt(0.1, -1, 0.3), 0);
	assert.equal(knobValueAt(-0.1, -1, 0.3), 0);
});

test('only a frame with no field driver is the simulated build', () => {
	assert.equal(simulated(undefined), false); // no frame yet
	assert.equal(simulated({}), true);
	assert.equal(simulated({ drivers: [] }), true);
	assert.equal(simulated({ drivers: [{ kind: 'optommp', state: 'connected' }] }), false);
});

test('a toggle button flips: a click turns an off input on and an on input off', () => {
	assert.equal(toggled(false), true);
	assert.equal(toggled(true), false);
	assert.equal(toggled(undefined), true); // no value yet: the first click turns it on
});

test('the buttons and the knob name input tags, the rings output tags, one ring per button', () => {
	for (const t of BAR_WIRING.buttons) assert.match(t, /^EPIC_M0_DI\d\d$/);
	for (const t of BAR_WIRING.rings) assert.match(t, /^EPIC_M1_DO\d\d$/);
	assert.equal(BAR_WIRING.rings.length, BAR_WIRING.buttons.length);
	assert.match(BAR_WIRING.knob.tag, /^EPIC_M3_AI\d\d$/);
});
