// The aluminium bar beside the chassis: two pushbuttons and a knob, which
// the 3D screen presses and turns on the simulated build (sim.yaml, the
// playground). On the controller the inputs belong to the modules and the
// screen writes nothing.
//
// PROVISIONAL WIRING (#837), to confirm on the bench unit. The buttons are
// latching (toggle) pushbuttons with an LED ring, most likely on DI00 and
// DI01; their rings are taken to be on DO0 and DO1, which bench.st drives
// from DI00 and DI01, so a ring lights when its button is in. The knob is
// most likely the potentiometer on the voltage input slot 3 channel 0 (it
// read 8.4 V, then 7.9 V, while the other 23 channels sit at 0 V). Change
// it here once the bench shows otherwise.
export const BAR_WIRING = {
	/** Top button, then bottom. */
	buttons: ['EPIC_M0_DI00', 'EPIC_M0_DI01'],
	/** Each button's LED ring. */
	rings: ['EPIC_M1_DO00', 'EPIC_M1_DO01'],
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

/** A toggle button's next state when clicked: in if it was out, out if in. */
export function toggled(current: unknown): boolean {
	return current !== true;
}

/** The simulated build: a frame has arrived and no field driver is in it. */
export function simulated(frame: { drivers?: unknown[] } | null | undefined): boolean {
	return !!frame && !frame.drivers?.length;
}
