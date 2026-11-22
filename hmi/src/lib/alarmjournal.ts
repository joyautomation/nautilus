// What the alarm history shows for each journal row, pure so it has tests.
//
// A paging row (kind "notify", #715) is not a state change: it records one
// notification an escalation policy sent, or tried to. The controller's
// journal leaves it out unless a client names kinds, so a host that wants
// the paging rows asks with `journalKinds(true)`; the history view then
// shows them as their own row kind, hidden until the operator turns them
// on (or filters by Paged), so a screen that never paged looks as it did.

import type { AlarmEvent, AlarmEventKind } from './alarms.svelte.js';

/** The state-change kinds, in the controller's order. */
export const STATE_KINDS: AlarmEventKind[] = ['active', 'rtn', 'ack', 'shelve', 'unshelve', 'suppress', 'unsuppress'];

/**
 * The `kinds` a host passes to `AlarmClient.journal` to get paging rows
 * too: every state kind plus notify. Without `withPaging` it is undefined,
 * which is the controller's default (state changes only).
 */
export function journalKinds(withPaging: boolean): AlarmEventKind[] | undefined {
	return withPaging ? [...STATE_KINDS, 'notify'] : undefined;
}

/** Whether a row shows: a paging row only when paging is on or Paged is the kind filter. */
export function visibleKind(e: Pick<AlarmEvent, 'kind'>, filterKind: AlarmEventKind | '', showPaging: boolean): boolean {
	if (filterKind) return e.kind === filterKind;
	return e.kind !== 'notify' || showPaging;
}

/** "Paged, level 2" for a paging row. */
export function pagingEvent(e: AlarmEvent): string {
	return e.level ? `Paged, level ${e.level}` : 'Paged';
}

/**
 * The State After cell of a paging row: who was paged, by which channel and
 * how it went, e.g. "sent to bob by sms" or "nobody on call (joined late)".
 */
export function pagingOutcome(e: AlarmEvent): string {
	const result = (e.result ?? '').replace(/-/g, ' ') || 'paged';
	const who = e.to ? ` to ${e.to}` : '';
	const via = e.via ? ` by ${e.via}` : '';
	const why = e.reason ? ` (${e.reason})` : '';
	return `${result}${who}${via}${why}`;
}
