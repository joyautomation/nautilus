// Opt-in clip recorder: with GESTURE_CLIPS=<dir> set, every test records a
// video of its own run — for demonstrating a gesture and for reviewing a
// failure without re-running it. Unset, recordClips() registers nothing and
// the harness behaves (and times) exactly as before.
//
// How: each Browser the test launches gets a CDP screencast
// (Page.startScreencast); its JPEG frames are held in memory with their
// arrival time. When the test ends they are resampled onto a fixed 25 fps
// timeline (a screencast only emits a frame when something repaints; still
// stretches over 1 s are shortened to 1 s) and piped to ffmpeg →
// <dir>/<suite>/<NN>-<slug>.mp4 (libx264, yuv420p).
// <dir>/index.md and <dir>/index.html list every clip with PASS/FAIL, and
// <dir>/manifest.json describes the same for machines (schema 1, set
// "gestures" — the docs site's proof pages read it; contract in
// randd/handoffs/TEST-PLAN-PROOF.md): run {sha, date, runId, host} from
// GITHUB_SHA / GITHUB_RUN_ID (else `git rev-parse HEAD`, "local") and one
// item per test {kind "gesture", id "<suite>/<NN-slug>", suite, test,
// verdict pass|fail, clip "<suite>/<NN-slug>.mp4" | null, durationS}.
// Zero npm deps; needs `ffmpeg` on PATH only when recording.

import { before, after, beforeEach, afterEach } from 'node:test';
import { spawn, execSync } from 'node:child_process';
import { mkdirSync, rmSync, writeFileSync, readFileSync, readdirSync, renameSync, existsSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { hostname } from 'node:os';
import { fileURLToPath } from 'node:url';
import { Browser } from './cdp.mjs';

const FPS = 25;
const TAIL_MS = 600; // hold the final state on screen a little
// A still stretch longer than this (Editor.open's mount wait, a settle
// sleep) plays for this long instead — clips show the gestures, not the
// waiting. Everything that moves keeps its real timing.
const MAX_HOLD_MS = 1000;

const slugify = (name) =>
	name
		.toLowerCase()
		.replace(/[^a-z0-9]+/g, '-')
		.replace(/^-+|-+$/g, '')
		.slice(0, 60)
		.replace(/-+$/, '');

const withTimeout = (p, ms) => Promise.race([p, new Promise((r) => setTimeout(r, ms))]);

/** Call once at the top of a test file: `recordClips('gestures')`. */
export function recordClips(suite) {
	const root = process.env.GESTURE_CLIPS;
	if (!root) return;
	const dir = join(root, suite);
	let n = 0;
	let frames = null; // [{ t, data: Buffer }] for the running test
	const results = [];
	const started = new Date().toISOString().replace(/\.\d+Z$/, 'Z');

	Browser.recorder = {
		async attach(b) {
			if (!frames) return;
			const sink = frames;
			b.on('Page.screencastFrame', ({ data, sessionId }) => {
				sink.push({ t: Date.now(), data: Buffer.from(data, 'base64') });
				b.send('Page.screencastFrameAck', { sessionId }).catch(() => {});
			});
			await b.send('Page.startScreencast', { format: 'jpeg', quality: 80 });
		},
		async detach(b) {
			if (!frames) return;
			const sink = frames;
			// A final still: a test whose last step repainted nothing still
			// ends on what the page shows at close.
			await withTimeout(
				(async () => {
					try {
						const { data } = await b.send('Page.captureScreenshot', { format: 'jpeg', quality: 80 });
						sink.push({ t: Date.now(), data: Buffer.from(data, 'base64') });
						await b.send('Page.stopScreencast');
					} catch {
						/* page already gone */
					}
				})(),
				2000
			);
		}
	};

	before(() => {
		try {
			execSync('ffmpeg -version', { stdio: 'ignore' });
		} catch {
			throw new Error('GESTURE_CLIPS is set but ffmpeg is not on PATH');
		}
		rmSync(dir, { recursive: true, force: true });
		mkdirSync(dir, { recursive: true });
	});

	beforeEach(() => {
		frames = [];
	});

	afterEach(async (t) => {
		const got = frames;
		frames = null;
		const id = `${String(++n).padStart(2, '0')}-${slugify(t.name)}`;
		const file = `${id}.mp4`;
		let clip = null;
		let durationS = null;
		try {
			if (got.length) {
				durationS = await encode(got, join(dir, file));
				clip = file;
			}
		} catch (e) {
			// A broken clip must not turn a passing test red.
			console.error(`[clips] ${file}: ${e.message}`);
		}
		results.push({ n, id, name: t.name, passed: t.passed, clip, durationS });
	});

	after(() => {
		writeJson(join(dir, 'results.json'), { suite, started, results });
		writeIndex(root);
	});
}

function encode(raw, out) {
	const step = 1000 / FPS;
	// Clip timeline: real arrival times with long still gaps shortened.
	const frames = [];
	for (const f of raw) {
		const prev = frames[frames.length - 1];
		const t = prev ? prev.t + Math.min(f.t - prev.raw, MAX_HOLD_MS) : 0;
		frames.push({ t, raw: f.t, data: f.data });
	}
	const t0 = 0;
	const end = frames[frames.length - 1].t + TAIL_MS;
	const ticks = Math.floor((end - t0) / step) + 1;
	const ff = spawn(
		'ffmpeg',
		[
			'-y', '-loglevel', 'error',
			'-f', 'image2pipe', '-c:v', 'mjpeg', '-framerate', String(FPS), '-i', '-',
			'-vf', 'scale=trunc(iw/2)*2:trunc(ih/2)*2',
			'-c:v', 'libx264', '-preset', 'veryfast', '-crf', '28', '-pix_fmt', 'yuv420p',
			'-movflags', '+faststart', out
		],
		{ stdio: ['pipe', 'ignore', 'pipe'] }
	);
	let err = '';
	ff.stderr.on('data', (d) => (err = (err + d).slice(-2000)));
	const done = new Promise((res, rej) => {
		ff.on('error', rej);
		ff.on('close', (code) =>
			code === 0 ? res(Math.round((ticks / FPS) * 10) / 10) : rej(new Error(`ffmpeg exited ${code}: ${err.trim()}`))
		);
	});
	ff.stdin.on('error', () => {}); // surfaced through `done`
	(async () => {
		// Each output tick shows the latest frame that had arrived by then.
		let i = 0;
		for (let tick = t0; tick <= end; tick += step) {
			while (i + 1 < frames.length && frames[i + 1].t <= tick) i++;
			if (!ff.stdin.write(frames[i].data)) await new Promise((r) => ff.stdin.once('drain', r));
		}
		ff.stdin.end();
	})();
	return done;
}

function writeJson(path, value) {
	writeFileSync(path + '.tmp', JSON.stringify(value, null, '\t'));
	renameSync(path + '.tmp', path);
}

const esc = (s) => s.replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c]);

/** Rebuild <root>/index.md, index.html and manifest.json from every suite's
 * results.json — each test file runs in its own process, so whichever
 * finishes last writes the complete index. */
function writeIndex(root) {
	const suites = readdirSync(root, { withFileTypes: true })
		.filter((d) => d.isDirectory() && existsSync(join(root, d.name, 'results.json')))
		.map((d) => JSON.parse(readFileSync(join(root, d.name, 'results.json'), 'utf8')))
		.sort((a, b) => a.suite.localeCompare(b.suite));
	const verdict = (r) => (r.passed ? 'PASS' : 'FAIL');
	const md = ['# Gesture harness clips', ''];
	const html = [
		'<!doctype html><meta charset="utf-8"><title>Gesture harness clips</title>',
		'<style>body{font:14px system-ui,sans-serif;margin:16px;background:#fff;color:#111}',
		'section{margin:0 0 24px}video{width:100%;max-width:720px;display:block;border:1px solid #ccc}',
		'.FAIL{color:#b00;font-weight:600}.PASS{color:#070}</style>',
		'<h1>Gesture harness clips</h1>'
	];
	for (const s of suites) {
		const failed = s.results.filter((r) => !r.passed).length;
		const summary = `${s.results.length - failed} passed, ${failed} failed`;
		md.push(`## ${s.suite} (${summary})`, '', '| # | test | result | clip |', '|---|------|--------|------|');
		html.push(`<h2>${esc(s.suite)} (${summary})</h2>`);
		for (const r of s.results) {
			const nn = String(r.n).padStart(2, '0');
			const href = r.clip ? `${s.suite}/${r.clip}` : null;
			md.push(`| ${nn} | ${r.name.replace(/\|/g, '\\|')} | ${verdict(r)} | ${href ? `[${r.clip}](${href})` : '—'} |`);
			html.push(
				`<section><h3>${nn} <span class="${verdict(r)}">${verdict(r)}</span> ${esc(r.name)}</h3>` +
					(href ? `<video controls preload="none" src="${esc(href)}"></video>` : '<p>no clip</p>') +
					'</section>'
			);
		}
		md.push('');
	}
	writeFileSync(join(root, 'index.md'), md.join('\n'));
	writeFileSync(join(root, 'index.html'), html.join('\n'));
	writeManifest(root, suites);
}

function gitSha() {
	try {
		return execSync('git rev-parse HEAD', { cwd: dirname(fileURLToPath(import.meta.url)), stdio: ['ignore', 'pipe', 'ignore'] })
			.toString()
			.trim();
	} catch {
		return null;
	}
}

/** <root>/manifest.json: schema 1, set "gestures" (see the header). */
function writeManifest(root, suites) {
	const starts = suites.map((s) => s.started).filter(Boolean).sort();
	const items = [];
	for (const s of suites) {
		for (const r of s.results) {
			const id = r.id ?? `${String(r.n).padStart(2, '0')}-${slugify(r.name)}`;
			items.push({
				kind: 'gesture',
				id: `${s.suite}/${id}`,
				suite: s.suite,
				test: r.name,
				verdict: r.passed ? 'pass' : 'fail',
				clip: r.clip ? `${s.suite}/${r.clip}` : null,
				durationS: r.durationS ?? null
			});
		}
	}
	writeJson(join(root, 'manifest.json'), {
		schema: 1,
		set: 'gestures',
		run: {
			sha: process.env.GITHUB_SHA || gitSha(),
			date: starts[0] ?? new Date().toISOString().replace(/\.\d+Z$/, 'Z'),
			runId: process.env.GITHUB_RUN_ID || 'local',
			host: hostname().split('.')[0]
		},
		items
	});
}
