// The built-in kinds' pure parts: what each reads off its struct tag and
// the status text its label shows. The Svelte components in components/
// read the same members; keeping the text here makes it testable and keeps
// a label and a drawer saying the same thing.
import { member, num } from './bindings.js';

const clamp = (v: number, lo: number, hi: number) => Math.max(lo, Math.min(hi, v));

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
export function tankStatus(value: unknown, good: boolean): string {
	return good ? `${tankState(value).level.toFixed(1)} %` : 'stale';
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
export function pumpStatus(value: unknown, good: boolean): string {
	if (!good) return 'stale';
	const s = pumpState(value);
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
export function valveStatus(value: unknown, good: boolean): string {
	if (!good) return 'stale';
	const s = valveState(value);
	return `${s.pos.toFixed(0)} % (cmd ${s.cmd.toFixed(0)})`;
}
