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
	let current = $state<Theme>('system');
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
		init() {
			let saved: string | null = null;
			try {
				saved = localStorage.getItem(KEY);
			} catch {
				/* private mode */
			}
			current = isTheme(saved) ? saved : 'system';
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
