// The component registry the EDITOR knows: the kit's built-in equipment —
// the same modules the runtime <Mimic> renders, so the canvas is WYSIWYG.
// (Custom registry components render as a placeholder chip here; they only
// exist in the app that supplies them.)
import { Gauge, Heater, Pump, Sparkline, Tank, Valve } from '@joyautomation/nautilus-hmi';
import type { Component } from 'svelte';
import { PORTS } from './builtinPorts';
export { PORTS };

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export const registry: Record<string, Component<any>> = { Tank, Heater, Pump, Valve, Gauge, Sparkline };
export const registryNames = Object.keys(registry);

/** Fallbacks merged UNDER doc props/bindings so every component renders on
 * a dead canvas (Sparkline's `values` is required; the rest just look
 * better mid-motion in the palette and on unbound equipment). */
export const DEMO_PROPS: Record<string, Record<string, unknown>> = {
	Sparkline: { values: [4, 6, 5, 8, 6, 9, 7, 8] },
	Tank: { levelPct: 62 },
	Heater: { powerPct: 60 },
	Pump: { running: true },
	Valve: { openPct: 70 },
	Gauge: { value: 65, setpoint: 65 }
};

/** Named connection points, as fractions of the rendered box. Pipe drawing
 * and vertex drags snap to them, and a pipe END landing on (or in) a
 * component rides along when the component moves. Editor-side only — the
 * .mimic.json keeps plain [x, y] points, so the runtime <Mimic> needs
 * nothing new. (Data lives in builtinPorts.ts, re-exported here so existing
 * imports of `PORTS` from './registry' keep working.) */

/** Tag-bindable prop suggestions, per component (pipes use the "pipe" key). */
export const BIND_HINTS: Record<string, string[]> = {
	Tank: ['levelPct'],
	Heater: ['powerPct'],
	Pump: ['running', 'speedPct'],
	Valve: ['openPct'],
	Gauge: ['value', 'setpoint'],
	Sparkline: [],
	pipe: ['flowing', 'rate']
};
