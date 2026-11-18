// The aluminium bar beside the chassis: two pushbuttons and a knob, which
// the 3D screen presses and turns on the simulated build (sim.yaml, the
// playground). On the controller the inputs belong to the modules and the
// screen writes nothing.
//
// WIRING (#837), from Opto 22's I/O mapping for the load panel and James:
// the top push button latches, on DI0, and lights while it is in; the
// bottom one is momentary, on DI1, and its blue LED ring is an output,
// DO0, which the program drives (bench.st mirrors DI0 to DO0, so latching
// the top button lights the bottom one's ring). The knob is the panel's
// potentiometer; on this bench it reads on the voltage input slot 3
// channel 0 (8.4 V, then 7.9 V, with the other 23 channels at 0 V); the
// panel's mapping names a universal I/O module this bench does not have.
export type ButtonKind = 'latching' | 'momentary';
export const BAR_WIRING = {
	/** Top button, then bottom; ring: the tag that lights it, and its colour. */
	buttons: [
		{ tag: 'EPIC_M0_DI00', kind: 'latching' as ButtonKind, ring: { tag: 'EPIC_M0_DI00', color: '#ff3b30' } },
		{ tag: 'EPIC_M0_DI01', kind: 'momentary' as ButtonKind, ring: { tag: 'EPIC_M1_DO00', color: '#2f7bff' } }
	],
	knob: { tag: 'EPIC_M3_AI00', min: 0, max: 10, step: 0.1, unit: 'V' }
} as const;

/** The knob's travel: a potentiometer's 300°, centred on 12 o'clock. */
export const KNOB_SWEEP = (300 / 180) * Math.PI;

/** The pointer's angle for a value, radians clockwise from 12 o'clock. */
export function knobAngle(volts: number, k: { min: number; max: number } = BAR_WIRING.knob): number {
	const frac = Math.min(1, Math.max(0, (volts - k.min) / (k.max - k.min)));
	return -KNOB_SWEEP / 2 + frac * KNOB_SWEEP;
}

/**
 * The value the knob turns to when the pointer is at (dx, dy) from its
 * centre, in the knob's plane (x right, y up): the angle of the pointer,
 * clockwise from 12 o'clock, mapped over the sweep and rounded to the step.
 * In the dead zone at the bottom (between the two ends) it stays at the end
 * nearer the value it had, so a drag past the end never jumps to the other.
 */
export function knobValueAt(
	dx: number,
	dy: number,
	prev: number,
	k: { min: number; max: number; step: number } = BAR_WIRING.knob
): number {
	const half = KNOB_SWEEP / 2;
	const a = Math.atan2(dx, dy); // clockwise from up, -π..π
	let frac: number;
	if (a < -half || a > half) frac = prev - k.min < (k.max - k.min) / 2 ? 0 : 1;
	else frac = (a + half) / KNOB_SWEEP;
	const v = k.min + frac * (k.max - k.min);
	return Math.round(v / k.step) * k.step;
}

/** What a press writes: a latching button flips its input, a momentary one turns it on. */
export function onPress(kind: ButtonKind, current: unknown): boolean {
	return kind === 'latching' ? current !== true : true;
}

/** What letting go writes: a momentary button turns its input off; a latching one writes nothing (null). */
export function onRelease(kind: ButtonKind): boolean | null {
	return kind === 'momentary' ? false : null;
}

/** The simulated build: a frame has arrived and no field driver is in it. */
export function simulated(frame: { drivers?: unknown[] } | null | undefined): boolean {
	return !!frame && !frame.drivers?.length;
}
