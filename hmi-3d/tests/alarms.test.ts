// Unit tests for the per-asset alarm fold (src/lib/alarms.ts).
import { describe, expect, it } from './harness.js';
import { worstAlarmByAsset, alarmAsset, worstUnder, PRIORITY_SIGN, PRIORITIES } from '../src/lib/alarms.js';

const inst = (tag: string, priority: string, state: string) => ({ tag, priority, state });

describe('alarmAsset', () => {
	it('is the tag root', () => {
		expect(alarmAsset('P101.Fault')).toBe('P101');
		expect(alarmAsset('HighLevel')).toBe('HighLevel');
	});
});

describe('worstAlarmByAsset', () => {
	it('skips normal, shelved and suppressed', () => {
		const m = worstAlarmByAsset([inst('P101.Fault', 'medium', 'normal'), inst('T101.HH', 'high', 'shelved'), inst('XV101.Fault', 'low', 'suppressed')]);
		expect(m.size).toBe(0);
	});
	it('keeps the worst priority and any unack on an asset', () => {
		const m = worstAlarmByAsset([
			inst('T101.HH', 'high', 'ack-active'),
			inst('T101.LL', 'critical', 'ack-active'),
			inst('T101.Temp', 'low', 'unack-active')
		]);
		expect(m.get('T101')).toEqual({ priority: 'critical', unacked: true, count: 3, active: true });
	});
	it('unack-rtn still counts (it is on the operator’s list until acked)', () => {
		const m = worstAlarmByAsset([inst('P101.Fault', 'medium', 'unack-rtn')]);
		expect(m.get('P101')).toEqual({ priority: 'medium', unacked: true, count: 1, active: false });
	});
	it('an unknown priority never outranks a known one', () => {
		const m = worstAlarmByAsset([inst('P101.A', 'low', 'ack-active'), inst('P101.B', 'weird', 'ack-active')]);
		expect(m.get('P101')?.priority).toBe('low');
	});
	it('separates assets', () => {
		const m = worstAlarmByAsset([inst('P101.Fault', 'medium', 'ack-active'), inst('XV101.Fault', 'medium', 'unack-active')]);
		expect([...m.keys()]).toEqual(['P101', 'XV101']);
		expect(m.get('P101')?.unacked).toBe(false);
		expect(m.get('XV101')?.unacked).toBe(true);
	});
});

describe('worstUnder', () => {
	const m = worstAlarmByAsset([
		inst('NODE1_Fan3.Fault', 'high', 'unack-active'),
		inst('NODE1_Temp_CPU.HighHigh', 'critical', 'ack-active'),
		inst('NODE10_Fan1.Fault', 'low', 'unack-active'),
		inst('SW1_Storm', 'critical', 'unack-active')
	]);
	it('rolls a device up: its own tag and every part under it, worst first, counts summed', () => {
		expect(worstUnder(m, 'NODE1')).toEqual({ priority: 'critical', unacked: true, count: 2, active: true });
		expect(worstUnder(m, 'SW1')).toEqual({ priority: 'critical', unacked: true, count: 1, active: true });
	});
	it('does not take a device whose name merely starts the same (NODE10 is not NODE1)', () => {
		expect(worstUnder(m, 'NODE10')?.count).toBe(1);
		expect(worstUnder(m, 'NODE2')).toBe(undefined);
	});
});

describe('PRIORITY_SIGN', () => {
	it('gives every level its own shape, worst first', () => {
		const shapes = PRIORITIES.map((p) => PRIORITY_SIGN[p].shape);
		expect(new Set(shapes).size).toBe(PRIORITIES.length);
		expect(PRIORITY_SIGN.critical.urgency).toBe('act now');
	});
});

describe('returned to normal, not yet acknowledged', () => {
	it('stays on the list, and says it is no longer active', () => {
		const m = worstAlarmByAsset([inst('NODE1_Fan3.Fault', 'high', 'unack-rtn')]);
		expect(m.get('NODE1_Fan3')).toEqual({ priority: 'high', unacked: true, count: 1, active: false });
		const both = worstAlarmByAsset([inst('NODE1_Fan3.Fault', 'high', 'unack-rtn'), inst('NODE1_Fan3.Fault2', 'low', 'ack-active')]);
		expect(both.get('NODE1_Fan3')?.active).toBe(true);
	});
});
