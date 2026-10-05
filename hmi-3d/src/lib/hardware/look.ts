// How a part's state reads in 3D. ISA-101 restraint, as for the process
// kinds: a healthy part is grey with a green LED; colour means abnormal.
// A part that is not there, or not reported, is a wireframe, never a solid
// that could pass for a healthy one.
import type { Palette } from '../palette.js';
import type { PartState } from './profile.js';

export interface PartLook {
	/** Draw the model (else the empty-slot rendering). */
	fitted: boolean;
	led?: string;
	accent?: string;
	opacity: number;
	/** Wireframe colour for an empty, absent or unpublished position. */
	wire?: string;
}

export function partLook(state: PartState, palette: Palette, xray = false): PartLook {
	const p = palette.priority;
	const o = xray ? 0.9 : 1;
	switch (state) {
		case 'ok':
			return { fitted: true, led: palette.running, opacity: o };
		case 'warning':
			return { fitted: true, led: p.medium, accent: p.medium, opacity: o };
		case 'critical':
			return { fitted: true, led: p.critical, accent: p.critical, opacity: o };
		case 'stale':
			return { fitted: true, accent: palette.stale, opacity: 0.45 };
		case 'assumed':
			return { fitted: true, opacity: 0.5 };
		case 'absent':
			return { fitted: false, wire: p.critical, opacity: 1 };
		// Drivers publish a slot's tag only when something is fitted (a pulled
		// part keeps its tag and reads Present = false), so an unpublished
		// binding is an empty slot, drawn like one.
		case 'missing':
		case 'unbound':
			return { fitted: false, opacity: 1 };
	}
}

/** Where the part library is served from, relative to the app root. */
export const DEFAULT_MODELS = '/models/hardware/';
