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
	return p;
}

/**
 * The paper look's colours (docs/design/spatial-hmi.md §3f), laid over a
 * theme palette: the equipment goes near-white and the lines dark, labels
 * become white cards with dark ink, and the backdrop is paper — while the
 * priority colours, the selection and the hover come through untouched,
 * so an alarm reads exactly as it does in the lit scene. Steel, fluid and
 * running all land on the same quiet greys on purpose: in this look the
 * only colour is an alarm, and flow is shown by the chevrons moving.
 */
export function paperPalette(base: Palette = DEFAULT_PALETTE): Palette {
	return {
		...base,
		steel: '#f7f7f5',
		steelDark: '#e3e3df',
		shell: '#fbfbfa',
		fluid: '#dfe6ee',
		running: '#f7f7f5',
		stale: '#d9d9d5',
		selected: '#2b2b28',
		handle: '#8a8a86',
		ink: '#45453f',
		paper: '#f4f3ef',
		label: { bg: '#ffffff', ink: '#2b2b28', border: '#d6d6d1' },
		priority: { ...base.priority }
	};
}
