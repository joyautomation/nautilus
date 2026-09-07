// The component catalog — a storybook for a plant, kept inside the plant.
//
// An HMI kit's components are visual: their real test is looking at them, in
// every state the process can put them in. A catalog route is how a team sees
// that set at once — and, more usefully in commissioning, how an engineer
// answers "what does a dead point look like?" without waiting for the process
// to produce one.
//
// The registry is the APP's, not the kit's: every deployment has its own
// symbols, its own equipment families, its own house glyphs, and the kit has
// no business enumerating them. So the kit ships the *shape* — `Story`,
// `Variant`, `StoryGroup` — plus the two screens that render any registry of
// that shape (`CatalogIndex`, `CatalogEntry`) and the pure helpers those
// screens need. An app writes one `stories.ts` and two `+page.svelte` files.
//
// One rule the shape enforces: a variant renders from STATIC PROPS ALONE.
// A catalog that needs a live subscription to draw a card is a catalog that is
// blank whenever the runtime is down — exactly when someone is looking at it.
// Components that read their data internally (a tag path, a store) cannot honour
// that, so `Story.component` is OPTIONAL: a story with no component and a
// `note` renders as a LIVE-ONLY card. It stays in the index, named and grouped,
// saying why it has no preview — which is information — instead of appearing as
// a broken box or vanishing from the list.

import type { Component } from 'svelte';

/** One prop set for a story's component — a single card on the detail page. */
export interface Variant {
	/** Heading on the card. Unique within a story; used as the `{#each}` key. */
	name: string;
	/** Spread onto the component. Must render with no live data behind it. */
	props: Record<string, unknown>;
	/** Why this state matters, or what to look at. Rendered under the preview. */
	note?: string;
}

/**
 * One component, and every state worth seeing it in.
 *
 * `component` is optional on purpose — see the live-only note at the top of
 * this file. A story with no `component` (or no `variants`) is rendered as a
 * note card carrying `note`.
 */
export interface Story {
	/** URL segment. Unique across the whole registry, not merely within a group. */
	slug: string;
	title: string;
	/** One or two sentences: what it is and what its visual encoding means. */
	blurb: string;
	/** Omit for a live-only entry. */
	component?: Component<any>;
	variants?: Variant[];
	/**
	 * Shown in place of a preview when there is nothing previewable — the
	 * reason, in the operator's terms ("takes a tag path; renders from the
	 * live subscription").
	 */
	note?: string;
	/** Extra search terms beyond title/slug/blurb. */
	tags?: string[];
}

/**
 * A named section of the index. The grouping axis is the app's to choose —
 * for a plant it is usually the PROCESS SYSTEM (wells, boosters, reservoirs,
 * treatment…) rather than the component taxonomy, because that is the axis an
 * engineer is thinking on when they come looking.
 */
export interface StoryGroup {
	/** Stable id — anchor target on the index page. */
	id: string;
	title: string;
	/** Optional line under the group heading. */
	blurb?: string;
	stories: Story[];
}

/** A story that has something to draw. The negation is a live-only entry. */
export function isPreviewable(story: Story): boolean {
	return !!story.component && (story.variants?.length ?? 0) > 0;
}

/** The variant a card thumbnail shows: the first one, if there is one. */
export function previewVariant(story: Story): Variant | undefined {
	return isPreviewable(story) ? story.variants![0] : undefined;
}

/** Total stories across every group — the index subtitle's number. */
export function storyCount(groups: StoryGroup[]): number {
	return groups.reduce((n, g) => n + g.stories.length, 0);
}

/** Every story, flattened, in registry order. */
export function allStories(groups: StoryGroup[]): Story[] {
	return groups.flatMap((g) => g.stories);
}

/**
 * Resolve a URL slug, and report which group it was in — the detail page shows
 * the group as an eyebrow, so it needs both. `null` for an unknown slug (a
 * stale bookmark), which the caller renders as a not-found rather than a crash.
 */
export function findStory(
	groups: StoryGroup[],
	slug: string
): { story: Story; group: StoryGroup } | null {
	for (const group of groups) {
		const story = group.stories.find((s) => s.slug === slug);
		if (story) return { story, group };
	}
	return null;
}

/** The previous/next story in registry order, for detail-page paging. */
export function neighbors(
	groups: StoryGroup[],
	slug: string
): { prev?: Story; next?: Story } {
	const flat = allStories(groups);
	const i = flat.findIndex((s) => s.slug === slug);
	if (i < 0) return {};
	return { prev: flat[i - 1], next: flat[i + 1] };
}

/**
 * Search over titles, slugs, blurbs, tags and group titles. Every whitespace-
 * separated term must match somewhere (AND, not OR) — typing more words
 * narrows, which is what a search box is expected to do. Groups left with no
 * matching story are dropped rather than shown empty.
 */
export function filterGroups(groups: StoryGroup[], query: string): StoryGroup[] {
	const terms = query.toLowerCase().split(/\s+/).filter(Boolean);
	if (terms.length === 0) return groups;
	const out: StoryGroup[] = [];
	for (const group of groups) {
		const stories = group.stories.filter((s) => {
			const hay = [s.title, s.slug, s.blurb, group.title, ...(s.tags ?? [])]
				.join(' ')
				.toLowerCase();
			return terms.every((t) => hay.includes(t));
		});
		if (stories.length > 0) out.push({ ...group, stories });
	}
	return out;
}

/** Options for {@link formatProps}. */
export interface FormatPropsOptions {
	/** Arrays longer than this collapse to `[…N items]`. Default 6. */
	maxArray?: number;
	/** Strings longer than this are truncated with an ellipsis. Default 120. */
	maxString?: number;
	/** Indent width. Default 2. */
	indent?: number;
}

/**
 * The `<details> props` readout.
 *
 * Story props are not JSON: they carry event handlers, snippets, Svelte
 * components and 500-point trend arrays. `JSON.stringify` on those either
 * throws (circular component internals) or dumps a screenful of numbers that
 * hides the three props that mattered. So functions become `ƒ()`, components
 * become `<Component>`, long arrays and strings are elided, and anything that
 * still refuses to serialise degrades to its type name rather than taking the
 * page down — the readout is documentation, and documentation must not be the
 * thing that breaks a screen.
 */
export function formatProps(
	props: Record<string, unknown>,
	options: FormatPropsOptions = {}
): string {
	const { maxArray = 6, maxString = 120, indent = 2 } = options;
	const pad = ' '.repeat(Math.max(0, indent));
	// Ancestors, not "every object seen": a value referenced twice as a SIBLING
	// is not a cycle, and reporting it as one would lie about the props.
	const stack: object[] = [];

	function scalar(v: unknown): string | null {
		if (v === null) return 'null';
		switch (typeof v) {
			case 'undefined':
				return 'undefined';
			case 'boolean':
				return String(v);
			case 'number':
				return Number.isFinite(v) ? String(v) : `"${String(v)}"`;
			case 'bigint':
				return `"${v}n"`;
			case 'function':
				return '"ƒ()"';
			case 'symbol':
				return JSON.stringify(String(v));
			case 'string':
				return JSON.stringify(v.length > maxString ? `${v.slice(0, maxString)}…` : v);
			default:
				return null;
		}
	}

	function render(v: unknown, depth: number): string {
		const s = scalar(v);
		if (s !== null) return s;
		const obj = v as Record<string, unknown>;
		if (stack.includes(obj)) return '"[circular]"';
		if (typeof (obj as { toJSON?: unknown }).toJSON === 'function') {
			return render((obj as { toJSON(): unknown }).toJSON(), depth);
		}
		const here = pad.repeat(depth + 1);
		const close = pad.repeat(depth);
		stack.push(obj);
		try {
			if (Array.isArray(obj)) {
				if (obj.length > maxArray) return `"[…${obj.length} items]"`;
				if (obj.length === 0) return '[]';
				const items = obj.map((item) => `${here}${render(item, depth + 1)}`);
				return `[\n${items.join(',\n')}\n${close}]`;
			}
			// `undefined` entries are dropped, as `JSON.stringify` drops them: a
			// prop that was never passed should not read as one that was passed empty.
			const entries = Object.entries(obj).filter(([, val]) => val !== undefined);
			if (entries.length === 0) return '{}';
			const lines = entries.map(
				([k, val]) => `${here}${JSON.stringify(k)}: ${render(val, depth + 1)}`
			);
			return `{\n${lines.join(',\n')}\n${close}}`;
		} finally {
			stack.pop();
		}
	}

	try {
		return render(props, 0);
	} catch {
		// A getter that throws, an exotic proxy: the readout is documentation and
		// must never be the thing that takes a screen down.
		return '{ /* props are not serialisable */ }';
	}
}
