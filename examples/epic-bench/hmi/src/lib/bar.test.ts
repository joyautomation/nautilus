// node --test --experimental-strip-types src/lib/bar.test.ts (npm test)
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { BAR_WIRING, KNOB_SWEEP, knobAngle, knobValueAt, onPress, onRelease, simulated } from './bar.ts';

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

test('the latching button flips on each press and writes nothing on release', () => {
	assert.equal(onPress('latching', false), true);
	assert.equal(onPress('latching', true), false);
	assert.equal(onPress('latching', undefined), true); // no value yet: the first press latches it on
	assert.equal(onRelease('latching'), null);
});

test('the momentary button is on while held and off when let go, whatever it was', () => {
	assert.equal(onPress('momentary', false), true);
	assert.equal(onPress('momentary', true), true);
	assert.equal(onRelease('momentary'), false);
});

test('the load panel mapping: top latching on DI0, bottom momentary on DI1 with its blue ring on DO0', () => {
	assert.deepEqual(
		BAR_WIRING.buttons.map((b) => [b.tag, b.kind, b.ring.tag]),
		[
			['EPIC_M0_DI00', 'latching', 'EPIC_M0_DI00'],
			['EPIC_M0_DI01', 'momentary', 'EPIC_M1_DO00']
		]
	);
	assert.match(BAR_WIRING.knob.tag, /^EPIC_M3_AI\d\d$/);
});
