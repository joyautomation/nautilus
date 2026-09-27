// Unit tests for the per-asset alarm fold (src/lib/alarms.ts).
import { describe, expect, it } from './harness.js';
import { worstAlarmByAsset, alarmAsset } from '../src/lib/alarms.js';

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
		expect(m.get('T101')).toEqual({ priority: 'critical', unacked: true, count: 3 });
	});
	it('unack-rtn still counts (it is on the operator’s list until acked)', () => {
		const m = worstAlarmByAsset([inst('P101.Fault', 'medium', 'unack-rtn')]);
		expect(m.get('P101')).toEqual({ priority: 'medium', unacked: true, count: 1 });
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
