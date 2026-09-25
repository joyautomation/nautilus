// The diagram editors' clipboard: the system clipboard where the webview
// allows it (so a copy in one editor pastes in another panel of the same
// kind), falling back to this webview's memory when it doesn't (a headless
// harness, a denied permission, an older runtime).
//
// The system payload is a JSON envelope under a nautilus-specific web
// custom format, plus the same JSON as text/plain (so a runtime without
// custom formats still round-trips it, and a paste into a text editor
// shows something legible instead of nothing). Reading accepts either;
// anything that isn't a nautilus envelope of the wanted kind is ignored.

export type ClipKind = 'fbd' | 'ld' | 'sfc' | 'mimic';

type Envelope = { nautilus: ClipKind; v: 1; data: unknown };

const MIME = 'web application/x-nautilus+json';
const memory = new Map<ClipKind, unknown>();

// Clipboard promises can hang on a permission prompt the webview never
// shows; a paste must never wait on one.
const TIMEOUT_MS = 400;
function timeout<T>(p: Promise<T>): Promise<T> {
	return Promise.race([
		p,
		new Promise<T>((_, reject) => setTimeout(() => reject(new Error('clipboard timeout')), TIMEOUT_MS))
	]);
}

function parse(kind: ClipKind, text: string | undefined): unknown {
	if (!text) return undefined;
	try {
		const env = JSON.parse(text) as Envelope;
		return env && env.nautilus === kind && env.v === 1 ? env.data : undefined;
	} catch {
		return undefined;
	}
}

/** Store a copy: memory synchronously, the system clipboard best-effort. */
export function writeClip(kind: ClipKind, data: unknown): void {
	const plain = JSON.parse(JSON.stringify(data)) as unknown; // no proxies
	memory.set(kind, plain);
	const text = JSON.stringify({ nautilus: kind, v: 1, data: plain } satisfies Envelope);
	const cb = typeof navigator !== 'undefined' ? navigator.clipboard : undefined;
	if (!cb) return;
	const plainOnly = () => cb.writeText?.(text).catch(() => undefined);
	try {
		if (typeof ClipboardItem !== 'undefined' && cb.write) {
			const item = new ClipboardItem({
				'text/plain': new Blob([text], { type: 'text/plain' }),
				[MIME]: new Blob([text], { type: MIME })
			});
			void timeout(cb.write([item])).catch(plainOnly);
		} else void plainOnly();
	} catch {
		void plainOnly();
	}
}

/** The payload to paste: the system clipboard's nautilus envelope of this
 * kind when there is one, else the last copy made in this webview. */
export async function readClip<T>(kind: ClipKind): Promise<T | undefined> {
	const cb = typeof navigator !== 'undefined' ? navigator.clipboard : undefined;
	if (cb) {
		try {
			if (cb.read) {
				const items = await timeout(cb.read());
				for (const item of items) {
					for (const type of [MIME, 'text/plain']) {
						if (!item.types.includes(type)) continue;
						const got = parse(kind, await (await item.getType(type)).text());
						if (got !== undefined) return got as T;
					}
				}
			} else if (cb.readText) {
				const got = parse(kind, await timeout(cb.readText()));
				if (got !== undefined) return got as T;
			}
		} catch {
			/* no permission / no custom formats — memory below */
		}
	}
	return memory.get(kind) as T | undefined;
}

/** Whether this webview holds a copy (drives the paste buttons' enabled
 * state; the system clipboard may hold one too, but can't be probed
 * without a read). */
export function hasMemoryClip(kind: ClipKind): boolean {
	return memory.has(kind);
}

/** True while the user is typing somewhere — an input, textarea, select,
 * or contenteditable (the float editor is an input/textarea) — where Ctrl+C/X/V/A and Del are
 * the field's, never the canvas's. */
export function typingTarget(ev?: Event): boolean {
	const els = [document.activeElement, ev?.target as Element | null];
	for (const el of els) {
		if (!el || !(el instanceof Element)) continue;
		const tag = el.tagName;
		if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return true;
		if ((el as HTMLElement).isContentEditable) return true;
	}
	return false;
}
