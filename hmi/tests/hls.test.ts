// The own HLS player (#724 slice 3b): the playlist reader, the codec string,
// which segments to fetch, the buffer rules, the bitrate meter, and the loop
// against a fake fetch and a fake MediaSource.
import { describe, it, expect } from './harness.js';
import { parsePlaylist, codecFromInit, nextSegments, bufferTrim, liveSeek, BitrateMeter, playHls, MAX_SEGMENTS, FALLBACK_CODEC } from '../src/lib/hls.js';

const playlist = (seq: number, n: number, extra = '') =>
	['#EXTM3U', '#EXT-X-VERSION:7', '#EXT-X-TARGETDURATION:2', `#EXT-X-MEDIA-SEQUENCE:${seq}`, '#EXT-X-MAP:URI="init.mp4"', extra]
		.concat(Array.from({ length: n }, (_, i) => `#EXTINF:2.000,\nseg-${seq + i}.m4s`))
		.filter((l) => l !== '')
		.join('\n') + '\n';

describe('parsePlaylist', () => {
	it('reads a good playlist', () => {
		const p = parsePlaylist(playlist(10, 3))!;
		expect(p.mapUri).toBe('init.mp4');
		expect(p.mediaSequence).toBe(10);
		expect(p.targetDuration).toBe(2);
		expect(p.segments).toEqual([
			{ seq: 10, uri: 'seg-10.m4s', duration: 2 },
			{ seq: 11, uri: 'seg-11.m4s', duration: 2 },
			{ seq: 12, uri: 'seg-12.m4s', duration: 2 }
		]);
	});
	it('refuses a playlist with no MAP, one that is not M3U, and unsafe names', () => {
		expect(parsePlaylist('#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:1\n#EXTINF:2,\nseg-1.m4s\n')).toBeNull();
		expect(parsePlaylist('hello\n#EXT-X-MAP:URI="init.mp4"\n')).toBeNull();
		expect(parsePlaylist('#EXTM3U\n#EXT-X-MAP:URI="../init.mp4"\n')).toBeNull();
		const p = parsePlaylist(playlist(1, 0, '') + '#EXTINF:2,\nhttp://x/seg.m4s\n#EXTINF:2,\nseg-2.m4s\n')!;
		expect(p.segments).toEqual([{ seq: 2, uri: 'seg-2.m4s', duration: 2 }]);
	});
	it('ignores unknown tags and comments', () => {
		const p = parsePlaylist(playlist(5, 1, '#EXT-X-FANCY:1\n# a comment'))!;
		expect(p.segments.length).toBe(1);
	});
	it('bounds the segments it reads', () => {
		const p = parsePlaylist(playlist(0, 5000))!;
		expect(p.segments.length).toBe(MAX_SEGMENTS);
	});
});

describe('codecFromInit', () => {
	const box = (tail: number[]) => new Uint8Array([0, 0, 0, 20, 0x66, 0x74, 0x79, 0x70, 0, 0, 0, 30, 0x61, 0x76, 0x63, 0x43, ...tail]);
	it('reads profile, compatibility and level from avcC', () => {
		expect(codecFromInit(box([1, 0x64, 0x00, 0x1f, 0xff]))).toBe('avc1.64001F');
		expect(codecFromInit(box([1, 0x42, 0xe0, 0x1e]))).toBe('avc1.42E01E');
	});
	it('is null when there is no avcC or it is cut short', () => {
		expect(codecFromInit(new Uint8Array([1, 2, 3, 4, 5, 6, 7, 8, 9]))).toBeNull();
		expect(codecFromInit(box([1, 0x64]))).toBeNull();
	});
});

describe('nextSegments', () => {
	const p = (seq: number, n: number) => parsePlaylist(playlist(seq, n))!;
	it('starts at the live edge', () => {
		expect(nextSegments(p(10, 6), null).map((s) => s.seq)).toEqual([14, 15]);
		expect(nextSegments(p(10, 1), null).map((s) => s.seq)).toEqual([10]);
	});
	it('afterwards takes only the newer ones', () => {
		expect(nextSegments(p(10, 6), 13).map((s) => s.seq)).toEqual([14, 15]);
		expect(nextSegments(p(10, 6), 15)).toEqual([]);
	});
	it('jumps to the window start when it fell behind, never beyond the window', () => {
		expect(nextSegments(p(20, 6), 3).map((s) => s.seq)).toEqual([20, 21, 22, 23, 24, 25]);
		expect(nextSegments(p(20, 6), 19).map((s) => s.seq)).toEqual([20, 21, 22, 23, 24, 25]);
	});
	it('has nothing from an empty playlist', () => {
		expect(nextSegments(p(1, 0), null)).toEqual([]);
		expect(nextSegments(p(1, 0), 4)).toEqual([]);
	});
});

describe('bufferTrim / liveSeek', () => {
	it('removes what is older than 20 s behind the playhead', () => {
		expect(bufferTrim(100, [{ start: 50, end: 105 }])).toEqual({ start: 50, end: 80 });
		expect(bufferTrim(100, [{ start: 85, end: 105 }])).toBeNull();
		expect(bufferTrim(100, [])).toBeNull();
		expect(bufferTrim(30, [{ start: 0, end: 40 }], 10)).toEqual({ start: 0, end: 20 });
	});
	it('seeks forward only when far behind or before the buffer', () => {
		expect(liveSeek(10, [{ start: 10, end: 14 }])).toBeNull();
		expect(liveSeek(10, [{ start: 10, end: 30 }])).toBe(27.5);
		expect(liveSeek(0, [{ start: 500, end: 504 }])).toBe(501.5);
		expect(liveSeek(0, [])).toBeNull();
	});
});

describe('BitrateMeter', () => {
	it('measures over the last ~10 s', () => {
		const m = new BitrateMeter();
		expect(m.bitsPerSecond(0)).toBe(0);
		m.add(250_000, 0);
		m.add(250_000, 2000);
		expect(m.bitsPerSecond(4000)).toBe(1_000_000); // 4 Mbit over 4 s
		expect(m.bitsPerSecond(30_000)).toBe(0); // all too old
	});
	it('does not read one fresh segment as a burst', () => {
		const m = new BitrateMeter();
		m.add(100_000, 5000);
		expect(m.bitsPerSecond(5000)).toBe(400_000);
	});
});

// ---- the loop, with fakes ----

class FakeBuf {
	updating = false;
	mode = '';
	appended: Uint8Array[] = [];
	removed: Array<[number, number]> = [];
	buffered = { length: 0, start: (_: number) => 0, end: (_: number) => 0 };
	#l: Record<string, Array<() => void>> = {};
	addEventListener(t: string, f: () => void) {
		(this.#l[t] ??= []).push(f);
	}
	removeEventListener(t: string, f: () => void) {
		this.#l[t] = (this.#l[t] ?? []).filter((x) => x !== f);
	}
	#fire(t: string) {
		queueMicrotask(() => [...(this.#l[t] ?? [])].forEach((f) => f()));
	}
	appendBuffer(d: Uint8Array) {
		this.appended.push(d);
		this.buffered = { length: 1, start: () => 0, end: () => this.appended.length * 2 };
		this.#fire('updateend');
	}
	remove(a: number, b: number) {
		this.removed.push([a, b]);
		this.#fire('updateend');
	}
	abort() {}
}
class FakeMS {
	static last: FakeMS;
	static isTypeSupported = () => true;
	readyState = 'open';
	buf = new FakeBuf();
	types: string[] = [];
	removedBuf = false;
	ended = false;
	constructor() {
		FakeMS.last = this;
	}
	addEventListener() {}
	addSourceBuffer(t: string) {
		this.types.push(t);
		return this.buf;
	}
	removeSourceBuffer() {
		this.removedBuf = true;
	}
	endOfStream() {
		this.ended = true;
	}
}
const init = new Uint8Array([0, 0, 0, 8, 0x61, 0x76, 0x63, 0x43, 1, 0x4d, 0x00, 0x28]);
const flush = async () => {
	for (let i = 0; i < 60; i++) await Promise.resolve();
};

function rig(handler: (url: string) => Response | Error) {
	const asked: string[] = [];
	const signals: AbortSignal[] = [];
	const sleeps: Array<() => void> = [];
	const video: any = { src: '', currentTime: 0, muted: false, buffered: { length: 0, start: () => 0, end: () => 0 }, played: 0, play() { this.played++; return Promise.resolve(); }, removeAttribute() {}, load() {} };
	Object.defineProperty(video, 'buffered', { get: () => FakeMS.last?.buf.buffered ?? { length: 0, start: () => 0, end: () => 0 } });
	const revoked: string[] = [];
	const errors: Array<[string, number]> = [];
	const bitrates: number[] = [];
	const f = async (url: string, init?: RequestInit) => {
		asked.push(url);
		if (init?.signal) signals.push(init.signal);
		const r = handler(url);
		if (r instanceof Error) throw r;
		return r;
	};
	const h = playHls(video, '/api/cameras/c1/hls', {
		fetch: f,
		onError: (m, s) => errors.push([m, s]),
		onBitrate: (b) => bitrates.push(b),
		env: {
			MediaSource: FakeMS,
			native: false,
			sleep: (_ms, signal) => new Promise<void>((res) => { sleeps.push(res); signal.addEventListener('abort', () => res()); }),
			now: () => 0,
			createObjectURL: () => 'blob:fake',
			revokeObjectURL: (u) => revoked.push(u)
		}
	});
	return { h, asked, signals, sleeps, video, revoked, errors, bitrates };
}
const serve = (seq: () => number, n = 3) => (url: string): Response => {
	if (url.endsWith('index.m3u8')) return new Response(playlist(seq(), n));
	if (url.endsWith('init.mp4')) return new Response(init);
	return new Response(new Uint8Array(100));
};

describe('playHls', () => {
	it('appends the init segment first, then each segment once, in order', async () => {
		let seq = 10;
		const r = rig(serve(() => seq));
		await flush();
		const ms = FakeMS.last;
		expect(ms.types).toEqual(['video/mp4; codecs="avc1.4D0028"']);
		expect(ms.buf.mode).toBe('segments');
		expect(ms.buf.appended[0].length).toBe(init.length);
		// live edge: the last two of 10,11,12
		expect(r.asked.filter((u) => u.endsWith('.m4s'))).toEqual(['/api/cameras/c1/hls/seg-11.m4s', '/api/cameras/c1/hls/seg-12.m4s']);
		expect(r.video.played > 0).toBe(true);
		// the window slides: only 13 is new
		seq = 11;
		r.sleeps.shift()!();
		await flush();
		r.sleeps.shift()!();
		await flush();
		expect(r.asked.filter((u) => u.endsWith('.m4s'))).toEqual(['/api/cameras/c1/hls/seg-11.m4s', '/api/cameras/c1/hls/seg-12.m4s', '/api/cameras/c1/hls/seg-13.m4s']);
		expect(r.bitrates.length > 0).toBe(true);
		expect(r.errors).toEqual([]);
		r.h.stop();
	});

	it('a 410 with a JSON error stops it and says why', async () => {
		let polls = 0;
		const r = rig((url) => {
			if (url.endsWith('index.m3u8') && ++polls > 1) return new Response(JSON.stringify({ error: 'the live view reached its limit' }), { status: 410 });
			return serve(() => 1)(url);
		});
		await flush();
		r.sleeps.shift()!();
		await flush();
		expect(r.errors).toEqual([['the live view reached its limit', 410]]);
		expect(FakeMS.last.removedBuf).toBe(true);
		expect(r.revoked).toEqual(['blob:fake']);
		expect(r.sleeps.length).toBe(0);
	});

	it('a camera that never opened is reported at once with the controller words', async () => {
		const r = rig(() => new Response(JSON.stringify({ error: 'the camera did not answer' }), { status: 502 }));
		await flush();
		expect(r.errors).toEqual([['the camera did not answer', 502]]);
	});

	it('retries a few network errors, then gives up', async () => {
		let up = true;
		const r = rig((url) => (url.endsWith('index.m3u8') && !up ? new Error('offline') : serve(() => 1)(url)));
		await flush();
		up = false;
		for (let i = 0; i < 6; i++) {
			r.sleeps.shift()?.();
			await flush();
		}
		expect(r.errors).toEqual([['The live video was interrupted.', 0]]);
	});

	it('stop() aborts, stops polling and is idempotent', async () => {
		const r = rig(serve(() => 1));
		await flush();
		const n = r.asked.length;
		r.h.stop();
		r.h.stop();
		expect(r.signals.every((s) => s.aborted)).toBe(true);
		r.sleeps.forEach((s) => s());
		await flush();
		expect(r.asked.length).toBe(n);
		expect(r.revoked).toEqual(['blob:fake']);
		expect(r.errors).toEqual([]);
	});

	it('says so when the browser has no MediaSource and no native HLS', async () => {
		const errors: Array<[string, number]> = [];
		const v: any = { src: '' };
		playHls(v, '/h', { env: { MediaSource: undefined, native: false }, onError: (m, s) => errors.push([m, s]) });
		await flush();
		expect(errors).toEqual([['This browser cannot play live video from this camera.', 0]]);
	});

	it('prefers Media Source Extensions over native HLS when both exist', async () => {
		const v: any = { src: '', play: () => Promise.resolve() };
		const h = playHls(v, '/api/cameras/c1/hls/', { env: { native: true, MediaSource: class {} as any }, fetch: (async () => new Response('x', { status: 404 })) as any });
		expect(h.native).toBe(false);
		expect(v.src).toBe('');
		h.stop();
	});

	it('hands the playlist to the browser where HLS is native, and measures nothing', async () => {
		const v: any = { src: '', play: () => Promise.resolve() };
		const bit: number[] = [];
		const h = playHls(v, '/api/cameras/c1/hls/', { env: { native: true, MediaSource: undefined }, onBitrate: (b) => bit.push(b) });
		expect(v.src).toBe('/api/cameras/c1/hls/index.m3u8');
		expect(h.native).toBe(true);
		h.stop();
		expect(bit).toEqual([]);
		expect(FALLBACK_CODEC).toBe('avc1.42E01E');
	});
});
