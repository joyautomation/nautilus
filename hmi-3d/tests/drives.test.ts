// Unit tests for the drive vocabulary (src/lib/drives.ts): evaluation
// against a struct value, the node-level overrides, the member list a
// kind reads, the structural validator, and status templates.
import { describe, expect, it } from './harness.js';
import {
	evalDrives,
	evalNum,
	evalBool,
	driveMembers,
	validateDrives,
	formatStatus,
	statusMembers,
	validStatusTemplate,
	channelOf,
	rampAt,
	blinkOn,
	pulseAt,
	type Drive
} from '../src/lib/drives.js';

const pumpDrives: Drive[] = [
	{ mesh: 'Coupling', spin: { axis: 'x', revPerS: { bind: 'Speed', scale: 0.02 } } },
	{ mesh: 'Motor', tint: { bind: 'Running', on: 'running' } },
	{ mesh: 'Beacon', emissive: { bind: 'Fault', on: 'critical', intensity: 2 } },
	{ mesh: 'Guard', visible: { bind: '!Fault' } }
];
const motor = { Running: true, Fault: false, Speed: 50 };

describe('evalNum / evalBool', () => {
	it('scales, offsets and clamps a member', () => {
		expect(evalNum({ bind: 'Pos', scale: -0.9, offset: 90 }, { Pos: 100 })).toBe(0);
		expect(evalNum({ bind: 'Level', scale: 0.01, min: 0.01 }, { Level: 0 })).toBe(0.01);
		expect(evalNum({ bind: 'Level', max: 1 }, { Level: 140 })).toBe(1);
		expect(evalNum({ bind: 'Nope' }, {})).toBe(0);
	});
	it('a boolean is true, a number is above the threshold, ! negates', () => {
		expect(evalBool({ bind: 'Running' }, { Running: true })).toBe(true);
		expect(evalBool({ bind: 'Running' }, { Running: 1 })).toBe(true);
		expect(evalBool({ bind: 'Speed', threshold: 2 }, { Speed: 2 })).toBe(false);
		expect(evalBool({ bind: '!Fault' }, { Fault: false })).toBe(true);
		expect(evalBool({ bind: '!Fault' }, {})).toBe(true);
	});
	it("a node's bound prop overrides the member, by the built-ins' rule", () => {
		expect(evalNum({ bind: 'Speed' }, { Speed: 10 }, { speed: 75 })).toBe(75);
		expect(evalBool({ bind: 'Running' }, { Running: true }, { running: false })).toBe(false);
	});
});

describe('evalDrives', () => {
	it('one state per drive, in order', () => {
		const s = evalDrives(pumpDrives, motor);
		expect(s.map((x) => x.mesh)).toEqual(['Coupling', 'Motor', 'Beacon', 'Guard']);
		expect(s[0].spin!.axis).toBe('x');
		expect(Math.round(s[0].spin!.radPerS * 1000)).toBe(Math.round(2 * Math.PI * 1000)); // 50 % × 0.02 = 1 rev/s
		expect(s[1].tint).toBe('running');
		expect(s[2].emissive).toBe(null);
		expect(s[3].visible).toBe(true);
	});
	it('off states: the model keeps its own colour', () => {
		const s = evalDrives(pumpDrives, { Running: false, Fault: true, Speed: 0 });
		expect(s[1].tint).toBe(null);
		expect(s[2].emissive).toEqual({ color: 'critical', intensity: 2 });
		expect(s[3].visible).toBe(false);
	});
	it('turn and scale', () => {
		const s = evalDrives(
			[
				{ mesh: 'Handle', turn: { axis: 'z', deg: { bind: 'Pos', scale: -0.9, offset: 90 } } },
				{ mesh: 'Fluid', scale: { axis: 'y', to: { bind: 'Level', scale: 0.01 } } },
				{ mesh: 'Ball', scale: { axis: 'xyz', to: { bind: 'Level', scale: 0.01 } } }
			],
			{ Pos: 50, Level: 40 }
		);
		expect(Math.round(s[0].turn!.rad * 1000)).toBe(Math.round((Math.PI / 4) * 1000));
		expect(s[1].scale).toEqual([1, 0.4, 1]);
		expect(s[2].scale).toEqual([0.4, 0.4, 0.4]);
	});
	it('no drives, no states', () => {
		expect(evalDrives(undefined, motor)).toEqual([]);
	});
});

describe('driveMembers', () => {
	it('lists what the drives read, without the negation, deduplicated', () => {
		expect(driveMembers(pumpDrives)).toEqual(['Speed', 'Running', 'Fault']);
	});
});

describe('validateDrives', () => {
	it('accepts the pump', () => {
		expect(validateDrives(pumpDrives, '/kinds/pump/drive')).toEqual([]);
	});
	it('names the path of each problem', () => {
		const errs = validateDrives(
			[
				{ spin: { axis: 'w', revPerS: { bind: 'Speed' } } },
				{ mesh: 'A', spin: {}, tint: {} },
				{ mesh: 'B', tint: { bind: 'P101.Running', on: 'running' } },
				{ mesh: 'C', scale: { axis: 'y', to: { bind: '!Level' } } },
				{ mesh: 'D', emissive: { bind: 'Fault', on: 'critical', intensity: -1 } },
				{ mesh: 'E', wobble: {} },
				'nope'
			],
			'/kinds/x/drive'
		);
		expect(errs.map((e) => e.path)).toEqual([
			'/kinds/x/drive/0/mesh',
			'/kinds/x/drive/0/spin/axis',
			'/kinds/x/drive/1',
			'/kinds/x/drive/2/tint/bind',
			'/kinds/x/drive/3/scale/to/bind',
			'/kinds/x/drive/4/emissive/intensity',
			'/kinds/x/drive/5',
			'/kinds/x/drive/6'
		]);
	});
	it('channelOf', () => {
		expect(channelOf(pumpDrives[0])).toBe('spin');
		expect(channelOf({ mesh: 'a', spin: {}, tint: {} })).toBe(undefined);
	});
});

describe('status templates', () => {
	it('formats members, digits and a truth pick', () => {
		expect(formatStatus('{Level:1} %', { Level: 48.25 }, true)).toBe('48.3 %');
		expect(formatStatus('{Running?run:stopped} {Speed:0} %', { Running: true, Speed: 62.5 }, true)).toBe('run 63 %');
		expect(formatStatus('{Running?run:stopped}', { Running: false }, true)).toBe('stopped');
		expect(formatStatus('{Mode}', { Mode: 'AUTO' }, true)).toBe('AUTO');
		expect(formatStatus('{Nope}', {}, true)).toBe('—');
		expect(formatStatus('{Level} %', { Level: 1 }, false)).toBe('stale');
		expect(formatStatus('{Level:0} %', { Level: 1 }, true, { level: 77 })).toBe('77 %');
	});
	it('lists the members it reads', () => {
		expect(statusMembers('{Running?run:stopped} {Speed:0} % {Speed}')).toEqual(['Running', 'Speed']);
		expect(statusMembers(undefined)).toEqual([]);
	});
	it('rejects unbalanced or malformed templates', () => {
		expect(validStatusTemplate('{Level} %')).toBe(true);
		expect(validStatusTemplate('{Level %')).toBe(false);
		expect(validStatusTemplate('{1bad}')).toBe(false);
		expect(validStatusTemplate(3)).toBe(false);
	});
});

describe('a colour ramp on an analog', () => {
	const ramp: [number, string][] = [[20, 'fluid'], [60, '#ffffff'], [90, 'critical']];
	it('rampAt: the two stops around the value and the blend; clamped beyond the ends', () => {
		expect(rampAt(ramp, 10)).toEqual({ from: 'fluid', to: 'fluid', t: 0 });
		expect(rampAt(ramp, 20)).toEqual({ from: 'fluid', to: 'fluid', t: 0 });
		expect(rampAt(ramp, 40)).toEqual({ from: 'fluid', to: '#ffffff', t: 0.5 });
		expect(rampAt(ramp, 75)).toEqual({ from: '#ffffff', to: 'critical', t: 0.5 });
		expect(rampAt(ramp, 120)).toEqual({ from: 'critical', to: 'critical', t: 0 });
	});
	it('evalDrives hands the component the ramp and the value, for tint and emissive', () => {
		const drives: Drive[] = [
			{ mesh: 'Shell', tint: { bind: 'TempC', ramp } },
			{ mesh: 'Lamp', emissive: { bind: 'Speed', ramp: [[0, 'stale'], [60, 'running']], intensity: 2 } }
		];
		const [a, b] = evalDrives(drives, { TempC: 40, Speed: 30 });
		expect(a.tint).toEqual({ ramp, value: 40 });
		expect(b.emissive).toEqual({ ramp: [[0, 'stale'], [60, 'running']], value: 30, intensity: 2 });
		expect(driveMembers(drives)).toEqual(['TempC', 'Speed']);
	});
	it('validateDrives: a ramp has two ascending [value, colour] stops, a plain member, and no on / off', () => {
		expect(validateDrives([{ mesh: 'Shell', tint: { bind: 'TempC', ramp } }], '/d')).toEqual([]);
		const r = validateDrives(
			[
				{ mesh: 'Shell', tint: { bind: '!TempC', ramp: [[20, 'fluid']], on: 'x' } },
				{ mesh: 'Shell', emissive: { bind: 'TempC', ramp: [[20, 'fluid'], [10, 'x'], [30, 5]] } }
			],
			'/d'
		);
		expect(r.map((e) => e.path)).toEqual(['/d/0/tint/bind', '/d/0/tint/ramp', '/d/0/tint/on', '/d/1/emissive/ramp/1', '/d/1/emissive/ramp/2']);
	});
});

describe('blink and pulse (the clocked drives)', () => {
	it('evalDrives hands the component the on state with its off state and the rate, while the bool holds', () => {
		const drives: Drive[] = [
			{ mesh: 'Beacon', emissive: { bind: 'Fault', on: 'critical', intensity: 2, blink: 2 } },
			{ mesh: 'Shell', tint: { bind: 'Fault', on: 'critical', off: 'steel', blink: 1 } },
			{ mesh: 'Flag', visible: { bind: 'Fault', blink: 0.5 } }
		];
		const on = evalDrives(drives, { Fault: true });
		expect(on[0].blink).toEqual({ hz: 2, off: { mesh: 'Beacon', emissive: null } });
		expect(on[1].blink).toEqual({ hz: 1, off: { mesh: 'Shell', tint: 'steel' } });
		expect(on[2].blink).toEqual({ hz: 0.5, off: { mesh: 'Flag', visible: false } });
		const off = evalDrives(drives, { Fault: false });
		expect(off.map((s) => s.blink)).toEqual([undefined, undefined, undefined]);
		expect(off[0].emissive).toBe(null);
	});
	it('blinkOn: half the period on, half off, from t = 0', () => {
		expect(blinkOn(1, 0)).toBe(true);
		expect(blinkOn(1, 0.49)).toBe(true);
		expect(blinkOn(1, 0.5)).toBe(false);
		expect(blinkOn(1, 1)).toBe(true);
		expect(blinkOn(2, 0.3)).toBe(false);
	});
	it('pulse: a bool drive with hz and amount; off is back to the model (null); pulseAt swings 1 ± amount', () => {
		const drives: Drive[] = [{ mesh: 'Dome', pulse: { bind: 'Alarm', hz: 2, amount: 0.2 } }, { mesh: 'Lamp', pulse: { bind: 'Alarm' } }];
		expect(evalDrives(drives, { Alarm: true }).map((s) => s.pulse)).toEqual([{ hz: 2, amount: 0.2 }, { hz: 1, amount: 0.1 }]);
		expect(evalDrives(drives, { Alarm: false }).map((s) => s.pulse)).toEqual([null, null]);
		expect(Math.round(pulseAt({ hz: 1, amount: 0.2 }, 0.25) * 1e6) / 1e6).toBe(1.2);
		expect(Math.round(pulseAt({ hz: 1, amount: 0.2 }, 0.75) * 1e6) / 1e6).toBe(0.8);
		expect(driveMembers(drives)).toEqual(['Alarm']);
		expect(channelOf(drives[0])).toBe('pulse');
	});
	it('validateDrives: blink is a rate above 0 and not with a ramp; pulse has a bool, hz above 0 and an amount in (0, 1]', () => {
		const errs = validateDrives(
			[
				{ mesh: 'A', visible: { bind: 'X', blink: 0 } },
				{ mesh: 'A', tint: { bind: 'X', ramp: [[0, 'a'], [1, 'b']], blink: 1 } },
				{ mesh: 'A', pulse: { bind: '1bad', hz: -1, amount: 2 } },
				{ mesh: 'A', pulse: { bind: '!Alarm', hz: 3, amount: 0.5 } },
				{ mesh: 'A', emissive: { bind: 'X', on: 'critical', blink: 4 } }
			],
			'/d'
		).map((e) => e.path);
		expect(errs).toEqual(['/d/0/visible/blink', '/d/1/tint/blink', '/d/2/pulse/bind', '/d/2/pulse/hz', '/d/2/pulse/amount']);
	});
});
