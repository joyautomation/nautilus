// The alarm list (src/lib/alarmlist.ts): which alarms show, in what order,
// in what words, and which device and part each is on.
import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import { ago, alarmFacts, listAlarms, stateText, whereIs, worst, type Alarm } from '../src/lib/alarmlist.ts';

const a = (id: string, priority: Alarm['priority'], state: string, activeMs = 0, site?: string): Alarm => ({ id, tag: id, name: id, priority, state, activeMs, site });

describe('listAlarms', () => {
	it('drops normal and suppressed', () =>
		assert.deepEqual(
			listAlarms([a('x', 'high', 'normal'), a('y', 'high', 'suppressed'), a('z', 'low', 'ack-active')]).map((x) => x.id),
			['z']
		));
	it('new, then standing, then cleared-but-unacked; then priority; then newest', () =>
		assert.deepEqual(
			listAlarms([
				a('rtn-crit', 'critical', 'unack-rtn'),
				a('acked-med', 'medium', 'ack-active'),
				a('new-low', 'low', 'unack-active'),
				a('new-high-old', 'high', 'unack-active', 1),
				a('new-high', 'high', 'unack-active', 2)
			]).map((x) => x.id),
			['new-high', 'new-high-old', 'new-low', 'acked-med', 'rtn-crit']
		));
});

describe('words', () => {
	it('states', () => {
		assert.equal(stateText('unack-active'), 'new');
		assert.equal(stateText('unack-rtn'), 'cleared, not acknowledged');
	});
	it('ages', () => {
		assert.equal(ago(1000, 13_000), '12 s');
		assert.equal(ago(0, 1), '');
		assert.equal(ago(0 + 1, 1 + 4 * 60_000), '4 min');
		assert.equal(ago(1, 1 + 3 * 3_600_000), '3 h');
	});
	it('the worst still active wins over a worse one that has cleared', () => {
		assert.equal(worst([a('c', 'critical', 'unack-rtn'), a('m', 'medium', 'ack-active')]), 'medium');
		assert.equal(worst([a('c', 'critical', 'unack-rtn')]), 'critical');
		assert.equal(worst([]), undefined);
	});
});

describe('whereIs', () => {
	const devices = ['SW1', 'SW2', 'NODE1', 'NODE2'];
	it('a part alarm: its device and the part tag', () =>
		assert.deepEqual(whereIs(a('NODE1_PSU2.Fault', 'high', 'unack-active'), devices), { device: 'NODE1', asset: 'NODE1_PSU2' }));
	it('a device-level alarm is on the device', () =>
		assert.deepEqual(whereIs(a('SW1_Flap', 'high', 'unack-active'), devices), { device: 'SW1', asset: 'SW1_Flap' }));
	it('the site names the device when it is given', () =>
		assert.deepEqual(whereIs(a('Storm_A', 'critical', 'unack-active', 0, 'SW2'), devices), { device: 'SW2', asset: 'Storm_A' }));
	it('an alarm on nothing in the plant has no device', () =>
		assert.equal(whereIs(a('Room_Hot', 'low', 'unack-active'), devices).device, undefined));
});

describe('alarmFacts: a faceplate says what its sign is', () => {
	const all = [a('NODE1_PSU2.Fault', 'high', 'unack-active'), a('NODE1_Fan3.Fault', 'medium', 'ack-active'), a('NODE2_Fan1.Fault', 'low', 'unack-active'), a('NODE1_PSU1.Fault', 'high', 'normal')];
	it('a part: only its own, in the list order', () =>
		assert.deepEqual(alarmFacts(all, 'NODE1_PSU2'), [{ label: 'Alarm · high', value: 'NODE1_PSU2.Fault (new)' }]));
	it('a device: everything under it', () =>
		assert.deepEqual(alarmFacts(all, 'NODE1', true).map((f) => f.value), ['NODE1_PSU2.Fault (new)', 'NODE1_Fan3.Fault (active, acknowledged)']));
	it('NODE1 is not NODE10', () => assert.deepEqual(alarmFacts([a('NODE10_Fan1.Fault', 'low', 'unack-active')], 'NODE1', true), []));
});
