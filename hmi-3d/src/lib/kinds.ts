// The built-in kinds' pure parts: what each reads off its struct tag and
// the status text its label shows. The Svelte components in components/
// read the same members; keeping the text here makes it testable and keeps
// a label and a drawer saying the same thing. Every reader takes the node's
// bound overrides too (`bind: { level: "LIT101_Level" }` on a node with no
// struct tag), so a flat-tag project's label agrees with its geometry.
import { member, num } from './bindings.js';
import type { ScenePort } from './scene.js';

const clamp = (v: number, lo: number, hi: number) => Math.max(lo, Math.min(hi, v));

/** The built-in kinds' contract (docs/design/spatial-hmi.md §3b): the UDT
 * each reads by default, the members it reads, and where pipes meet it
 * (its ports, in its frame: a pipe anchored `{ node, port }` resolves
 * here). registry.ts carries it into the registry; internal/scene's
 * Builtin and models/kinds.json must match. Pure, so a test can hold
 * models/kinds.json to it without loading a component. */
export const BUILTIN_CONTRACT: Record<string, { type: string; members: string[]; ports: Record<string, ScenePort> }> = {
	tank: { type: 'Tank', members: ['Level', 'TempC'], ports: { inlet: { at: [0, 0.46, 0], dir: [0, 1, 0] }, outlet: { at: [0.25, 0, 0], dir: [1, 0, 0] } } },
	pump: { type: 'Motor', members: ['Running', 'Fault', 'Speed'], ports: { suction: { at: [-0.21, 0.09, 0], dir: [-1, 0, 0] }, discharge: { at: [0, 0.21, 0], dir: [0, 1, 0] } } },
	valve: { type: 'Valve', members: ['Pos', 'Cmd'], ports: { in: { at: [0, -0.08, 0], dir: [0, -1, 0] }, out: { at: [0, 0.08, 0], dir: [0, 1, 0] } } }
};

export interface TankState {
	level: number;
	tempC: number;
}
export function tankState(value: unknown, over: { level?: unknown; tempC?: unknown } = {}): TankState {
	return {
		level: clamp(num(over.level ?? member(value, 'Level')), 0, 100),
		tempC: num(over.tempC ?? member(value, 'TempC'))
	};
}
export function tankStatus(value: unknown, good: boolean, over: { level?: unknown; tempC?: unknown } = {}): string {
	return good ? `${tankState(value, over).level.toFixed(1)} %` : 'stale';
}

export interface PumpState {
	running: boolean;
	fault: boolean;
	speed: number;
}
export function pumpState(value: unknown, over: { running?: unknown; fault?: unknown; speed?: unknown } = {}): PumpState {
	return {
		running: (over.running ?? member(value, 'Running')) === true,
		fault: (over.fault ?? member(value, 'Fault')) === true,
		speed: clamp(num(over.speed ?? member(value, 'Speed')), 0, 100)
	};
}
export function pumpStatus(value: unknown, good: boolean, over: { running?: unknown; fault?: unknown; speed?: unknown } = {}): string {
	if (!good) return 'stale';
	const s = pumpState(value, over);
	return s.fault ? 'FAULT' : s.running ? `run ${s.speed.toFixed(0)} %` : 'stopped';
}

export interface ValveState {
	pos: number;
	cmd: number;
}
export function valveState(value: unknown, over: { pos?: unknown; cmd?: unknown } = {}): ValveState {
	return {
		pos: clamp(num(over.pos ?? member(value, 'Pos')), 0, 100),
		cmd: clamp(num(over.cmd ?? member(value, 'Cmd')), 0, 100)
	};
}
export function valveStatus(value: unknown, good: boolean, over: { pos?: unknown; cmd?: unknown } = {}): string {
	if (!good) return 'stale';
	const s = valveState(value, over);
	return `${s.pos.toFixed(0)} % (cmd ${s.cmd.toFixed(0)})`;
}
