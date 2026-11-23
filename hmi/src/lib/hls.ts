// A small live-HLS player for the built-in cameras (#724 slice 3b).
//
// There is no hls.js here on purpose: the kit's built form is embedded in the
// playground's wasm, so every npm dependency grows it. The controller serves
// a narrow, known shape (one fragmented-MP4 H.264 playlist with a sliding
// window of ~2 s segments), which a small player can follow with Media Source
// Extensions where a browser has them; a browser with only native HLS (iPhone) gets the playlist URL.
//
// The logic is pure and tested (tests/hls.test.ts); `playHls` is the thin
// DOM-facing loop, with everything it touches injectable through `env`.

/** The most segments read from one playlist. */
export const MAX_SEGMENTS = 64;
export const FALLBACK_CODEC = 'avc1.42E01E';

export interface HlsSegment {
	seq: number;
	uri: string;
	duration: number;
}

export interface HlsPlaylist {
	mapUri: string;
	mediaSequence: number;
	targetDuration: number;
	segments: HlsSegment[];
}

// File names only: the controller serves relative names in the playlist's own
// directory, so anything with a path, a scheme or `..` is refused.
const NAME = /^[A-Za-z0-9._-]+$/;
const safeName = (s: string) => NAME.test(s) && s !== '.' && s !== '..';

/** Read a media playlist; null when it is not `#EXTM3U`, has no `EXT-X-MAP`, or names something unsafe. */
export function parsePlaylist(text: string): HlsPlaylist | null {
	if (typeof text !== 'string') return null;
	const lines = text.split(/\r?\n/).map((l) => l.trim());
	if (lines[0] !== '#EXTM3U') return null;
	let mapUri = '';
	let mediaSequence = 0;
	let targetDuration = 0;
	const segments: HlsSegment[] = [];
	let dur: number | null = null;
	for (const l of lines) {
		if (!l) continue;
		if (l.startsWith('#')) {
			if (l.startsWith('#EXT-X-MAP:')) {
				const m = /URI="([^"]*)"/.exec(l);
				if (!m || !safeName(m[1])) return null;
				mapUri = m[1];
			} else if (l.startsWith('#EXT-X-MEDIA-SEQUENCE:')) {
				const n = parseInt(l.slice(22), 10);
				if (isFinite(n) && n >= 0) mediaSequence = n;
			} else if (l.startsWith('#EXT-X-TARGETDURATION:')) {
				const n = parseFloat(l.slice(22));
				if (isFinite(n) && n > 0) targetDuration = n;
			} else if (l.startsWith('#EXTINF:')) {
				const n = parseFloat(l.slice(8));
				dur = isFinite(n) && n >= 0 ? n : 0;
			}
			continue; // unknown tags and comments are ignored
		}
		// a URI line
		if (segments.length >= MAX_SEGMENTS) break;
		if (dur !== null && safeName(l)) {
			segments.push({ seq: mediaSequence + segments.length, uri: l, duration: dur });
		} else if (dur !== null) {
			// an unsafe name keeps the numbering of the ones after it
			segments.push({ seq: mediaSequence + segments.length, uri: '', duration: dur });
		}
		dur = null;
	}
	if (!mapUri) return null;
	return { mapUri, mediaSequence, targetDuration, segments: segments.filter((s) => s.uri !== '') };
}

/** `avc1.PPCCLL` from the `avcC` box of an init segment; null when there is none. */
export function codecFromInit(init: Uint8Array): string | null {
	for (let i = 0; i + 8 <= init.length; i++) {
		if (init[i] === 0x61 && init[i + 1] === 0x76 && init[i + 2] === 0x63 && init[i + 3] === 0x43) {
			// 'avcC', then configurationVersion, profile, compatibility, level
			if (i + 8 > init.length) return null;
			const hex = (b: number) => b.toString(16).toUpperCase().padStart(2, '0');
			return `avc1.${hex(init[i + 5])}${hex(init[i + 6])}${hex(init[i + 7])}`;
		}
	}
	return null;
}

/**
 * Which segments to fetch now: at the start the last `liveEdgeBack`, after
 * that every one newer than the last appended; fallen behind the window, the
 * window's start. Never more than the window holds.
 */
export function nextSegments(p: Pick<HlsPlaylist, 'segments'>, lastAppendedSeq: number | null, liveEdgeBack = 2): HlsSegment[] {
	const segs = p.segments;
	if (segs.length === 0) return [];
	if (lastAppendedSeq === null) return segs.slice(-Math.max(1, liveEdgeBack));
	if (lastAppendedSeq < segs[0].seq - 1) return segs.slice();
	return segs.filter((s) => s.seq > lastAppendedSeq);
}

export interface Range {
	start: number;
	end: number;
}

/** The part of the buffer to remove: everything older than `keepSeconds` behind the playhead; null when none. */
export function bufferTrim(currentTime: number, buffered: Range[], keepSeconds = 20): Range | null {
	if (buffered.length === 0) return null;
	const cut = currentTime - keepSeconds;
	const start = buffered[0].start;
	if (!(cut > start)) return null;
	return { start, end: cut };
}

/** Where to seek to stay near the live edge; null when the playhead is fine. */
export function liveSeek(currentTime: number, buffered: Range[], maxBehind = 8, target = 2.5): number | null {
	if (buffered.length === 0) return null;
	const last = buffered[buffered.length - 1];
	if (currentTime < last.start || last.end - currentTime > maxBehind) return Math.max(last.start, last.end - target);
	return null;
}

/** Bits per second over the last ~10 s of what was received. */
export class BitrateMeter {
	#samples: Array<{ t: number; bytes: number }> = [];
	readonly windowMs: number;
	constructor(windowMs = 10_000) {
		this.windowMs = windowMs;
	}
	add(bytes: number, nowMs: number): void {
		this.#samples.push({ t: nowMs, bytes });
		this.#prune(nowMs);
	}
	#prune(nowMs: number) {
		while (this.#samples.length > 0 && nowMs - this.#samples[0].t > this.windowMs) this.#samples.shift();
	}
	bitsPerSecond(nowMs: number): number {
		this.#prune(nowMs);
		if (this.#samples.length === 0) return 0;
		const total = this.#samples.reduce((a, s) => a + s.bytes, 0);
		// at least 2 s, so one fresh segment is not read as a burst
		const span = Math.max(2000, Math.min(this.windowMs, nowMs - this.#samples[0].t));
		return (total * 8 * 1000) / span;
	}
}

// ---- the player ----

export interface HlsOptions {
	fetch?: (input: string, init?: RequestInit) => Promise<Response>;
	onBitrate?: (bps: number) => void;
	onError?: (message: string, status: number) => void;
	signal?: AbortSignal;
	/** Everything the browser provides, injectable for tests. */
	env?: {
		MediaSource?: any;
		/** Whether the video element plays HLS natively (Safari, iOS). */
		native?: boolean;
		sleep?: (ms: number, signal: AbortSignal) => Promise<void>;
		now?: () => number;
		createObjectURL?: (o: unknown) => string;
		revokeObjectURL?: (u: string) => void;
	};
}

export interface HlsHandle {
	stop(): void;
	/** The browser plays the stream itself, so no bitrate is measured. */
	native: boolean;
}

export const INTERRUPTED = 'The live video was interrupted.';
export const UNSUPPORTED = 'This browser cannot play live video from this camera.';
const UNREADABLE = 'The live video could not be read.';
const RETRIES = 3;

class PlayError extends Error {
	constructor(
		message: string,
		readonly status: number
	) {
		super(message);
	}
}

const defaultSleep = (ms: number, signal: AbortSignal) =>
	new Promise<void>((resolve) => {
		if (signal.aborted) return resolve();
		const t = setTimeout(done, ms);
		function done() {
			clearTimeout(t);
			signal.removeEventListener('abort', done);
			resolve();
		}
		signal.addEventListener('abort', done);
	});

const rangesOf = (b: { length: number; start(i: number): number; end(i: number): number }): Range[] => {
	const out: Range[] = [];
	for (let i = 0; i < b.length; i++) out.push({ start: b.start(i), end: b.end(i) });
	return out;
};

/**
 * Play the camera's live HLS stream in `video`. `baseUrl` is the directory the
 * playlist lives in (`.../cameras/{id}/hls`). Returns a handle; `stop()` ends
 * everything and is safe to call twice.
 */
export function playHls(video: HTMLVideoElement, baseUrl: string, opts: HlsOptions = {}): HlsHandle {
	const env = opts.env ?? {};
	const base = baseUrl.replace(/\/+$/, '');
	const playlistUrl = `${base}/index.m3u8`;
	const native = env.native ?? (typeof video.canPlayType === 'function' && video.canPlayType('application/vnd.apple.mpegurl') !== '');
	const MS = 'MediaSource' in env ? env.MediaSource : (globalThis as any).MediaSource;
	const doFetch = opts.fetch ?? ((u: string, i?: RequestInit) => globalThis.fetch(u, i));
	const sleep = env.sleep ?? defaultSleep;
	const now = env.now ?? (() => Date.now());
	const ctl = new AbortController();
	let stopped = false;
	let failedOnce = false;
	let sb: any = null;
	let ms: any = null;
	let objectUrl = '';

	const fail = (message: string, status: number) => {
		if (stopped || failedOnce) return;
		failedOnce = true;
		stop();
		opts.onError?.(message, status);
	};

	function stop() {
		if (stopped) return;
		stopped = true;
		ctl.abort();
		try {
			if (ms && ms.readyState === 'open') {
				if (sb) {
					if (sb.updating) sb.abort();
					ms.removeSourceBuffer(sb);
				}
				ms.endOfStream();
			}
		} catch {
			// the element may already have detached it
		}
		sb = null;
		ms = null;
		if (objectUrl) {
			try {
				(env.revokeObjectURL ?? ((u: string) => URL.revokeObjectURL(u)))(objectUrl);
			} catch {
				// nothing to revoke
			}
			objectUrl = '';
		}
		try {
			video.removeAttribute?.('src');
			video.load?.();
		} catch {
			// a fake or detached element
		}
	}
	opts.signal?.addEventListener('abort', stop);
	if (opts.signal?.aborted) stop();

	// Media Source Extensions where there are any (it measures the bitrate and does the
	// same thing everywhere: Chrome's own native HLS is newer and refuses what the
	// controller's segments are, a Safari without MSE (iPhone) is where native is needed)
	if (native && !MS) {
		video.src = playlistUrl;
		void video.play?.()?.catch?.(() => {});
		return { stop, native: true };
	}
	if (!MS) {
		queueMicrotask(() => fail(UNSUPPORTED, 0));
		return { stop, native: false };
	}

	async function get(url: string): Promise<Response> {
		let res: Response;
		try {
			res = await doFetch(url, { signal: ctl.signal, credentials: 'same-origin', cache: 'no-store' });
		} catch (e) {
			if (stopped) throw e;
			throw new PlayError(INTERRUPTED, 0);
		}
		if (!res.ok) {
			let message = '';
			try {
				const j: any = JSON.parse(await res.text());
				if (j && typeof j.error === 'string') message = j.error;
			} catch {
				// not JSON
			}
			throw new PlayError(message || `The camera did not answer (${res.status}).`, res.status);
		}
		return res;
	}

	const append = (data: Uint8Array) =>
		new Promise<void>((resolve, reject) => {
			const s = sb;
			if (!s) return reject(new Error('stopped'));
			const ok = () => {
				cleanup();
				resolve();
			};
			const bad = () => {
				cleanup();
				reject(new PlayError(INTERRUPTED, 0));
			};
			const cleanup = () => {
				s.removeEventListener('updateend', ok);
				s.removeEventListener('error', bad);
			};
			s.addEventListener('updateend', ok);
			s.addEventListener('error', bad);
			try {
				s.appendBuffer(data);
			} catch {
				cleanup();
				reject(new PlayError(INTERRUPTED, 0));
			}
		});

	const remove = (r: Range) =>
		new Promise<void>((resolve) => {
			const s = sb;
			if (!s) return resolve();
			const done = () => {
				s.removeEventListener('updateend', done);
				resolve();
			};
			s.addEventListener('updateend', done);
			try {
				s.remove(r.start, r.end);
			} catch {
				done();
			}
		});

	async function run() {
		ms = new MS();
		objectUrl = (env.createObjectURL ?? ((o: unknown) => URL.createObjectURL(o as MediaSource)))(ms);
		video.src = objectUrl;
		await new Promise<void>((resolve) => {
			if (ms.readyState === 'open') return resolve();
			ms.addEventListener('sourceopen', () => resolve(), { once: true });
		});
		if (stopped) return;

		let playlist = await readPlaylist();
		const init = new Uint8Array(await (await get(`${base}/${playlist.mapUri}`)).arrayBuffer());
		const meter = new BitrateMeter();
		meter.add(init.length, now());
		const codec = codecFromInit(init) ?? FALLBACK_CODEC;
		const type = `video/mp4; codecs="${codec}"`;
		if (typeof MS.isTypeSupported === 'function' && !MS.isTypeSupported(type)) throw new PlayError(UNSUPPORTED, 0);
		sb = ms.addSourceBuffer(type);
		sb.mode = 'segments';
		await append(init);

		let last: number | null = null;
		let started = false;
		let errors = 0;
		for (;;) {
			if (stopped) return;
			for (const seg of nextSegments(playlist, last)) {
				if (stopped) return;
				let retry = false;
				try {
					const data = new Uint8Array(await (await get(`${base}/${seg.uri}`)).arrayBuffer());
					meter.add(data.length, now());
					await append(data);
					errors = 0;
				} catch (e) {
					if (stopped) return;
					// a segment that left the window is skipped, the next poll catches up
					if (e instanceof PlayError && e.status === 0 && ++errors <= RETRIES) retry = true;
					else if (!(e instanceof PlayError && e.status === 404)) throw e;
				}
				if (retry) break; // a network blip: poll again, the segment is asked for once more
				last = seg.seq;
				if (stopped) return;
				const r = rangesOf(video.buffered);
				const seek = liveSeek(video.currentTime, r);
				if (seek !== null) video.currentTime = seek;
				const trim = bufferTrim(video.currentTime, rangesOf(sb.buffered));
				if (trim) await remove(trim);
				if (!started) {
					started = true;
					try {
						video.muted = true;
						void video.play?.()?.catch?.(() => {});
					} catch {
						// autoplay may be blocked: the controls stay
					}
				}
			}
			if (stopped) return;
			opts.onBitrate?.(meter.bitsPerSecond(now()));
			await sleep(Math.max(500, Math.min(1000, (playlist.targetDuration || 2) * 500)), ctl.signal);
			if (stopped) return;
			try {
				playlist = await readPlaylist();
				errors = 0;
			} catch (e) {
				if (stopped) return;
				if (e instanceof PlayError && e.status === 0 && ++errors <= RETRIES) continue;
				throw e;
			}
		}
	}

	async function readPlaylist(): Promise<HlsPlaylist> {
		const p = parsePlaylist(await (await get(playlistUrl)).text());
		if (!p) throw new PlayError(UNREADABLE, 0);
		return p;
	}

	run().catch((e) => {
		if (stopped) return;
		if (e instanceof PlayError) fail(e.message, e.status);
		else fail(INTERRUPTED, 0);
	});
	return { stop, native: false };
}
