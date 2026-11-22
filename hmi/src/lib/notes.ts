// Operator notes and the shift log (#720): the client for /api/notes, the
// draft keeper, and the pure rules the panel and the shift log render from.
// No runes, no DOM; tested in tests/notes.test.ts with a fake fetch and a
// fake storage, because what it must never do is quiet:
//
//   - lose what a person wrote: an answer that did not keep the note (a pair
//     with no majority, an unconfirmed proposal, a lost race, a refusal) says
//     why and the text stays as a draft until it is kept;
//   - show one person's unsent text to the next person at a shared terminal:
//     a draft is kept per principal and per target, and a sign-out clears the
//     person's drafts;
//   - call an unknown outcome a failure or a success: it says "may have been
//     kept" and gives the note's id to check;
//   - offer to edit what the controller will refuse (past the author's hour,
//     someone else's note), though the controller decides every request.

import { kitFetch, type Fetch } from './kitfetch.js';
import { refusalMessage } from './auth.js';

export type NoteTargetKind = 'asset' | 'tag' | 'alarm' | 'area' | 'log';

export interface NoteTarget {
	kind: NoteTargetKind;
	id?: string;
	/** An alarm note: the occurrence's id, and when it went active. */
	occurrence?: string;
	activeMs?: number;
}

export interface Note {
	id: string;
	target: NoteTarget;
	kind: 'note' | 'handover';
	re?: string;
	text: string;
	by: string;
	principal?: string;
	/** When it was first written, epoch ms. */
	at: number;
	revisions: number;
	edited?: boolean;
	editedAt?: number;
	editedBy?: string;
	retracted?: boolean;
	reason?: string;
	/** When the author's hour ends, epoch ms. */
	editableUntil?: number;
}

export interface NoteRevision {
	note: string;
	rev: number;
	at: number;
	by: string;
	principal?: string;
	text: string;
	retract?: boolean;
	reason?: string;
	override?: boolean;
}

export interface NoteQuery {
	target?: NoteTargetKind;
	id?: string;
	occurrence?: string;
	kind?: 'note' | 'handover';
	author?: string;
	since?: number;
	until?: number;
	limit?: number;
	retracted?: boolean;
}

/** A replaced log history's notes: a record, read-only, never live. */
export interface SupersededHistory {
	by?: string;
	movedAt?: number;
}

/** Why a write did not keep the note. */
export type NoteOutcome =
	/** Refused before anything was proposed: nothing was kept, and nothing is queued. */
	| 'refused'
	/** The controller could not confirm: the note may have been kept. */
	| 'unknown'
	/** Another change to the note won: reload and decide again. */
	| 'conflict'
	/** Not signed in. */
	| 'signin'
	/** Not allowed (the controller says why). */
	| 'forbidden'
	/** The note itself was refused (too long, no such tag...). */
	| 'invalid'
	/** The controller did not answer. */
	| 'down';

export type NoteAnswer<T> =
	| { ok: true; value: T }
	| { ok: false; status: number; message: string; outcome: NoteOutcome; /** set when the outcome is unknown */ noteId?: string };

export interface NotesClientOptions {
	/** Default `/api/notes`. */
	url?: string;
	token?: string;
	fetch?: Fetch;
}

const UNREACHABLE = 'the controller did not answer';

export class NotesClient {
	readonly #url: string;
	readonly #token?: string;
	readonly #fetch: Fetch;

	constructor(opts: NotesClientOptions = {}) {
		this.#url = opts.url ?? '/api/notes';
		this.#token = opts.token;
		this.#fetch = opts.fetch ?? kitFetch;
	}

	#headers(json = false): Record<string, string> {
		const h: Record<string, string> = {};
		if (this.#token) h['X-Nautilus-Token'] = this.#token;
		if (json) h['Content-Type'] = 'application/json';
		return h;
	}

	async #do<T>(path: string, init: RequestInit, pick: (j: any) => T | undefined, failed: string): Promise<NoteAnswer<T>> {
		const write = init.method === 'POST';
		let res: Response;
		try {
			res = await this.#fetch(this.#url + path, init);
		} catch {
			// a write whose answer never came may have been kept: only a read is simply down
			return write ? { ok: false, status: 0, message: UNREACHABLE, outcome: 'unknown' } : { ok: false, status: 0, message: UNREACHABLE, outcome: 'down' };
		}
		const text = await res.text();
		if (!res.ok) return classifyFailure(res.status, res.headers.get('X-Nautilus-Outcome'), text, failed, write);
		let j: unknown;
		try {
			j = text ? JSON.parse(text) : undefined;
		} catch {
			return { ok: false, status: res.status, message: 'the controller answered something that is not JSON', outcome: 'down' };
		}
		const v = pick(j);
		if (v === undefined) return { ok: false, status: res.status, message: 'the controller answered something this page does not understand', outcome: 'down' };
		return { ok: true, value: v };
	}

	/** The live notes, newest first. 404 (outcome `invalid`): this controller keeps none. */
	list(q: NoteQuery = {}): Promise<NoteAnswer<Note[]>> {
		return this.#do(queryString(q), { headers: this.#headers() }, (j) => (Array.isArray(j?.notes) ? (j.notes as Note[]) : undefined), 'reading the notes failed');
	}

	/** The notes of a log history that was replaced: a record, never live. */
	superseded(q: NoteQuery = {}): Promise<NoteAnswer<{ notes: Note[]; history?: SupersededHistory }>> {
		return this.#do(queryString({ ...q }, true), { headers: this.#headers() }, (j) => (Array.isArray(j?.notes) ? { notes: j.notes as Note[], history: j.history as SupersededHistory | undefined } : undefined), 'reading the superseded notes failed');
	}

	/** A note with every revision. */
	get(id: string): Promise<NoteAnswer<{ note: Note; history: NoteRevision[] }>> {
		return this.#do('/' + encodeURIComponent(id), { headers: this.#headers() }, (j) => (j && typeof j.id === 'string' ? { note: j as Note, history: (j.history ?? []) as NoteRevision[] } : undefined), 'reading the note failed');
	}

	/** How many live notes each target of a kind has. */
	counts(kind: Exclude<NoteTargetKind, 'log'>): Promise<NoteAnswer<Record<string, number>>> {
		return this.#do('/counts?target=' + kind, { headers: this.#headers() }, (j) => (j && typeof j.counts === 'object' ? (j.counts as Record<string, number>) : undefined), 'reading the counts failed');
	}

	add(body: { target?: NoteTarget; text: string; kind?: 'note' | 'handover'; re?: string }): Promise<NoteAnswer<Note>> {
		return this.#do('', { method: 'POST', headers: this.#headers(true), body: JSON.stringify(body) }, noteOf, 'the note was not kept');
	}

	revise(id: string, text: string): Promise<NoteAnswer<Note>> {
		return this.#do('/' + encodeURIComponent(id) + '/revise', { method: 'POST', headers: this.#headers(true), body: JSON.stringify({ text }) }, noteOf, 'the change was not kept');
	}

	retract(id: string, reason: string): Promise<NoteAnswer<Note>> {
		return this.#do('/' + encodeURIComponent(id) + '/retract', { method: 'POST', headers: this.#headers(true), body: JSON.stringify({ reason }) }, noteOf, 'the retraction was not kept');
	}
}

function noteOf(j: any): Note | undefined {
	return j && typeof j.id === 'string' ? (j as Note) : undefined;
}

function queryString(q: NoteQuery, superseded = false): string {
	const p = new URLSearchParams();
	if (q.target) p.set('target', q.target);
	if (q.id) p.set('id', q.id);
	if (q.occurrence) p.set('occurrence', q.occurrence);
	if (q.kind) p.set('kind', q.kind);
	if (q.author) p.set('author', q.author);
	if (q.since) p.set('since', String(q.since));
	if (q.until) p.set('until', String(q.until));
	if (q.limit) p.set('limit', String(q.limit));
	if (q.retracted) p.set('retracted', '1');
	if (superseded) p.set('superseded', '1');
	const s = p.toString();
	return s ? '?' + s : '';
}

/**
 * Say why a write was not kept, in the controller's words, and what the
 * page must do about it. A 503 marked `refused` was stopped before anything
 * was proposed (nothing is queued); one marked `unknown` may have been kept.
 */
export function classifyFailure(status: number, outcomeHeader: string | null, body: string, failed: string, write = false): Extract<NoteAnswer<never>, { ok: false }> {
	let j: { error?: unknown; note?: unknown } | undefined;
	try {
		j = JSON.parse(body);
	} catch {
		/* plain text */
	}
	const message = refusalMessage(status, body, failed);
	const noteId = typeof j?.note === 'string' ? j.note : undefined;
	// Only an answer the controller marked "refused" is certain: it stopped
	// before anything was proposed. A 503 or a 5xx without the mark (a proxy's,
	// a crash) may have come after the proposal, so a write is "unknown", never
	// "nothing is waiting".
	if (status >= 500 && outcomeHeader === 'refused') return { ok: false, status, message, outcome: 'refused' };
	if (status === 503 && outcomeHeader === 'unknown') return { ok: false, status, message, outcome: 'unknown', noteId };
	if (status === 409) return { ok: false, status, message, outcome: 'conflict' };
	if (status === 401) return { ok: false, status, message, outcome: 'signin' };
	if (status === 403) return { ok: false, status, message, outcome: 'forbidden' };
	if (status >= 400 && status < 500) return { ok: false, status, message, outcome: 'invalid' };
	return { ok: false, status, message, outcome: write ? 'unknown' : 'down', noteId };
}

/** What to tell a person about a note that was not kept. */
export function notKeptText(a: Extract<NoteAnswer<never>, { ok: false }>): string {
	switch (a.outcome) {
		case 'refused':
			return `Not kept, and nothing is waiting to be kept: ${a.message}. Your text is still here; try again when the controllers can agree.`;
		case 'unknown':
			return `It may have been kept: ${a.message}.${a.noteId ? ' Check the list before sending it again.' : ' Look at the list before sending it again.'} Your text is still here.`;
		case 'conflict':
			return `${a.message}. Your text is still here.`;
		case 'signin':
			return `Sign in to write a note (${a.message}). Your text is still here.`;
		default:
			return `${a.message}. Your text is still here.`;
	}
}

// ---- who may change a note, as far as this page can tell

/** The author's hour, by the stamp's own clock, with a few minutes of skew. */
export const EDIT_WINDOW_MS = 60 * 60 * 1000;
export const CLOCK_SKEW_MS = 5 * 60 * 1000;

/**
 * Whether the page should offer to change `note` as `principal`: the
 * author, inside their hour, not retracted. A supervisor's retraction at any
 * time is offered by `canRetractAnytime`. The controller decides every
 * request: this only keeps the page from offering what it will refuse.
 */
export function canChange(note: Note, principal: string | undefined, nowMs: number): boolean {
	if (note.retracted || !principal || !note.principal || note.principal !== principal) return false;
	const age = nowMs - note.at;
	return age <= EDIT_WINDOW_MS && age >= -CLOCK_SKEW_MS;
}

// ---- the shift log's order

/** The local calendar day a time falls on, as YYYY-MM-DD. */
export function dayKey(ms: number, tzOffsetMin = new Date(ms).getTimezoneOffset()): string {
	const d = new Date(ms - tzOffsetMin * 60000);
	return d.toISOString().slice(0, 10);
}

export interface DayGroup {
	day: string;
	notes: Note[];
}

/**
 * The shift log's order: newest day first; inside a day, handovers on top
 * (the next shift reads them first), then the rest newest first. A retracted
 * note is not in the log.
 */
export function groupByDay(notes: Note[], tzOffsetMin?: number): DayGroup[] {
	const by = new Map<string, Note[]>();
	for (const n of notes) {
		if (n.retracted) continue;
		const k = dayKey(n.at, tzOffsetMin ?? new Date(n.at).getTimezoneOffset());
		(by.get(k) ?? by.set(k, []).get(k)!).push(n);
	}
	return [...by.entries()]
		.sort((a, b) => (a[0] < b[0] ? 1 : -1))
		.map(([day, ns]) => ({
			day,
			notes: ns.sort((a, b) => (a.kind === 'handover' ? 0 : 1) - (b.kind === 'handover' ? 0 : 1) || b.at - a.at)
		}));
}

/** A one-line label for a note's target. */
export function targetLabel(t: NoteTarget): string {
	switch (t.kind) {
		case 'log':
			return 'shift log';
		case 'alarm':
			return `alarm ${t.id}${t.occurrence ? ` (occurrence ${t.occurrence.slice(0, 8)})` : ''}`;
		default:
			return `${t.kind} ${t.id ?? ''}`.trim();
	}
}

/** The key a target's draft is kept under. */
export function targetKey(t: NoteTarget): string {
	return [t.kind, t.id ?? '', t.occurrence ?? ''].join('|');
}

// ---- drafts

/** The few storage calls the keeper needs; localStorage satisfies it. */
export interface DraftStorage {
	getItem(k: string): string | null;
	setItem(k: string, v: string): void;
	removeItem(k: string): void;
	key(i: number): string | null;
	readonly length: number;
}

const PREFIX = 'nautilus.notes.draft:';

/**
 * Unsent note text, kept so a refused or unconfirmed write does not lose it,
 * and so a page reload does not. A draft belongs to a principal and a target:
 * nobody else sees it, and `clearFor` (a sign-out) takes a person's away, for
 * a terminal several people share. Storage that is missing or refuses (a
 * private window) falls back to memory for the life of the page.
 */
export class DraftKeeper {
	readonly #store: DraftStorage | null;
	readonly #mem = new Map<string, string>();

	constructor(store?: DraftStorage | null) {
		this.#store = store === undefined ? browserStorage() : store;
	}

	#key(principal: string, t: NoteTarget): string {
		return PREFIX + encodeURIComponent(principal) + ':' + encodeURIComponent(targetKey(t));
	}

	/** Keep (or, with empty text, drop) a person's draft for a target. */
	save(principal: string | undefined, t: NoteTarget, text: string): void {
		if (!principal) return; // nobody to keep it for: a draft is never anonymous
		const k = this.#key(principal, t);
		if (text === '') return this.clear(principal, t);
		this.#mem.set(k, text);
		try {
			this.#store?.setItem(k, text);
		} catch {
			/* memory holds it */
		}
	}

	load(principal: string | undefined, t: NoteTarget): string {
		if (!principal) return '';
		const k = this.#key(principal, t);
		try {
			const v = this.#store?.getItem(k);
			if (v != null) return v;
		} catch {
			/* memory */
		}
		return this.#mem.get(k) ?? '';
	}

	clear(principal: string | undefined, t: NoteTarget): void {
		if (!principal) return;
		const k = this.#key(principal, t);
		this.#mem.delete(k);
		try {
			this.#store?.removeItem(k);
		} catch {
			/* memory is clear */
		}
	}

	/**
	 * Take away every draft that is not `principal`'s: someone else is signed
	 * in on this terminal now, and what the person before them left unsent is
	 * not theirs to see or to find in the browser's storage. (A page loaded
	 * signed out keeps them: the same person may be about to sign in again.)
	 */
	clearOthers(principal: string | undefined): void {
		if (!principal) return;
		const mine = PREFIX + encodeURIComponent(principal) + ':';
		for (const k of [...this.#mem.keys()]) if (k.startsWith(PREFIX) && !k.startsWith(mine)) this.#mem.delete(k);
		try {
			const s = this.#store;
			if (!s) return;
			const keys: string[] = [];
			for (let i = 0; i < s.length; i++) {
				const k = s.key(i);
				if (k && k.startsWith(PREFIX) && !k.startsWith(mine)) keys.push(k);
			}
			for (const k of keys) s.removeItem(k);
		} catch {
			/* nothing more to do */
		}
	}

	/** Take away every draft a person has (a sign-out). */
	clearFor(principal: string | undefined): void {
		if (!principal) return;
		const mine = PREFIX + encodeURIComponent(principal) + ':';
		for (const k of [...this.#mem.keys()]) if (k.startsWith(mine)) this.#mem.delete(k);
		try {
			const s = this.#store;
			if (!s) return;
			const keys: string[] = [];
			for (let i = 0; i < s.length; i++) {
				const k = s.key(i);
				if (k && k.startsWith(mine)) keys.push(k);
			}
			for (const k of keys) s.removeItem(k);
		} catch {
			/* nothing more to do */
		}
	}
}

/**
 * What the box holds when the person signed in changes, and what is kept.
 * A session that ends (or a refresh of who is signed in that finds nobody)
 * takes nothing away: the text stays where it is, for the same person to
 * come back to; the explicit sign-out button clears their drafts
 * (`clearFor`, from the kit's sign-out). A person who is only known after
 * the page started keeps what they already typed. A different person gets
 * their own draft, and the previous person's are gone from storage.
 */
export function principalChanged(drafts: DraftKeeper, prev: string | undefined, next: string | undefined, target: NoteTarget, text: string): string {
	if (next === prev || next === undefined) return text;
	drafts.clearOthers(next);
	if (prev === undefined && text.trim() !== '') {
		drafts.save(next, target, text); // typed before it was known who: theirs now
		return text;
	}
	return drafts.load(next, target);
}

function browserStorage(): DraftStorage | null {
	try {
		return globalThis.localStorage ?? null;
	} catch {
		return null; // blocked storage throws on access
	}
}
