// Light/dark theming: three modes (System / Light / Dark) persisted to
// localStorage, applied as data-theme on <html>. The CSS token layer
// (theme.css) reads [data-theme]. `effective` is reactive so consumers (e.g. a
// code editor, a canvas chart) can follow the resolved theme.
export type Theme = 'system' | 'light' | 'dark';

const KEY = 'theme';

const isTheme = (v: string | null): v is Theme => v === 'system' || v === 'light' || v === 'dark';

function prefersDark(): boolean {
	return globalThis.matchMedia?.('(prefers-color-scheme: dark)').matches ?? true;
}

function resolve(t: Theme): 'light' | 'dark' {
	return t === 'system' ? (prefersDark() ? 'dark' : 'light') : t;
}

function createTheme() {
	// Dark before `init()` runs too, so a pre-paint stamp and the store agree.
	let current = $state<Theme>('dark');
	let effective = $state<'light' | 'dark'>('dark');
	let bound = false;

	function apply(t: Theme) {
		const e = resolve(t);
		effective = e;
		if (typeof document !== 'undefined') document.documentElement.dataset.theme = e;
	}

	function bindSystem() {
		if (bound || typeof globalThis.matchMedia === 'undefined') return;
		bound = true;
		globalThis.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
			if (current === 'system') apply(current);
		});
	}

	return {
		get value() {
			return current;
		},
		get effective() {
			return effective;
		},
		/**
		 * Read the persisted choice and stamp it on `<html>`.
		 *
		 * **The default is dark**, not system. An HMI's design case is an ops
		 * room at 03:00, and a control screen that comes up white because the
		 * workstation happens to be set to a light desktop theme is the wrong
		 * default for the room it lives in. Pass `'system'` to follow the OS
		 * instead (which itself falls back to dark where `matchMedia` cannot
		 * answer), or `'light'` for a daylight panel.
		 *
		 * A saved choice always wins; this is only what a first visit gets.
		 */
		init(fallback: Theme = 'dark') {
			let saved: string | null = null;
			try {
				saved = localStorage.getItem(KEY);
			} catch {
				/* private mode */
			}
			current = isTheme(saved) ? saved : fallback;
			apply(current);
			bindSystem();
		},
		set(t: Theme) {
			current = t;
			try {
				localStorage.setItem(KEY, t);
			} catch {
				/* private mode */
			}
			apply(t);
		}
	};
}

export const theme = createTheme();
