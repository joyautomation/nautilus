// The parameter client (#718 slice 5): what it asks, and what it says for each
// answer. A refused set carries its results; nothing is ever "empty" by error.
import { describe, it, expect } from './harness.js';
import { ParametersClient, driftBadge, drifted, rangeText, refusedText, takeRepoSet, valueText, type Parameter } from '../src/lib/parameters.js';

const json = (b: unknown, status = 200) => new Response(JSON.stringify(b), { status });

function client(answer: Response | Error) {
	const asked: { url: string; init?: RequestInit }[] = [];
	const c = new ParametersClient({
		fetch: async (i, init) => {
			asked.push({ url: String(i), init });
			if (answer instanceof Error) throw answer;
			return answer;
		}
	});
	return { c, asked };
}

const P = (name: string, state: Parameter['state'], extra: Partial<Parameter> = {}): Parameter => ({ name, value: 1, init: 2, state, ...extra });

describe('list', () => {
	it('reads /api/parameters with the write-back mode', async () => {
		const { c, asked } = client(json({ parameters: [P('A', 'in-step')], writeBack: 'commit' }));
		const r = await c.list();
		expect(asked[0].url).toBe('/api/parameters');
		expect(r).toEqual({ ok: true, parameters: [P('A', 'in-step')], writeBack: 'commit' });
	});
	it('an unknown write-back mode is off', async () => {
		const r = await client(json({ parameters: [], writeBack: 'bogus' })).c.list();
		expect(r.ok && r.writeBack).toBe('off');
	});
	it('says a refusal in the controller\'s words, a 404 as too old, a dead network as unreachable', async () => {
		expect(await client(json({ error: 'needs read' }, 403)).c.list()).toEqual({ ok: false, status: 403, message: 'needs read' });
		const old = await client(new Response('', { status: 404 })).c.list();
		expect(String(!old.ok && old.message).includes('too old')).toBe(true);
		expect(await client(new Error('x')).c.list()).toEqual({ ok: false, status: 0, message: 'controller unreachable' });
	});
});

describe('importSet', () => {
	it('posts version 1, the set and dryRun', async () => {
		const { c, asked } = client(json({ dryRun: true, applied: false, results: [{ name: 'A', status: 'change', from: 1, to: 2 }] }));
		const r = await c.importSet({ A: 2 }, true);
		expect(asked[0].url).toBe('/api/parameters/import');
		expect(asked[0].init?.method).toBe('POST');
		expect(JSON.parse(String(asked[0].init?.body))).toEqual({ version: 1, parameters: { A: 2 }, dryRun: true });
		expect(r.ok && r.applied).toBe(false);
	});
	it('a 422 is a refusal that names every refused tag and keeps the results', async () => {
		const results = [
			{ name: 'A', status: 'refused', reason: 'above max 90' },
			{ name: 'B', status: 'change' }
		];
		const r = await client(json({ dryRun: false, applied: false, results }, 422)).c.importSet({ A: 99, B: 1 });
		expect(r.ok).toBe(false);
		if (!r.ok) {
			expect(r.status).toBe(422);
			expect(r.message).toBe('refused, nothing written: A (above max 90)');
			expect(r.results).toEqual(results);
		}
	});
	it('a 403 names the tags, a 501 says it is a mirror', async () => {
		const r403 = await client(json({ error: 'not permitted to write: A' }, 403)).c.importSet({ A: 1 });
		expect(!r403.ok && r403.message).toBe('not permitted to write: A');
		const r501 = await client(new Response('', { status: 501 })).c.importSet({ A: 1 });
		expect(String(!r501.ok && r501.message).includes('mirrors')).toBe(true);
	});
	it('applied comes back true', async () => {
		const r = await client(json({ applied: true, results: [{ name: 'A', status: 'applied' }] })).c.importSet({ A: 1 });
		expect(r).toEqual({ ok: true, applied: true, results: [{ name: 'A', status: 'applied' }] });
	});
});

describe('helpers', () => {
	it('takeRepoSet takes only repo-ahead and conflict rows, never an operator edit', () => {
		const set = takeRepoSet([
			P('in', 'in-step'),
			P('plant', 'plant-ahead'),
			P('repo', 'repo-ahead', { init: 5 }),
			P('both', 'conflict', { init: false }),
			P('noinit', 'repo-ahead', { init: undefined })
		]);
		expect(set).toEqual({ repo: 5, both: false });
	});
	it('drifted drops only the in-step rows', () => {
		expect(drifted([P('a', 'in-step'), P('b', 'plant-ahead'), P('c', 'conflict')]).map((p) => p.name)).toEqual(['b', 'c']);
	});
	it('the badge tells committed from plain plant-ahead and says why a write-back cannot', () => {
		expect(driftBadge(P('a', 'in-step')).kind).toBe('good');
		expect(driftBadge(P('a', 'plant-ahead')).label).toBe('plant ahead');
		expect(driftBadge(P('a', 'plant-ahead', { committed: true })).label).toBe('committed');
		expect(String(driftBadge(P('a', 'plant-ahead', { unwritable: 'init is quoted' })).hint).includes('init is quoted')).toBe(true);
		expect(driftBadge(P('a', 'repo-ahead')).kind).toBe('serious');
		expect(driftBadge(P('a', 'conflict')).label).toBe('conflict');
	});
	it('formats values, ranges and refusals', () => {
		expect(valueText(undefined)).toBe('—');
		expect(valueText(0)).toBe('0');
		expect(valueText(false)).toBe('false');
		expect(valueText('x')).toBe('x');
		expect(rangeText({})).toBe('');
		expect(rangeText({ min: 0 })).toBe('0 – …');
		expect(rangeText({ min: 0, max: 90 })).toBe('0 – 90');
		expect(String(refusedText([{ name: 'A', status: 'change' }])).includes('nothing was written')).toBe(true);
	});
});
