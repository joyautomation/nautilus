// node --test --experimental-strip-types src/lib/bar.test.ts (npm test)
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { BAR, BAR_BUTTONS, BAR_WIRING, KNOB_SWEEP, buttonIn, knobAngle, knobValueAt, onPress, onRelease, press, release, ringLit, simulated } from './bar.ts';

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

test('the drawn buttons: the top one is DI0 latching, the bottom one DI1 momentary, each at its own height on the bar', () => {
	assert.deepEqual(
		BAR_BUTTONS.map((b) => [b.tag, b.kind]),
		[
			['EPIC_M0_DI00', 'latching'],
			['EPIC_M0_DI01', 'momentary']
		]
	);
	assert.ok(BAR_BUTTONS[0].y > BAR_BUTTONS[1].y, 'the first button is drawn above the second');
	assert.equal(BAR_BUTTONS[0].y, BAR.parts[0]);
	assert.equal(BAR_BUTTONS[1].y, BAR.parts[1]);
	assert.ok(BAR_BUTTONS[1].y > BAR.parts[2], 'both buttons sit above the knob');
});

test('each drawn button reads, lights and writes its own tags', () => {
	const tags = (on: string[]) => (tag: string) => on.includes(tag);
	const [top, bottom] = BAR_BUTTONS;
	// The top button is in while DI0 is on; the bottom one's ring follows DO0, not DI1.
	assert.equal(buttonIn(top, false, tags(['EPIC_M0_DI00'])), true);
	assert.equal(buttonIn(top, false, tags(['EPIC_M0_DI01'])), false);
	assert.equal(buttonIn(bottom, false, tags(['EPIC_M0_DI01'])), true);
	assert.equal(buttonIn(bottom, true, tags([])), true); // held, before the frame shows it
	assert.equal(ringLit(top, tags(['EPIC_M0_DI00'])), true);
	assert.equal(ringLit(bottom, tags(['EPIC_M1_DO00'])), true);
	assert.equal(ringLit(bottom, tags(['EPIC_M0_DI01', 'EPIC_M0_DI00'])), false);
	// A press writes the button's own input; only the momentary one is held, and letting go writes it off.
	assert.deepEqual(press(top, tags(['EPIC_M0_DI00'])), { tag: 'EPIC_M0_DI00', value: false, hold: false });
	assert.deepEqual(press(bottom, tags([])), { tag: 'EPIC_M0_DI01', value: true, hold: true });
	assert.equal(release(top), null);
	assert.deepEqual(release(bottom), { tag: 'EPIC_M0_DI01', value: false });
});
