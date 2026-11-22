// The alarm history's paging rows (#616, #715): which kinds a host asks for
// to get them, when a row shows, and what it says.
import { describe, it, expect } from './harness.js';
import { journalKinds, pagingEvent, pagingOutcome, visibleKind, STATE_KINDS } from '../src/lib/alarmjournal.js';
import type { AlarmEvent } from '../src/lib/alarms.svelte.js';

const page = (o: Partial<AlarmEvent> = {}): AlarmEvent => ({ ts: 1, id: 'HiTemp', name: 'Hi temp', kind: 'notify', ...o });

describe('asking for paging rows', () => {
	it('names every state kind plus notify, since the controller leaves notify out of an unnamed query', () => {
		expect(journalKinds(false)).toBe(undefined);
		const k = journalKinds(true) ?? [];
		expect(k.length).toBe(STATE_KINDS.length + 1);
		expect(k.includes('notify')).toBe(true);
		for (const s of STATE_KINDS) expect(k.includes(s)).toBe(true);
	});
});

describe('which rows show', () => {
	it('hides paging rows by default, shows them when on or when Paged is the filter', () => {
		expect(visibleKind(page(), '', false)).toBe(false);
		expect(visibleKind(page(), '', true)).toBe(true);
		expect(visibleKind(page(), 'notify', false)).toBe(true);
		expect(visibleKind({ kind: 'ack' }, '', false)).toBe(true);
		expect(visibleKind({ kind: 'ack' }, 'notify', true)).toBe(false);
		expect(visibleKind({ kind: 'notify' }, 'ack', true)).toBe(false);
	});
});

describe('what a paging row says', () => {
	it('the level and who was paged', () => {
		expect(pagingEvent(page({ level: 2 }))).toBe('Paged, level 2');
		expect(pagingEvent(page())).toBe('Paged');
		expect(pagingOutcome(page({ result: 'sent', to: 'bob', via: 'sms' }))).toBe('sent to bob by sms');
		expect(pagingOutcome(page({ result: 'nobody-on-call', reason: 'joined late' }))).toBe('nobody on call (joined late)');
		expect(pagingOutcome(page({ result: 'failed', to: 'alice', via: 'email', reason: 'smtp 550' }))).toBe('failed to alice by email (smtp 550)');
	});
});
