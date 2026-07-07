// Reduced-motion preference: three modes (System / Full / Reduced) persisted to
// localStorage, applied as data-motion on <html>. `reduced` is reactive so
// components with JS/SMIL motion (e.g. the pump status lamp) can honor it; the
// bulk of CSS animation/transition is killed by a rule in theme.css.
export type Motion = 'system' | 'full' | 'reduced';

const KEY = 'motion';

const isMotion = (v: string | null): v is Motion =>
	v === 'system' || v === 'full' || v === 'reduced';

function prefersReduced(): boolean {
	return globalThis.matchMedia?.('(prefers-reduced-motion: reduce)').matches ?? false;
}

function resolve(m: Motion): boolean {
	return m === 'reduced' || (m === 'system' && prefersReduced());
}

function createMotion() {
	let current = $state<Motion>('system');
	let reduced = $state(false);
	let bound = false;

	function apply(m: Motion) {
		const r = resolve(m);
		reduced = r;
		if (typeof document !== 'undefined')
			document.documentElement.dataset.motion = r ? 'reduced' : 'full';
	}

	function bindSystem() {
		if (bound || typeof globalThis.matchMedia === 'undefined') return;
		bound = true;
		globalThis.matchMedia('(prefers-reduced-motion: reduce)').addEventListener('change', () => {
			if (current === 'system') apply(current);
		});
	}

	return {
		get value() {
			return current;
		},
		get reduced() {
			return reduced;
		},
		init() {
			let saved: string | null = null;
			try {
				saved = localStorage.getItem(KEY);
			} catch {
				/* private mode */
			}
			current = isMotion(saved) ? saved : 'system';
			apply(current);
			bindSystem();
		},
		set(m: Motion) {
			current = m;
			try {
				localStorage.setItem(KEY, m);
			} catch {
				/* private mode */
			}
			apply(m);
		}
	};
}

export const motion = createMotion();
