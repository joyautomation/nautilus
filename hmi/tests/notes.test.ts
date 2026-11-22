// The notes client's contract (#720): a write that did not keep the note
// says so and why (refused, unknown, lost a race), the draft is never lost
// and never shown to the next person at a shared terminal, and the page
// offers a change only where the controller would take it.
import { describe, it, expect } from './harness.js';
import {
	NotesClient,
	DraftKeeper,
	principalChanged,
	classifyFailure,
	notKeptText,
	canChange,
	groupByDay,
	dayKey,
	targetLabel,
	targetKey,
	noteTime,
	type Note,
	type DraftStorage
} from '../src/lib/notes.js';

type Sent = { url: string; method: string; body: any };
function fake(answers: Array<Response | Error>) {
	const sent: Sent[] = [];
	const f = (async (url: string, init?: RequestInit) => {
		sent.push({ url, method: init?.method ?? 'GET', body: init?.body ? JSON.parse(String(init.body)) : undefined });
		const a = answers.shift();
		if (!a) throw new Error('no more answers');
		if (a instanceof Error) throw a;
		return a;
	}) as unknown as typeof fetch;
	return { sent, client: new NotesClient({ fetch: f, token: 't' }) };
}
const json = (b: unknown, status = 200, headers: Record<string, string> = {}) => new Response(JSON.stringify(b), { status, headers });
const text = (b: string, status: number, headers: Record<string, string> = {}) => new Response(b, { status, headers });

const note = (o: Partial<Note> = {}): Note => ({
	id: 'n1', target: { kind: 'log' }, kind: 'note', text: 'pump 2 vibrating', by: 'alice', principal: 'user:alice',
	at: 1_700_000_000_000, revisions: 1, ...o
});

describe('NotesClient', () => {
	it('lists with the query, newest first as the controller says', async () => {
		const { client, sent } = fake([json({ notes: [note()] })]);
		const r = await client.list({ target: 'alarm', id: 'Hi Temp', occurrence: '00ff', retracted: true, limit: 5 });
		expect(r.ok && r.value.length).toBe(1);
		expect(sent[0].url).toBe('/api/notes?target=alarm&id=Hi+Temp&occurrence=00ff&limit=5&retracted=1');
	});
	it('reads a superseded history and says it is one', async () => {
		const { client, sent } = fake([json({ notes: [note({ id: 'old' })], history: { by: 'akhan', movedAt: 5 } })]);
		const r = await client.superseded({ target: 'log' });
		expect(r.ok && r.value.history?.by).toBe('akhan');
		expect(sent[0].url).toBe('/api/notes?target=log&superseded=1');
	});
	it('adds, revises and retracts with the bodies the controller reads', async () => {
		const { client, sent } = fake([json(note(), 201), json(note({ revisions: 2 })), json(note({ retracted: true }))]);
		await client.add({ target: { kind: 'tag', id: 'SP' }, text: 'raised', kind: 'handover' });
		await client.revise('n1', 'raised to 70');
		await client.retract('n1', 'wrong tag');
		expect(sent.map((s) => s.method + ' ' + s.url).join(',')).toBe('POST /api/notes,POST /api/notes/n1/revise,POST /api/notes/n1/retract');
		expect(sent[0].body).toEqual({ target: { kind: 'tag', id: 'SP' }, text: 'raised', kind: 'handover' });
		expect(sent[1].body).toEqual({ text: 'raised to 70' });
		expect(sent[2].body).toEqual({ reason: 'wrong tag' });
	});
	it('reads one note with its history, and the counts', async () => {
		const { client } = fake([json({ ...note(), history: [{ note: 'n1', rev: 1, at: 1, by: 'alice', text: 'first' }] }), json({ target: 'tag', counts: { SP: 2 } })]);
		const g = await client.get('n1');
		expect(g.ok && g.value.history.length).toBe(1);
		const c = await client.counts('tag');
		expect(c.ok && c.value.SP).toBe(2);
	});
	it('a write whose answer never came, or came from a proxy, may have been kept', async () => {
		const { client } = fake([new Error('reset'), text('Bad Gateway', 502), text('', 503), new Error('reset')]);
		const a = await client.add({ text: 'x' });
		expect(!a.ok && a.outcome).toBe('unknown');
		const b = await client.revise('n1', 'x');
		expect(!b.ok && b.outcome).toBe('unknown');
		const c = await client.retract('n1', 'x');
		expect(!c.ok && c.outcome).toBe('unknown');
		expect(!c.ok && notKeptText(c).includes('nothing is waiting')).toBe(false);
		const d = await client.list(); // a read that fails is simply down
		expect(!d.ok && d.outcome).toBe('down');
	});
	it('an answer it does not understand is not a success; a dead controller is down', async () => {
		const { client } = fake([json({}), json({ nope: 1 }), new Error('down'), text('not json', 200)]);
		expect((await client.list()).ok).toBe(false);
		expect((await client.add({ text: 'x' })).ok).toBe(false);
		const d = await client.list();
		expect(!d.ok && d.outcome).toBe('down');
		const j = await client.list();
		expect(!j.ok && j.outcome).toBe('down');
	});
});

describe('a write that did not keep the note says why', () => {
	it('a refusal before anything was proposed is "refused": nothing is queued', async () => {
		const { client } = fake([text('operator-intent log has no majority: neither the other controller nor the witness answers; notes cannot be written until one of them returns', 503, { 'X-Nautilus-Outcome': 'refused' })]);
		const r = await client.add({ text: 'x', target: { kind: 'log' } });
		expect(!r.ok && r.outcome).toBe('refused');
		expect(!r.ok && notKeptText(r).includes('nothing is waiting to be kept')).toBe(true);
		expect(!r.ok && notKeptText(r).includes('Your text is still here')).toBe(true);
	});
	it('an unconfirmed proposal is "unknown", with the note id to check, never "failed"', async () => {
		const { client } = fake([json({ error: 'outcome unknown: the note was passed to the replicated log but not confirmed', note: 'abc' }, 503, { 'X-Nautilus-Outcome': 'unknown' })]);
		const r = await client.add({ text: 'x', target: { kind: 'log' } });
		expect(!r.ok && r.outcome).toBe('unknown');
		expect(!r.ok && r.noteId).toBe('abc');
		expect(!r.ok && notKeptText(r).startsWith('It may have been kept')).toBe(true);
		expect(!r.ok && notKeptText(r).includes('failed')).toBe(false);
	});
	it('a lost race, a sign-in, a refusal by role and a bad note each have their own outcome', () => {
		const o = (s: number, h: string | null = null) => classifyFailure(s, h, JSON.stringify({ error: 'because' }), 'failed').outcome;
		expect(o(409)).toBe('conflict');
		expect(o(401)).toBe('signin');
		expect(o(403)).toBe('forbidden');
		expect(o(400)).toBe('invalid');
		expect(o(422)).toBe('invalid');
		expect(o(404)).toBe('invalid');
		expect(o(500)).toBe('down');
		// without the controller's mark a 503 is nobody's promise: not "nothing is waiting"
		expect(o(503)).toBe('down');
		expect(classifyFailure(503, null, '', 'failed', true).outcome).toBe('unknown');
		expect(classifyFailure(502, null, 'Bad Gateway', 'failed', true).outcome).toBe('unknown');
		expect(classifyFailure(500, null, '', 'failed', true).outcome).toBe('unknown');
		expect(classifyFailure(503, 'refused', 'no majority', 'failed', true).outcome).toBe('refused');
		// the controller marks its role and sign-in refusals "refused" too: they are a sign-in and a permission, not a pair without a majority
		expect(classifyFailure(401, 'refused', 'anonymous has no role here', 'failed', true).outcome).toBe('signin');
		expect(classifyFailure(403, 'refused', 'may not note', 'failed', true).outcome).toBe('forbidden');
		expect(classifyFailure(400, 'refused', 'no text', 'failed', true).outcome).toBe('invalid');
		expect(o(503, 'unknown')).toBe('unknown');
	});
	it('says the controller\'s own words, and the draft is always still there', () => {
		for (const outcome of ['refused', 'unknown', 'conflict', 'signin', 'forbidden', 'invalid', 'down'] as const) {
			const a = { ok: false as const, status: 400, message: 'because', outcome };
			expect(notKeptText(a).includes('Your text is still here') || notKeptText(a).includes('still here')).toBe(true);
		}
	});
});

describe('canChange: only what the controller would take', () => {
	const now = note().at + 10 * 60_000;
	it('the author, inside the hour', () => {
		expect(canChange(note(), 'user:alice', now)).toBe(true);
	});
	it('not another person, not past the hour, not a retracted note', () => {
		expect(canChange(note(), 'user:bob', now)).toBe(false);
		expect(canChange(note(), 'user:alice', note().at + 61 * 60_000)).toBe(false);
		expect(canChange(note({ retracted: true }), 'user:alice', now)).toBe(false);
		expect(canChange(note(), undefined, now)).toBe(false);
		expect(canChange(note({ principal: undefined }), 'user:alice', now)).toBe(false);
	});
	it('a stamp a little ahead is skew; one far ahead is outside the window', () => {
		expect(canChange(note({ at: now + 2 * 60_000 }), 'user:alice', now)).toBe(true);
		expect(canChange(note({ at: now + 2 * 3600_000 }), 'user:alice', now)).toBe(false);
	});
});

describe('the shift log\'s order', () => {
	const t = (h: number, m = 0) => Date.UTC(2026, 10, 22, h, m);
	it('newest day first, handovers on top of a day, then newest first; retracted notes are not in it', () => {
		const ns = [
			note({ id: 'a', at: t(8), text: 'morning' }),
			note({ id: 'b', at: t(14), text: 'afternoon' }),
			note({ id: 'c', at: t(9), kind: 'handover', text: 'handover' }),
			note({ id: 'd', at: Date.UTC(2026, 10, 21, 20), text: 'yesterday' }),
			note({ id: 'e', at: t(15), retracted: true, text: 'retracted' })
		];
		const g = groupByDay(ns, 0);
		expect(g.map((x) => x.day).join(',')).toBe('2026-11-22,2026-11-21');
		expect(g[0].notes.map((n) => n.id).join('')).toBe('cba');
		expect(g[1].notes.map((n) => n.id).join('')).toBe('d');
	});
	it('a day is the local day', () => {
		// 23:30 UTC on the 22nd is the 23rd at UTC+1
		expect(dayKey(Date.UTC(2026, 10, 22, 23, 30), 0)).toBe('2026-11-22');
		expect(dayKey(Date.UTC(2026, 10, 22, 23, 30), -60)).toBe('2026-11-23');
	});
});

describe('labels', () => {
	it('names a target in a line', () => {
		expect(targetLabel({ kind: 'log' })).toBe('shift log');
		expect(targetLabel({ kind: 'alarm', id: 'HiTemp', occurrence: '00ff00ff11' })).toBe('alarm HiTemp (occurrence 00ff00ff)');
		expect(targetLabel({ kind: 'asset', id: 'Pump 2' })).toBe('asset Pump 2');
		expect(targetKey({ kind: 'alarm', id: 'A', occurrence: 'x' })).toBe('alarm|A|x');
	});
});

function memoryStorage(): DraftStorage & { data: Map<string, string> } {
	const data = new Map<string, string>();
	return {
		data,
		getItem: (k) => data.get(k) ?? null,
		setItem: (k, v) => void data.set(k, v),
		removeItem: (k) => void data.delete(k),
		key: (i) => [...data.keys()][i] ?? null,
		get length() {
			return data.size;
		}
	};
}

describe('DraftKeeper: one person\'s unsent text is theirs', () => {
	const log = { kind: 'log' as const };
	it('keeps a draft per principal and per target', () => {
		const d = new DraftKeeper(memoryStorage());
		d.save('user:alice', log, 'pump 2 is vibrating');
		d.save('user:alice', { kind: 'tag', id: 'SP' }, 'about SP');
		expect(d.load('user:alice', log)).toBe('pump 2 is vibrating');
		expect(d.load('user:alice', { kind: 'tag', id: 'SP' })).toBe('about SP');
		expect(d.load('user:alice', { kind: 'tag', id: 'Out' })).toBe('');
		expect(d.load('user:bob', log)).toBe('');
	});
	it('the next person who signs in does not see the last one\'s text, and sign-out takes it away', () => {
		const s = memoryStorage();
		const d = new DraftKeeper(s);
		d.save('user:alice', log, 'unsent: the burner tripped twice');
		d.save('user:alice', { kind: 'asset', id: 'Burner 2' }, 'unsent too');
		d.save('user:bob', log, "bob's text");
		// alice signs out on a shared terminal
		d.clearFor('user:alice');
		expect(d.load('user:alice', log)).toBe('');
		expect(d.load('user:alice', { kind: 'asset', id: 'Burner 2' })).toBe('');
		expect([...s.data.values()].some((v) => v.includes('burner'))).toBe(false); // gone from storage too
		expect([...s.data.values()].some((v) => v.includes('unsent'))).toBe(false);
		expect(d.load('user:bob', log)).toBe("bob's text"); // another person's stays
		// a different person signing in on the same terminal finds nothing of alice's
		expect(new DraftKeeper(s).load('user:carol', log)).toBe('');
	});
	it('someone else signing in finds the last person\'s unsent text gone from storage too', () => {
		const s = memoryStorage();
		const d = new DraftKeeper(s);
		d.save('user:alice', log, 'alice left this unsent');
		d.save('user:alice', { kind: 'tag', id: 'SP' }, 'and this');
		// the page loads for bob: alice signed out some other way (a button, an expiry)
		const next = new DraftKeeper(s);
		next.clearOthers('user:bob');
		expect(s.data.size).toBe(0);
		expect(next.load('user:alice', log)).toBe('');
		// bob's own survive his own reload
		next.save('user:bob', log, "bob's draft");
		new DraftKeeper(s).clearOthers('user:bob');
		expect(new DraftKeeper(s).load('user:bob', log)).toBe("bob's draft");
		// a page loaded signed out takes nothing away: the same person may be about to sign in
		new DraftKeeper(s).clearOthers(undefined);
		expect(new DraftKeeper(s).load('user:bob', log)).toBe("bob's draft");
	});
	it('a principal that merely starts like another does not share its drafts', () => {
		const d = new DraftKeeper(memoryStorage());
		d.save('user:al', log, 'al');
		d.save('user:alice', log, 'alice');
		d.clearFor('user:al');
		expect(d.load('user:alice', log)).toBe('alice');
	});
	it('an empty text drops the draft; a draft is never kept for nobody', () => {
		const s = memoryStorage();
		const d = new DraftKeeper(s);
		d.save('user:alice', log, 'x');
		d.save('user:alice', log, '');
		expect(s.data.size).toBe(0);
		d.save(undefined, log, 'anonymous text');
		expect(s.data.size).toBe(0);
		expect(d.load(undefined, log)).toBe('');
	});
	it('survives a reload (a new keeper over the same storage)', () => {
		const s = memoryStorage();
		new DraftKeeper(s).save('user:alice', log, 'kept across a reload');
		expect(new DraftKeeper(s).load('user:alice', log)).toBe('kept across a reload');
	});
	it('storage that refuses falls back to memory, and clearFor still clears', () => {
		const refusing: DraftStorage = {
			getItem: () => { throw new Error('blocked'); },
			setItem: () => { throw new Error('blocked'); },
			removeItem: () => { throw new Error('blocked'); },
			key: () => { throw new Error('blocked'); },
			length: 0
		};
		const d = new DraftKeeper(refusing);
		d.save('user:alice', log, 'in memory');
		expect(d.load('user:alice', log)).toBe('in memory');
		d.clearFor('user:alice');
		expect(d.load('user:alice', log)).toBe('');
		const none = new DraftKeeper(null);
		none.save('user:alice', log, 'x');
		expect(none.load('user:alice', log)).toBe('x');
	});
});

describe('principalChanged: whose text is in the box', () => {
	const log = { kind: 'log' as const };
	it('a person known late keeps what they already typed, and it is kept for them', () => {
		const s = memoryStorage();
		const d = new DraftKeeper(s);
		const text = principalChanged(d, undefined, 'user:alice', log, 'typed before /api/me answered');
		expect(text).toBe('typed before /api/me answered');
		expect(d.load('user:alice', log)).toBe('typed before /api/me answered');
	});
	it('a person known late with nothing typed gets their stored draft', () => {
		const d = new DraftKeeper(memoryStorage());
		d.save('user:alice', log, 'from before the reload');
		expect(principalChanged(d, undefined, 'user:alice', log, '')).toBe('from before the reload');
	});
	it('a session that ends or a refresh that finds nobody takes nothing away', () => {
		const s = memoryStorage();
		const d = new DraftKeeper(s);
		d.save('user:alice', log, 'mid-note when the session expired');
		expect(principalChanged(d, 'user:alice', undefined, log, 'mid-note when the session expired')).toBe('mid-note when the session expired');
		expect(d.load('user:alice', log)).toBe('mid-note when the session expired');
		// and she signs in again: her text is still the box's, and kept
		expect(principalChanged(d, undefined, 'user:alice', log, 'mid-note when the session expired')).toBe('mid-note when the session expired');
	});
	it('a different person gets their own draft and the last one is gone from storage', () => {
		const s = memoryStorage();
		const d = new DraftKeeper(s);
		d.save('user:alice', log, "alice's unsent");
		d.save('user:bob', log, "bob's own");
		expect(principalChanged(d, 'user:alice', 'user:bob', log, "alice's unsent")).toBe("bob's own");
		expect([...s.data.values()].some((v) => v.includes('alice'))).toBe(false);
		expect(principalChanged(d, 'user:bob', 'user:carol', log, "bob's own")).toBe('');
	});
	it('the same person again changes nothing', () => {
		const d = new DraftKeeper(memoryStorage());
		expect(principalChanged(d, 'user:alice', 'user:alice', log, 'x')).toBe('x');
	});
});

describe('a note\'s time (#714 demo: the asset page stuck on "Reading the notes…")', () => {
	it('is shown with its zone, and never throws', () => {
		// dateStyle + timeZoneName made every card throw in the browser
		const t = noteTime(Date.UTC(2026, 10, 22, 18, 0), 'en-US');
		expect(t.includes('2026')).toBe(true);
		expect(t.includes('Nov')).toBe(true);
		expect(/[A-Z]{2,5}|GMT|UTC/.test(t)).toBe(true);
	});
});
