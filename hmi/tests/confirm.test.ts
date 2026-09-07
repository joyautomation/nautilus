// The confirm queue. What makes this worth testing rather than looking at:
// a confirmation that silently never settles is a command an operator believes
// they sent. Every case below is one of those.
import { describe, it, expect } from './harness.js';
import { createConfirmQueue, splitItems, type ConfirmRequest } from '../src/lib/confirm.js';

/** A queue with a host attached, plus a log of what the host was told to show. */
function mounted() {
	const seen: (ConfirmRequest | null)[] = [];
	const q = createConfirmQueue({ onchange: (c) => seen.push(c) });
	const detach = q.attach();
	return { q, seen, detach };
}

describe('confirm queue — the promise flow', () => {
	it('a request puts the question on screen and stays pending', async () => {
		const { q, seen } = mounted();
		let settled: boolean | 'pending' = 'pending';
		const p = q.request({ title: 'Stop P-101?' }).then((v) => (settled = v));

		expect(q.current?.options.title).toBe('Stop P-101?');
		expect(seen.length).toBe(1);
		// Nothing resolves until the operator answers.
		await Promise.resolve();
		expect(settled).toBe('pending');

		q.settle(true);
		await p;
		expect(settled).toBe(true);
	});

	it('confirm resolves true, cancel resolves false', async () => {
		const { q } = mounted();
		const yes = q.request({ title: 'a' });
		q.settle(true);
		expect(await yes).toBe(true);

		const no = q.request({ title: 'b' });
		q.settle(false);
		expect(await no).toBe(false);
	});

	it('clears the screen once answered', async () => {
		const { q, seen } = mounted();
		const p = q.request({ title: 'a' });
		q.settle(false);
		await p;
		expect(q.current).toBeNull();
		expect(seen[seen.length - 1]).toBeNull();
	});

	it('queues a second question behind the first, each with its own answer', async () => {
		const { q } = mounted();
		const first = q.request({ title: 'first' });
		const second = q.request({ title: 'second' });

		// Only the first is visible; the second is invisible until its turn.
		expect(q.current?.options.title).toBe('first');
		expect(q.waiting).toBe(1);

		q.settle(true);
		expect(await first).toBe(true);

		expect(q.current?.options.title).toBe('second');
		expect(q.waiting).toBe(0);
		q.settle(false);
		expect(await second).toBe(false);
		expect(q.current).toBeNull();
	});

	it('a stale click cannot answer somebody else’s question', async () => {
		const { q } = mounted();
		const first = q.request({ title: 'first' });
		const second = q.request({ title: 'second' });
		const staleId = q.current?.id ?? -1;

		q.settle(true);
		expect(await first).toBe(true);

		// The superseded dialog's button fires late, naming the OLD id.
		q.settleById(staleId, true);
		// The second question is untouched and still on screen.
		expect(q.current?.options.title).toBe('second');
		q.settle(false);
		expect(await second).toBe(false);
	});

	it('settling an empty queue is a no-op, not a throw', () => {
		const { q } = mounted();
		q.settle(true);
		q.settleById(999, true);
		expect(q.current).toBeNull();
	});
});

describe('confirm queue — no host mounted', () => {
	it('takes the fallback rather than leaving a promise dangling', async () => {
		const asked: string[] = [];
		const q = createConfirmQueue({
			fallback: (o) => {
				asked.push(o.title);
				return false;
			}
		});
		expect(q.attached).toBe(false);
		expect(await q.request({ title: 'Ack 3 alarms?' })).toBe(false);
		expect(asked).toEqual(['Ack 3 alarms?']);
		// Nothing was queued — the question was already answered.
		expect(q.current).toBeNull();
	});

	it('a fallback returning null queues anyway, for a host about to mount', async () => {
		const q = createConfirmQueue({ fallback: () => null });
		const p = q.request({ title: 'later' });
		expect(q.current?.options.title).toBe('later');
		q.settle(true);
		expect(await p).toBe(true);
	});

	it('the fallback is skipped entirely once a host is attached', async () => {
		let fell = 0;
		const q = createConfirmQueue({
			fallback: () => {
				fell++;
				return true;
			}
		});
		const detach = q.attach();
		expect(q.attached).toBe(true);
		const p = q.request({ title: 'x' });
		q.settle(true);
		expect(await p).toBe(true);
		expect(fell).toBe(0);

		// …and comes back when the host unmounts.
		detach();
		expect(q.attached).toBe(false);
		expect(await q.request({ title: 'y' })).toBe(true);
		expect(fell).toBe(1);
	});
});

describe('splitItems — enumerating what is being acted on', () => {
	it('shows everything under the cap', () => {
		expect(splitItems(['a', 'b'], 8)).toEqual({ shown: ['a', 'b'], more: 0 });
	});
	it('collapses the overflow to a count', () => {
		expect(splitItems(['a', 'b', 'c'], 2)).toEqual({ shown: ['a', 'b'], more: 1 });
	});
	it('handles nothing to list', () => {
		expect(splitItems(undefined)).toEqual({ shown: [], more: 0 });
		expect(splitItems([])).toEqual({ shown: [], more: 0 });
	});
	it('a cap of zero or less means no cap, not an empty list', () => {
		expect(splitItems(['a', 'b'], 0)).toEqual({ shown: ['a', 'b'], more: 0 });
	});
});
