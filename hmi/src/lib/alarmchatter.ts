// What the HMI says about chattering alarms and alarm floods (#1249), pure so
// it has tests. The controller detects both; this only words them. Nothing
// here hides or acknowledges an alarm for the operator.

import { PRIORITY_ORDER } from './alarms.svelte.js';
import type { AlarmChatter, AlarmEvent, AlarmFloodBrief, AlarmFloodGroup, AlarmInstance } from './alarms.svelte.js';

/** "60 s", "10 min", "2 h 5 min": a duration for a sentence. */
export function spanText(ms: number): string {
	const s = Math.max(0, Math.round(ms / 1000));
	if (s < 120) return `${s} s`;
	const m = Math.round(s / 60);
	if (m < 120) return `${m} min`;
	const h = Math.floor(m / 60);
	return m % 60 ? `${h} h ${m % 60} min` : `${h} h`;
}

/** The short badge text: "chattering ×7". */
export function chatterBadge(c: AlarmChatter): string {
	return `chattering ×${c.activations}`;
}

/** The sentence under it: how many times, since when, against which rule. */
export function chatterText(c: AlarmChatter, now: number): string {
	const age = spanText(now - c.sinceMs);
	const eps = c.episodes > 1 ? `, episode ${c.episodes} since the controller started` : '';
	return `Chattering: ${c.activations} activations in ${age}; ${c.count} in ${spanText(c.windowMs)} counts as chatter${eps}.`;
}

/** "Alarm flood: 23 in 10 min (limit 10)"; "at least" when the ring overflowed. */
export function floodText(f: AlarmFloodBrief): string {
	const n = f.saturated ? `at least ${f.count}` : String(f.count);
	return `Alarm flood: ${n} in ${spanText(f.windowMs)} (limit ${f.threshold})`;
}

/** "P101 (8), P102 (6)": the equipment that raised most of a flood. */
export function groupsText(groups: AlarmFloodGroup[]): string {
	return groups.map((g) => `${g.equipment} (${g.count})`).join(', ');
}

/** "seen when site RTU9 came back": the row's own words, when it has them. */
export function seenWhenText(a: Pick<AlarmInstance, 'seenWhen'>): string {
	return a.seenWhen ? `seen when ${a.seenWhen}` : '';
}

/** The alarms behind the Chattering filter chip. */
export function chatteringIds(alarms: Pick<AlarmInstance, 'id' | 'chatter'>[]): Set<string> {
	return new Set(alarms.filter((a) => a.chatter).map((a) => a.id));
}

/**
 * Flood focus: the alarms that need a person first, highest priority first
 * and oldest first within a priority, and those that can wait under "also
 * standing": a chattering alarm, and one acknowledged before the flood
 * began. An unacknowledged critical alarm always stays in the first list. Every alarm is in exactly one list; none is dropped.
 */
export function focusSplit<T extends Pick<AlarmInstance, 'priority' | 'activeMs' | 'chatter' | 'state' | 'name'>>(
	alarms: T[],
	flood: Pick<AlarmFloodBrief, 'sinceMs'> | null
): { first: T[]; also: T[] } {
	const order = (a: T, b: T) =>
		PRIORITY_ORDER.indexOf(b.priority) - PRIORITY_ORDER.indexOf(a.priority) ||
		(a.activeMs ?? 0) - (b.activeMs ?? 0) ||
		a.name.localeCompare(b.name);
	const first: T[] = [];
	const also: T[] = [];
	for (const a of alarms) {
		const stale = a.state === 'ack-active' && flood !== null && (a.activeMs ?? 0) < flood.sinceMs;
		// An unacknowledged critical alarm is never buried, chattering or not:
		// during a flood is when it matters most (naut-lead, #1249).
		const unackedCritical = a.priority === 'critical' && (a.state === 'unack-active' || a.state === 'unack-rtn');
		(!unackedCritical && (a.chatter || stale) ? also : first).push(a);
	}
	return { first: first.sort(order), also: also.sort(order) };
}

/** The Event cell of a detection row; undefined for any other kind. */
export function detectionEvent(e: AlarmEvent): string | undefined {
	switch (e.kind) {
		case 'chatter':
			return 'Chattering began';
		case 'chatter-end':
			return 'Chattering ended';
		case 'flood':
			return 'Alarm flood began';
		case 'flood-end':
			return 'Alarm flood ended';
		default:
			return undefined;
	}
}

/** The State After cell of a detection row: the numbers the controller recorded. */
export function detectionOutcome(e: AlarmEvent): string {
	const win = e.windowMs ? ` in ${spanText(e.windowMs)}` : '';
	const n = e.count ?? 0;
	switch (e.kind) {
		case 'chatter':
			return `${n} activations${win}`;
		case 'chatter-end':
		case 'flood-end': {
			const len = e.sinceMs ? `, lasted ${spanText(e.ts - e.sinceMs)}` : '';
			const peak = e.peak ? `, peak ${e.peak}${win}` : '';
			return `${n} activations${len}${peak}`;
		}
		case 'flood':
			return `${n} activations${win}${e.reason ? `; ${e.reason}` : ''}`;
		default:
			return '';
	}
}
