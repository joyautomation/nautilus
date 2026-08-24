// Unit tests for the delta-frame merge (src/lib/delta.ts).
//
//	npm test
//
// ./harness.ts is a ~60-line stand-in for vitest, so the kit can test its
// pure logic without acquiring a test runner. The API is a strict subset of
// vitest's: if the kit ever adopts it, this file changes one import line.
import { describe, expect, it } from './harness.js';
import { emptyDelta, mergeDelta, type DeltaState } from '../src/lib/delta.js';

type F = { ts: number; tags: Record<string, unknown>; seq?: number; full?: boolean };

const frame = (seq: number | undefined, tags: Record<string, unknown>, full?: boolean): F => {
	const f: F = { ts: 1, tags };
	if (seq !== undefined) f.seq = seq;
	if (full) f.full = true;
	return f;
};

describe('mergeDelta', () => {
	it('passes a frame with no seq straight through', () => {
		// The compatibility rule that makes asking for deltas safe against
		// any controller: an older one ignores ?delta=1 and sends whole
		// frames, and those must reach the consumer untouched.
		const st = emptyDelta();
		const f = frame(undefined, { A: 1 });
		const r = mergeDelta(st, f);
		expect(r.frame).toBe(f); // same object, not a copy
		expect(r.gap).toBe(false);
		expect(st.tags).toBeNull(); // no state accumulated
	});

	it('takes the first frame as the whole state', () => {
		const st = emptyDelta();
		const r = mergeDelta(st, frame(1, { A: 1, B: 2 }, true));
		expect(r.full).toBe(true);
		expect(r.frame!.tags).toEqual({ A: 1, B: 2 });
		expect(st.seq).toBe(1);
	});

	it('merges a delta over the accumulated state', () => {
		const st = emptyDelta();
		mergeDelta(st, frame(1, { A: 1, B: 2, C: 3 }, true));
		const r = mergeDelta(st, frame(2, { B: 20 }));
		expect(r.full).toBe(false);
		// B updated; A and C UNCHANGED, not dropped — a tag absent from a
		// delta is one that did not move.
		expect(r.frame!.tags).toEqual({ A: 1, B: 20, C: 3 });
	});

	it('treats an empty delta as "nothing moved"', () => {
		const st = emptyDelta();
		mergeDelta(st, frame(1, { A: 1 }, true));
		const r = mergeDelta(st, frame(2, {}));
		expect(r.frame!.tags).toEqual({ A: 1 });
	});

	it('replaces, not merges, on a full frame — so a deletion lands', () => {
		// The tag set changing is the one difference a delta cannot carry,
		// so the controller resyncs. If this merged instead of replacing, a
		// deleted tag would live on the screen forever.
		const st = emptyDelta();
		mergeDelta(st, frame(1, { A: 1, Gone: 9 }, true));
		const r = mergeDelta(st, frame(2, { A: 1, Added: 5 }, true));
		expect(r.full).toBe(true);
		expect(r.frame!.tags).toEqual({ A: 1, Added: 5 });
		expect('Gone' in r.frame!.tags).toBe(false);
	});

	it('reports a gap and publishes nothing when seq skips', () => {
		const st = emptyDelta();
		mergeDelta(st, frame(1, { A: 1 }, true));
		mergeDelta(st, frame(2, { A: 2 }));
		const r = mergeDelta(st, frame(4, { A: 4 }));
		expect(r.gap).toBe(true);
		expect(r.frame).toBeNull();
		// The skipped frame must NOT have been applied: the caller is
		// reconnecting, and a half-applied state is worse than none.
		expect(st.tags).toEqual({ A: 2 });
		expect(st.seq).toBe(2);
	});

	it('accepts any seq on the first frame of a connection', () => {
		// A reconnect restarts the controller's counter at 1, but a client
		// that re-created its state must not treat a first frame as a gap
		// whatever number it carries.
		const st = emptyDelta();
		const r = mergeDelta(st, frame(7, { A: 1 }, true));
		expect(r.gap).toBe(false);
		expect(st.seq).toBe(7);
	});

	it('publishes a fresh tags object every frame', () => {
		// Required, not defensive: the caller assigns the frame to Svelte
		// $state, which caches its proxy against the source object's
		// identity. Republishing one mutated object would freeze every
		// bound value on the screen at whatever it read first.
		const st = emptyDelta();
		const a = mergeDelta(st, frame(1, { A: 1 }, true)).frame!;
		const b = mergeDelta(st, frame(2, { A: 2 })).frame!;
		expect(a.tags).not.toBe(b.tags);
		expect(a.tags).not.toBe(st.tags);
		// ...and the published frame is not aliased by later merges.
		expect(a.tags.A).toBe(1);
		expect(b.tags.A).toBe(2);
	});

	it('carries the non-tag payload through untouched', () => {
		const st = emptyDelta();
		mergeDelta(st, { ts: 1, tags: { A: 1 }, seq: 1, full: true });
		const r = mergeDelta(st, {
			ts: 2,
			tags: {},
			seq: 2,
			quality: { A: 'stale' },
			scan: { count: 9 }
		} as unknown as F);
		const out = r.frame as unknown as Record<string, unknown>;
		expect(out.ts).toBe(2);
		expect(out.quality).toEqual({ A: 'stale' });
		expect(out.scan).toEqual({ count: 9 });
	});

	it('survives a long run of deltas without drifting', () => {
		const st: DeltaState = emptyDelta();
		const truth: Record<string, number> = {};
		for (let i = 0; i < 200; i++) truth[`T${i}`] = 0;
		mergeDelta(st, frame(1, { ...truth }, true));
		for (let n = 2; n <= 500; n++) {
			const d: Record<string, number> = {};
			// A handful of tags move each tick, as a real plant does.
			for (let k = 0; k < 7; k++) {
				const name = `T${(n * 13 + k * 29) % 200}`;
				truth[name] = n * 100 + k;
				d[name] = truth[name];
			}
			const r = mergeDelta(st, frame(n, d));
			expect(r.gap).toBe(false);
		}
		expect(st.tags).toEqual(truth);
	});
});
