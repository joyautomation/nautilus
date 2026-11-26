// The config screens' review step (#711, slice 3): the unified diff and the
// staged-apply client's calls and refusals.
import { describe, it, expect } from './harness.js';
import { unifiedDiff, diffFiles } from '../src/lib/config/diff.js';
import { ReviewClient, RESTART_NOTE, savedLine } from '../src/lib/config/review.js';

describe('config review: unified diff', () => {
	it('is empty for equal text', () => expect(unifiedDiff('a', 'b', 'x\n', 'x\n')).toBe(''));
	it('matches the shape of naut pull --io --dry-run', () => {
		const d = unifiedDiff('a/n.yaml', 'b/n.yaml', 'one\ntwo\nthree\n', 'one\n2\nthree\n');
		expect(d).toBe('--- a/n.yaml\n+++ b/n.yaml\n@@ -1,3 +1,3 @@\n one\n-two\n+2\n three\n');
	});
	it('keeps distant changes in separate hunks, with line numbers', () => {
		const a = Array.from({ length: 20 }, (_, i) => `l${i}`).join('\n') + '\n';
		const b = a.replace('l1\n', 'L1\n').replace('l18\n', 'L18\n');
		const d = unifiedDiff('a', 'b', a, b);
		expect(d.split('\n').filter((l) => l.startsWith('@@')).length).toBe(2);
		expect(d.includes('@@ -16,5 +16,5 @@')).toBe(true);
	});
	it('merges changes whose contexts touch into one hunk', () => {
		const a = Array.from({ length: 12 }, (_, i) => `l${i}`).join('\n') + '\n';
		const b = a.replace('l2\n', 'L2\n').replace('l7\n', 'L7\n');
		const d = unifiedDiff('a', 'b', a, b);
		expect(d.split('\n').filter((l) => l.startsWith('@@')).length).toBe(1);
		expect(d.includes('@@ -1,11 +1,11 @@')).toBe(true);
	});
	it('shows a new file as all additions', () => {
		expect(unifiedDiff('a', 'b', '', 'x\ny\n')).toBe('--- a\n+++ b\n@@ -0,0 +1,2 @@\n+x\n+y\n');
	});
	it('diffs only the files that changed', () => {
		const r = diffFiles({ 'a.yaml': 'x\n', 'b.yaml': 'y\n' }, { 'a.yaml': 'x\n', 'b.yaml': 'z\n', 'c.yaml': 'new\n' });
		expect(r.map((x) => x.path).join(',')).toBe('b.yaml,c.yaml');
	});
});

describe('config review: the staged-apply client', () => {
	type Call = { url: string; method?: string; body?: string; token?: string };
	const rec = (status: number, body: unknown, calls: Call[] = []) =>
		((async (url: string, init?: RequestInit) => {
			calls.push({ url, method: init?.method, body: init?.body as string, token: (init?.headers as Record<string, string>)?.['X-Nautilus-Token'] });
			return new Response(JSON.stringify(body), { status });
		}) as unknown as typeof fetch);

	it('stages the files, validates, applies with revertIfSilent', async () => {
		const calls: Call[] = [];
		const c = new ReviewClient({ fetch: rec(200, { id: '7', state: 'pending' }, calls), token: 't' });
		await c.stage({ 'nautilus.yaml': 'x' });
		await c.validate('7');
		await c.apply('7', '10s');
		await c.apply('7');
		expect(calls.map((x) => `${x.method} ${x.url}`).join('|')).toBe('POST /api/io/staged|POST /api/io/staged/7/validate|POST /api/io/staged/7/apply|POST /api/io/staged/7/apply');
		expect(calls[0].body).toBe('{"files":{"nautilus.yaml":"x"}}');
		expect(calls[2].body).toBe('{"revertIfSilent":"10s"}');
		expect(calls[3].body).toBe(undefined);
		expect(calls[0].token).toBe('t');
	});
	it('a failed apply answers with the candidate error, not a bare status', async () => {
		const r = await new ReviewClient({ fetch: rec(422, { id: '7', state: 'failed', error: 'rollback worked' }) }).apply('7');
		expect(r.ok).toBe(false);
		if (!r.ok) expect(r.message.includes('rollback worked')).toBe(true);
		if (!r.ok) expect(r.staged?.state).toBe('failed');
	});
	it('refusals name the fix', async () => {
		const stale = await new ReviewClient({ fetch: rec(409, { error: 'candidate 7 is applied' }) }).validate('7');
		if (!stale.ok) expect(stale.message.includes('Validate the candidate again')).toBe(true);
		const bad = await new ReviewClient({ fetch: rec(422, { error: 'unknown driver type' }) }).stage({ a: 'b' });
		if (!bad.ok) expect(bad.message.includes('Fix it in the form')).toBe(true);
		const nope = await new ReviewClient({ fetch: rec(403, { error: 'x' }) }).stage({ a: 'b' });
		if (!nope.ok) expect(nope.kind).toBe('forbidden');
		const down = await new ReviewClient({ fetch: (async () => { throw new Error('boom'); }) as unknown as typeof fetch }).stage({ a: 'b' });
		if (!down.ok) expect(down.kind).toBe('unreachable');
	});
});

describe('config review: commit and patch', () => {
	const make = (status: number, body: string | unknown, calls: { url: string; method?: string; body?: string }[] = []) =>
		((async (url: string, init?: RequestInit) => {
			calls.push({ url, method: init?.method, body: init?.body as string });
			return new Response(typeof body === 'string' ? body : JSON.stringify(body), { status });
		}) as unknown as typeof fetch);
	it('commits the candidate, with the message only when given', async () => {
		const calls: { url: string; method?: string; body?: string }[] = [];
		const c = new ReviewClient({ fetch: make(200, { commit: 'abc', branch: 'main', files: ['nautilus.yaml'] }, calls) });
		const r = await c.commit('7', 'raise setpoint');
		await c.commit('7');
		expect(calls[0].url).toBe('/api/config/commit');
		expect(calls[0].method).toBe('POST');
		expect(calls[0].body).toBe('{"id":"7","message":"raise setpoint"}');
		expect(calls[1].body).toBe('{"id":"7"}');
		expect(r.ok && r.value.branch).toBe('main');
	});
	it('passes the 409 text through, it names the patch route', async () => {
		const r = await new ReviewClient({ fetch: make(409, { error: 'this controller runs from a build; GET /api/config/patch returns the change' }) }).commit('7');
		expect(r.ok).toBe(false);
		if (!r.ok) { expect(r.status).toBe(409); expect(r.message.includes('/api/config/patch')).toBe(true); }
	});
	it('maps sign-in and forbidden like the other steps', async () => {
		const a = await new ReviewClient({ fetch: make(401, { error: 'x' }) }).commit('7');
		if (!a.ok) expect(a.kind).toBe('signin');
		const b = await new ReviewClient({ fetch: make(403, { error: 'x' }) }).patch();
		if (!b.ok) expect(b.kind).toBe('forbidden');
	});
	it('returns the patch text whole', async () => {
		const calls: { url: string; method?: string }[] = [];
		const r = await new ReviewClient({ fetch: make(200, '--- a/x\n+++ b/x\n', calls) }).patch();
		expect(calls[0].url).toBe('/api/config/patch');
		expect(calls[0].method).toBe('GET');
		expect(r.ok && r.patch).toBe('--- a/x\n+++ b/x\n');
	});
	it('nothing to patch is a refusal, not an empty patch', async () => {
		const r = await new ReviewClient({ fetch: make(409, { error: 'nothing to patch: what runs is the project' }) }).patch();
		expect(r.ok).toBe(false);
	});
	it('marks only the no-checkout refusal as having the patch way out', async () => {
		const a = await new ReviewClient({ fetch: make(409, { error: 'this controller runs from a build; GET /api/config/patch returns the change' }) }).commit('7');
		const b = await new ReviewClient({ fetch: make(409, { error: 'candidate 7 is no longer what runs' }) }).commit('7');
		if (!a.ok) expect(a.patch).toBe(true);
		if (!b.ok) expect(b.patch).toBe(false);
	});
	it('a commit for a candidate the controller forgot says to apply again', async () => {
		const r = await new ReviewClient({ fetch: make(404, { error: 'no staged I/O configuration 7' }) }).commit('7');
		expect(r.ok).toBe(false);
		if (!r.ok) expect(r.message.includes('apply the change again')).toBe(true);
	});
	it('5xx and network failures are refusals, an unreadable 200 too', async () => {
		const a = await new ReviewClient({ fetch: make(500, { error: 'git failed' }) }).commit('7');
		expect(a.ok).toBe(false);
		const b = await new ReviewClient({ fetch: (async () => { throw new Error('boom'); }) as unknown as typeof fetch }).commit('7');
		if (!b.ok) expect(b.kind).toBe('unreachable');
		const c = await new ReviewClient({ fetch: make(200, 'not json') }).commit('7');
		expect(c.ok).toBe(false);
	});
});

describe('config review: save for the next restart', () => {
	const make = (status: number, body: string | unknown, calls: { url: string; method?: string; body?: string }[] = []) =>
		((async (url: string, init?: RequestInit) => {
			calls.push({ url, method: init?.method, body: init?.body as string });
			return new Response(typeof body === 'string' ? body : JSON.stringify(body), { status });
		}) as unknown as typeof fetch);
	it('posts the candidate to /api/config/save, with the message only when given', async () => {
		const calls: { url: string; method?: string; body?: string }[] = [];
		const saved = { commit: 'abcdef0123456789', branch: 'main', files: ['nautilus.yaml'], by: 'user:alice', at: '2026-11-23T00:00:00Z', pending: true };
		const c = new ReviewClient({ fetch: make(200, saved, calls) });
		const r = await c.save('3', 'add a device');
		await c.save('3');
		expect(calls[0].url).toBe('/api/config/save');
		expect(calls[0].method).toBe('POST');
		expect(calls[0].body).toBe('{"id":"3","message":"add a device"}');
		expect(calls[1].body).toBe('{"id":"3"}');
		expect(r.ok && r.value.by).toBe('user:alice');
	});
	it('passes the controller refusal through whole', async () => {
		const r = await new ReviewClient({ fetch: make(409, { error: 'candidate 3 is invalid for a reason a restart would not fix: fix what the validation names' }) }).save('3');
		expect(r.ok).toBe(false);
		if (!r.ok) { expect(r.status).toBe(409); expect(r.message.includes('a restart would not fix')).toBe(true); }
	});
	it('says who saved it, which commit, that nothing running changed, and what a restart costs', () => {
		const line = savedLine({ commit: 'abcdef0123456789', branch: 'main', files: ['nautilus.yaml', 'profiles/x.yaml'], by: 'user:alice', at: '', pending: true });
		expect(line.includes('by user:alice')).toBe(true);
		expect(line.includes('commit abcdef012345 on main')).toBe(true);
		expect(line.includes('nautilus.yaml, profiles/x.yaml')).toBe(true);
		expect(line.includes('Nothing running has changed')).toBe(true);
		expect(RESTART_NOTE.includes('scan stops')).toBe(true);
		expect(RESTART_NOTE.includes('restart the standby first, then switch over')).toBe(true);
	});
});
