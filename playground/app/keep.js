// What the playground keeps for a visitor, all of it in their own browser.
//
// A project comes from a source that is ours: an example, a ?project= URL,
// or a share link (the whole project, compressed, in the link's #). The
// source is never changed. The first edit makes the visitor's own copy,
// kept in IndexedDB under the source's key with the fingerprint of the
// source it started from, so a later visit brings their copy back and can
// tell when the source has changed since.

const DB = 'nautilus-playground';
const STORE = 'projects';

let opened;
function db() {
	opened ??= new Promise((ok, no) => {
		const r = indexedDB.open(DB, 1);
		r.onupgradeneeded = () => r.result.createObjectStore(STORE, { keyPath: 'key' });
		r.onsuccess = () => ok(r.result);
		r.onerror = () => no(r.error);
	});
	return opened;
}

async function tx(mode, fn) {
	const d = await db();
	return new Promise((ok, no) => {
		const t = d.transaction(STORE, mode);
		const req = fn(t.objectStore(STORE));
		t.oncomplete = () => ok(req.result);
		t.onerror = t.onabort = () => no(t.error);
	});
}

/**
 * The visitor's copy for this source: {key, base, files, updated}, or
 * undefined. Storage that is blocked or unavailable (some private windows)
 * reads as nothing kept.
 */
export function mine(key) {
	return tx('readonly', (s) => s.get(key)).catch(() => undefined);
}
export function keep(copy) {
	return tx('readwrite', (s) => s.put(copy));
}
export function forget(key) {
	return tx('readwrite', (s) => s.delete(key)).catch(() => {});
}

/** A short, stable fingerprint of a project's files (order does not matter). */
export async function fingerprint(files) {
	const text = JSON.stringify(Object.keys(files).sort().map((k) => [k, files[k]]));
	const h = new Uint8Array(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(text)));
	return [...h.slice(0, 8)].map((b) => b.toString(16).padStart(2, '0')).join('');
}

export function sameFiles(a, b) {
	const ka = Object.keys(a);
	return ka.length === Object.keys(b).length && ka.every((k) => k in b && a[k] === b[k]);
}

// ---- share links: {title, open, hmi, files, from?, base?, removed?} as gzip, then
// base64url. A project made from one of our examples carries only what
// differs from it (from: the example's name), so a link to a changed
// lift-station is a few KB, not 35.

async function pipe(bytes, transform) {
	return new Uint8Array(await new Response(new Blob([bytes]).stream().pipeThrough(transform)).arrayBuffer());
}

export async function encodeProject(payload) {
	const gz = await pipe(new TextEncoder().encode(JSON.stringify(payload)), new CompressionStream('gzip'));
	let bin = '';
	for (let i = 0; i < gz.length; i += 0x8000) bin += String.fromCharCode(...gz.subarray(i, i + 0x8000));
	return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

/**
 * What to put in a share link for files, made from example (null: not from
 * one). base is the example's fingerprint, so a link opened after the
 * example has changed can say so.
 */
export function sharePayload({ title, open, hmi }, files, example, base) {
	if (!example) return { title, open, hmi, files };
	const changed = {};
	for (const [k, t] of Object.entries(files)) if (example.files[k] !== t) changed[k] = t;
	const removed = Object.keys(example.files).filter((k) => !(k in files));
	return { title, open, from: example.name, base, files: changed, ...(removed.length ? { removed } : {}) };
}

/**
 * A share link's project, checked: {title, open, hmi, files, from?, base?, removed?}.
 * With from, files are the changes to that example (loadSource merges them).
 */
export async function decodeProject(code) {
	let p;
	try {
		const bin = atob(code.replace(/-/g, '+').replace(/_/g, '/'));
		const gz = Uint8Array.from(bin, (c) => c.charCodeAt(0));
		p = JSON.parse(new TextDecoder().decode(await pipe(gz, new DecompressionStream('gzip'))));
	} catch {
		throw new Error('this share link is damaged or incomplete (was all of it copied?)');
	}
	const from = typeof p?.from === 'string' && /^[\w-]+$/.test(p.from) ? p.from : undefined;
	const files = p && typeof p.files === 'object' && p.files ? p.files : null;
	if (!files || (!from && !('nautilus.yaml' in files)) || Object.values(files).some((t) => typeof t !== 'string')) {
		throw new Error('this share link does not hold a nautilus project');
	}
	return {
		title: typeof p.title === 'string' ? p.title : 'shared project',
		open: Array.isArray(p.open) ? p.open.filter((f) => typeof f === 'string') : undefined,
		hmi: typeof p.hmi === 'string' && /^[\w-]+$/.test(p.hmi) ? p.hmi : undefined,
		from,
		base: typeof p.base === 'string' && /^[0-9a-f]{16}$/.test(p.base) ? p.base : undefined,
		removed: Array.isArray(p.removed) ? p.removed.filter((f) => typeof f === 'string') : [],
		files
	};
}
