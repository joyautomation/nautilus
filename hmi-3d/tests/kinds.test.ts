// Unit tests for the built-in kinds' pure parts (src/lib/kinds.ts): what
// each reads off its struct, the bind overrides, and the label text.
import { describe, expect, it } from './harness.js';
import { tankState, tankStatus, pumpState, pumpStatus, valveState, valveStatus } from '../src/lib/kinds.js';

describe('tank', () => {
	it('reads Level and TempC, clamped', () => {
		expect(tankState({ Level: 48.25, TempC: 21 })).toEqual({ level: 48.25, tempC: 21 });
		expect(tankState({ Level: 140 }).level).toBe(100);
		expect(tankState(undefined)).toEqual({ level: 0, tempC: 0 });
	});
	it('a bound prop overrides the member (a flat-tag project)', () => {
		expect(tankState({ Level: 10 }, { level: 55 }).level).toBe(55);
		expect(tankState(undefined, { level: 55 }).level).toBe(55);
	});
	it('status', () => {
		expect(tankStatus({ Level: 48.25 }, true)).toBe('48.3 %');
		expect(tankStatus({ Level: 48.25 }, false)).toBe('stale');
	});
});

describe('pump', () => {
	it('reads Running, Fault and Speed', () => {
		expect(pumpState({ Running: true, Fault: false, Speed: 62.5 })).toEqual({ running: true, fault: false, speed: 62.5 });
		expect(pumpState({ Running: 1, Speed: -5 })).toEqual({ running: false, fault: false, speed: 0 });
	});
	it('status', () => {
		expect(pumpStatus({ Running: true, Speed: 62.5 }, true)).toBe('run 63 %');
		expect(pumpStatus({ Running: false }, true)).toBe('stopped');
		expect(pumpStatus({ Running: true, Fault: true }, true)).toBe('FAULT');
		expect(pumpStatus({ Running: true }, false)).toBe('stale');
	});
});

describe('valve', () => {
	it('reads Pos and Cmd', () => {
		expect(valveState({ Pos: 40, Cmd: 50 })).toEqual({ pos: 40, cmd: 50 });
		expect(valveState({ Pos: 40 }, { cmd: 90 })).toEqual({ pos: 40, cmd: 90 });
	});
	it('status', () => {
		expect(valveStatus({ Pos: 40.4, Cmd: 50 }, true)).toBe('40 % (cmd 50)');
		expect(valveStatus({ Pos: 40 }, false)).toBe('stale');
	});
});
