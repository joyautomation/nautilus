// The aluminium bar beside the chassis: two pushbuttons and a knob, which
// the 3D screen presses and turns on the simulated build (sim.yaml, the
// playground). On the controller the inputs belong to the modules and the
// screen writes nothing.
//
// WIRING (#837), from the I/O mapping of this bench unit's load panel
// (Opto 22's documentation for it): the top push button latches, on DI0,
// and lights while it is in; the bottom one is momentary, on DI1, and its
// blue LED ring is an output, DO0, which the program drives (bench.st
// mirrors DI0 to DO0, so latching the top button lights the bottom one's
// ring); the knob is the potentiometer on the voltage input slot 3
// channel 0 (GRV-IV-24 ch 0).
export type ButtonKind = 'latching' | 'momentary';
export const BAR_WIRING = {
	/** Top button, then bottom; ring: the tag that lights it, and its colour. */
	buttons: [
		{ tag: 'EPIC_M0_DI00', kind: 'latching' as ButtonKind, ring: { tag: 'EPIC_M0_DI00', color: '#ff3b30' } },
		{ tag: 'EPIC_M0_DI01', kind: 'momentary' as ButtonKind, ring: { tag: 'EPIC_M1_DO00', color: '#2f7bff' } }
	],
	knob: { tag: 'EPIC_M3_AI00', min: 0, max: 10, step: 0.1, unit: 'V' }
} as const;

/**
 * Where the bar's parts sit on it (scene units): its x, its plate's bottom
 * and top, and each part's height, top to bottom: top button, bottom
 * button, knob, connector.
 */
export const BAR = { x: 222, y0: 8, y1: 134, parts: [112, 84, 54, 24] } as const;

export type BarButton = (typeof BAR_WIRING.buttons)[number] & { y: number };

/**
 * The buttons as the 3D screen draws them, top first: each with its height
 * on the bar and its wiring. Epic.svelte draws, hits and writes through
 * these alone, so a button's place and its tags can't come apart there.
 */
export const BAR_BUTTONS: readonly BarButton[] = BAR_WIRING.buttons.map((b, i) => ({ ...b, y: BAR.parts[i] }));

type Read = (tag: string) => unknown;

/** In while its input is on, or while held (before the frame shows it). */
export function buttonIn(b: BarButton, held: boolean, read: Read): boolean {
	return held || read(b.tag) === true;
}

/** Its ring: lit by the tag it is wired to (its own input, or an output). */
export function ringLit(b: BarButton, read: Read): boolean {
	return read(b.ring.tag) === true;
}

/** A press: the write to send, and whether the button is now held until let go. */
export function press(b: BarButton, read: Read): { tag: string; value: boolean; hold: boolean } {
	return { tag: b.tag, value: onPress(b.kind, read(b.tag)), hold: onRelease(b.kind) !== null };
}

/** Letting go: the write to send, or null when there is none. */
export function release(b: BarButton): { tag: string; value: boolean } | null {
	const v = onRelease(b.kind);
	return v === null ? null : { tag: b.tag, value: v };
}

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
