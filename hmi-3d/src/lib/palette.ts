// Colours. ISA-101 restraint: equipment is grey when normal; colour means
// abnormal (an alarm's priority) or a live state worth noticing (running,
// flowing). Bad quality is desaturated and translucent, so a stale model
// never looks like a healthy one.
//
// The UI-role colours come from the kit's theme tokens, read off the DOM
// once at mount, so the 3D view follows [data-theme] like every 2D
// component. Material greys are literal: they are paint, not UI.

export interface Palette {
	steel: string;
	steelDark: string;
	shell: string;
	fluid: string;
	running: string;
	stale: string;
	selected: string;
	/** The node under the pointer: the one a click would pick. */
	hover: string;
	handle: string;
	/** The drawn line: an inked edge in the paper look, a flow chevron. */
	ink: string;
	/** What the paper look paints behind everything. */
	paper: string;
	/** The bands that travel along a flowing pipe (its dashes), over the
	 * fluid colour. */
	flow?: string;
	/** The floor grid's lines (cell, section). */
	grid?: { cell: string; section: string };
	/** The theme is a dark one (read off --bg): the paper look inverts. */
	dark?: boolean;
	label: { bg: string; ink: string; border: string };
	/** Alarm priority -> colour, keyed by the kit's Priority names. */
	priority: Record<string, string>;
}

/** The dark theme's values, used before the DOM is available and as fallbacks. */
export const DEFAULT_PALETTE: Palette = {
	steel: '#8a8d91',
	steelDark: '#55585c',
	shell: '#b8bcc2',
	fluid: '#4a7fb5',
	flow: '#9cc8f2',
	running: '#5aa469',
	stale: '#6b6b6b',
	selected: '#e8e6e1',
	hover: '#6aa5e8',
	handle: '#c9a227',
	ink: '#3a3a38',
	paper: '#f4f3ef',
	label: { bg: '#1a1a19', ink: '#e8e6e1', border: '#383835' },
	priority: {
		critical: '#d03b3b',
		high: '#ec835a',
		medium: '#fab219',
		low: '#c3c2b7',
		diagnostic: '#898781'
	}
};

/** Token -> palette slot; the same roles PRIORITY_META in the kit uses. */
const TOKENS: Record<string, string> = {
	'--crit': 'critical',
	'--serious': 'high',
	'--warn': 'medium',
	'--ink-2': 'low',
	'--muted': 'diagnostic'
};

/**
 * Read the theme's tokens off an element. Anything the theme does not set
 * keeps its default. three.js needs real colour values (a `var()` is not a
 * colour), which is why this is a one-time read and not a CSS reference.
 */
export function paletteFromTheme(el: Element): Palette {
	const cs = getComputedStyle(el);
	const get = (name: string) => cs.getPropertyValue(name).trim();
	const p: Palette = { ...DEFAULT_PALETTE, label: { ...DEFAULT_PALETTE.label }, priority: { ...DEFAULT_PALETTE.priority } };
	for (const [token, prio] of Object.entries(TOKENS)) {
		const v = get(token);
		if (v) p.priority[prio] = v;
	}
	const good = get('--good');
	if (good) p.running = good;
	const ink = get('--ink');
	if (ink) {
		p.selected = ink;
		p.label.ink = ink;
	}
	const accent = get('--accent');
	if (accent) p.hover = accent;
	const surface = get('--surface');
	if (surface) p.label.bg = surface;
	const axis = get('--axis');
	if (axis) p.label.border = axis;
	const bg = get('--bg');
	p.dark = bg ? luminance(bg) < 0.4 : true;
	return p;
}

/**
 * The paper look's colours (docs/design/spatial-hmi.md §3f), laid over a
 * theme palette: the equipment goes near-white and the lines dark, labels
 * are dark lettering haloed in paper, and the backdrop is paper — while the
 * priority colours, the selection and the hover come through untouched,
 * so an alarm reads exactly as it does in the lit scene. Steel, shell and
 * fluid land on quiet near-whites: the drawing carries the form, and flow
 * is shown by the chevrons moving. `running` keeps its colour (James,
 * 2026-10-22: "tint should still apply to paper") — a tint drive naming
 * it, or a built-in pump that is running, reads as a state on the page
 * the way an alarm does.
 */
export function paperPalette(base: Palette = DEFAULT_PALETTE, dark = base.dark ?? false): Palette {
	if (dark) {
		// The same drawing inverted for a dark theme: a near-black sheet,
		// dark fills, light lines; flow a lighter blue to show on it.
		return {
			...base,
			steel: '#2a2b2e',
			steelDark: '#202124',
			shell: '#303135',
			fluid: '#2b3a4b',
			stale: '#3a3a3a',
			selected: '#e8e6e1',
			handle: '#8a8a86',
			ink: '#c9c8c1',
			paper: '#141517',
			flow: '#86aedb',
			grid: { cell: '#202124', section: '#2b2c30' },
			dark: true,
			label: { bg: '#141517', ink: '#e8e6e1', border: '#38383b' },
			priority: { ...base.priority }
		};
	}
	return {
		...base,
		steel: '#f7f7f5',
		steelDark: '#e3e3df',
		shell: '#fbfbfa',
		fluid: '#dfe6ee',
		stale: '#d9d9d5',
		selected: '#2b2b28',
		handle: '#8a8a86',
		ink: '#45453f',
		paper: '#f4f3ef',
		flow: '#5f86b3',
		grid: { cell: '#e6e6e2', section: '#d2d2cd' },
		dark: false,
		label: { bg: '#f4f3ef', ink: '#2b2b28', border: '#d6d6d1' },
		priority: { ...base.priority }
	};
}

/** Relative luminance (0-1) of a CSS colour in #rgb, #rrggbb or rgb() form;
 * 1 for anything it cannot read. */
function luminance(css: string): number {
	let r = 255, g = 255, b = 255;
	const hex = css.match(/^#([0-9a-f]{3}|[0-9a-f]{6})$/i)?.[1];
	if (hex) {
		const full = hex.length === 3 ? [...hex].map((c) => c + c).join('') : hex;
		[r, g, b] = [0, 2, 4].map((i) => parseInt(full.slice(i, i + 2), 16));
	} else {
		const m = css.match(/rgba?\(\s*([\d.]+)[ ,]+([\d.]+)[ ,]+([\d.]+)/i);
		if (!m) return 1;
		[r, g, b] = [m[1], m[2], m[3]].map(Number);
	}
	const lin = (c: number) => {
		const s = c / 255;
		return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
	};
	return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
}
