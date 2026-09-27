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
