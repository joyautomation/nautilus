// What the HMI says about chattering alarms and floods (#1249), and how flood
// focus orders the table. The controller detects; these are only the words
// and the order, and nothing may be dropped.
import { describe, it, expect } from './harness.js';
import {
	chatterBadge,
	chatterText,
	chatteringIds,
	detectionEvent,
	detectionOutcome,
	floodText,
	focusSplit,
	groupsText,
	seenWhenText,
	spanText
} from '../src/lib/alarmchatter.js';
import { DETECTION_KINDS, STATE_KINDS, journalKinds, visibleKind } from '../src/lib/alarmjournal.js';
import type { AlarmChatter, AlarmEvent } from '../src/lib/alarms.svelte.js';

const chat = (o: Partial<AlarmChatter> = {}): AlarmChatter => ({
	sinceMs: 1_000_000,
	lastMs: 1_040_000,
	activations: 7,
	episodes: 1,
	windowMs: 60_000,
	count: 3,
	...o
});

describe('durations in a sentence', () => {
	it('seconds under two minutes, minutes under two hours, then hours', () => {
		expect(spanText(60_000)).toBe('60 s');
		expect(spanText(119_000)).toBe('119 s');
		expect(spanText(10 * 60_000)).toBe('10 min');
		expect(spanText(125 * 60_000)).toBe('2 h 5 min');
		expect(spanText(180 * 60_000)).toBe('3 h');
		expect(spanText(-5)).toBe('0 s');
	});
});

describe('the chatter badge and its sentence', () => {
	it('counts the episode, and says the rule and the age', () => {
		expect(chatterBadge(chat())).toBe('chattering ×7');
		expect(chatterText(chat(), 1_000_000 + 90_000)).toBe(
			'Chattering: 7 activations in 90 s; 3 in 60 s counts as chatter.'
		);
	});
	it('mentions the episode number only after the first', () => {
		expect(chatterText(chat({ episodes: 3 }), 1_030_000).includes('episode 3 since the controller started')).toBe(true);
		expect(chatterText(chat({ episodes: 1 }), 1_030_000)).toBe(
			'Chattering: 7 activations in 30 s; 3 in 60 s counts as chatter.'
		);
	});
	it('lists the alarms behind the Chattering chip', () => {
		const ids = chatteringIds([{ id: 'a', chatter: chat() }, { id: 'b' }]);
		expect([...ids]).toEqual(['a']);
	});
});

describe('the flood sentence', () => {
	const f = { id: 'f', sinceMs: 0, count: 23, total: 40, peak: 31, windowMs: 600_000, threshold: 10 };
	it('reads as the controller counts it', () => {
		expect(floodText(f)).toBe('Alarm flood: 23 in 10 min (limit 10)');
	});
	it('says at least when the ring overflowed', () => {
		expect(floodText({ ...f, count: 1024, saturated: true })).toBe('Alarm flood: at least 1024 in 10 min (limit 10)');
	});
	it('names the equipment that raised most of it', () => {
		expect(groupsText([{ equipment: 'P101', count: 8 }, { equipment: 'P102', count: 6 }])).toBe('P101 (8), P102 (6)');
		expect(groupsText([])).toBe('');
	});
});

describe('seen when', () => {
	it('is the controller words, or nothing', () => {
		expect(seenWhenText({ seenWhen: 'site RTU9 came back' })).toBe('seen when site RTU9 came back');
		expect(seenWhenText({})).toBe('');
	});
});

type A = Parameters<typeof focusSplit>[0][number];
const al = (name: string, o: Partial<A> = {}): A => ({ name, priority: 'medium', activeMs: 100, state: 'unack-active', ...o });

describe('flood focus', () => {
	const flood = { sinceMs: 10_000, windowMs: 5000 };
	it('puts the highest priority first and the oldest first within it', () => {
		const { first, also } = focusSplit(
			[al('low', { priority: 'low' }), al('crit-late', { priority: 'critical', activeMs: 2000 }), al('crit-early', { priority: 'critical', activeMs: 1500 }), al('med')],
			flood
		);
		expect(first.map((a) => a.name)).toEqual(['crit-early', 'crit-late', 'med', 'low']);
		expect(also.length).toBe(0);
	});
	it('puts a chattering alarm and one acknowledged before the flood under also standing', () => {
		const alarms = [
			al('chatty', { priority: 'high', chatter: chat() }),
			al('old-acked', { state: 'ack-active', activeMs: 4000 }),
			al('new-acked', { state: 'ack-active', activeMs: 6000 }),
			al('fresh', { priority: 'high', activeMs: 7000 })
		];
		const { first, also } = focusSplit(alarms, flood);
		expect(first.map((a) => a.name)).toEqual(['fresh', 'new-acked']);
		expect(also.map((a) => a.name)).toEqual(['chatty', 'old-acked']);
	});
	it('keeps an unacknowledged critical alarm in the main list even when it chatters, and collapses it once acknowledged', () => {
		const unack = al('crit-unack', { priority: 'critical', state: 'unack-active', chatter: chat() });
		const rtn = al('crit-rtn', { priority: 'critical', state: 'unack-rtn', chatter: chat() });
		const acked = al('crit-acked', { priority: 'critical', state: 'ack-active', chatter: chat() });
		const highUnack = al('high-unack', { priority: 'high', state: 'unack-active', chatter: chat() });
		const { first, also } = focusSplit([unack, rtn, acked, highUnack], flood);
		expect(first.map((a) => a.name)).toEqual(['crit-rtn', 'crit-unack']);
		expect(also.map((a) => a.name)).toEqual(['crit-acked', 'high-unack']);
	});
	it('does not call a first-out alarm stale just because the operator acknowledged it', () => {
		// activated inside the window before the one that tipped the flood
		const { first, also } = focusSplit([al('cause', { state: 'ack-active', activeMs: 9000 })], flood);
		expect(first.length).toBe(1);
		expect(also.length).toBe(0);
	});
	it('never drops an alarm, and treats nothing as stale with no flood', () => {
		const alarms = [al('a', { state: 'ack-active', activeMs: 1 }), al('b', { chatter: chat() }), al('c')];
		const withFlood = focusSplit(alarms, flood);
		expect(withFlood.first.length + withFlood.also.length).toBe(3);
		const none = focusSplit(alarms, null);
		expect(none.first.map((a) => a.name).sort()).toEqual(['a', 'c']);
		expect(none.also.map((a) => a.name)).toEqual(['b']);
	});
});

const row = (o: Partial<AlarmEvent>): AlarmEvent => ({ ts: 5_000_000, id: 'x', name: 'x', kind: 'chatter', ...o });

describe('detection rows in the history', () => {
	it('are hidden unless asked for, and shown by their kind filter', () => {
		for (const k of DETECTION_KINDS) {
			expect(visibleKind({ kind: k }, '', false)).toBe(false);
			expect(visibleKind({ kind: k }, '', false, true)).toBe(true);
			expect(visibleKind({ kind: k }, k, false)).toBe(true);
		}
		expect(visibleKind({ kind: 'active' }, '', false)).toBe(true);
		expect(visibleKind({ kind: 'notify' }, '', false, true)).toBe(false);
	});
	it('are asked for by name, and the default query is unchanged', () => {
		expect(journalKinds(false)).toBe(undefined);
		expect(journalKinds(false, false)).toBe(undefined);
		const k = journalKinds(false, true) ?? [];
		expect(k.length).toBe(STATE_KINDS.length + DETECTION_KINDS.length);
		expect(k.includes('notify')).toBe(false);
		expect(k.includes('flood-end')).toBe(true);
		const both = journalKinds(true, true) ?? [];
		expect(both.length).toBe(STATE_KINDS.length + DETECTION_KINDS.length + 1);
	});
	it('say what began and ended, with the numbers the controller recorded', () => {
		expect(detectionEvent(row({ kind: 'flood-end' }))).toBe('Alarm flood ended');
		expect(detectionEvent(row({ kind: 'ack' }))).toBe(undefined);
		expect(detectionOutcome(row({ kind: 'chatter', count: 3, windowMs: 60_000 }))).toBe('3 activations in 60 s');
		expect(
			detectionOutcome(row({ kind: 'flood-end', count: 40, peak: 31, windowMs: 600_000, sinceMs: 5_000_000 - 900_000 }))
		).toBe('40 activations, lasted 15 min, peak 31 in 10 min');
		expect(detectionOutcome(row({ kind: 'flood', count: 11, windowMs: 600_000, reason: 'first out: A, B, C' }))).toBe(
			'11 activations in 10 min; first out: A, B, C'
		);
	});
});
