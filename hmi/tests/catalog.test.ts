// The catalog's pure logic. Two things here can silently produce a wrong
// screen rather than an error: the props readout (which must survive the
// unserialisable values a story's props are FULL of — components, handlers,
// 500-point arrays) and `isPreviewable` (which decides whether a card draws a
// component or a live-only note; get it wrong and the index shows a broken box
// where an explanation belonged).
import { describe, it, expect } from './harness.js';
import {
	allStories,
	filterGroups,
	findStory,
	formatProps,
	isPreviewable,
	neighbors,
	previewVariant,
	storyCount,
	type Story,
	type StoryGroup
} from '../src/lib/catalog.js';

// A stand-in component: the type wants a Svelte component, and the helpers only
// ever check it for presence.
const C = (() => {}) as unknown as Story['component'];

const story = (slug: string, extra: Partial<Story> = {}): Story => ({
	slug,
	title: slug,
	blurb: '',
	component: C,
	variants: [{ name: 'default', props: {} }],
	...extra
});

const groups: StoryGroup[] = [
	{
		id: 'wells',
		title: 'Wells',
		stories: [
			story('well-tank', { title: 'Well tank', blurb: 'Groundwater source vessel' }),
			story('well-pump', { title: 'Well pump', tags: ['motor', 'vfd'] })
		]
	},
	{
		id: 'boosters',
		title: 'Boosters',
		stories: [
			story('booster-row', { title: 'Booster row' }),
			// live-only: no component, no variants
			{ slug: 'zone-box', title: 'Zone box', blurb: 'Zone summary', note: 'reads the runtime' }
		]
	}
];

describe('isPreviewable / previewVariant', () => {
	it('a story with a component and at least one variant previews', () => {
		expect(isPreviewable(story('a'))).toBe(true);
		expect(previewVariant(story('a'))?.name).toBe('default');
	});

	it('no component means live-only, however many variants are declared', () => {
		const s: Story = { slug: 'x', title: 'X', blurb: '', variants: [{ name: 'v', props: {} }] };
		expect(isPreviewable(s)).toBe(false);
		expect(previewVariant(s)).toBe(undefined);
	});

	it('a component with an EMPTY variant list is live-only, not a blank card', () => {
		expect(isPreviewable(story('y', { variants: [] }))).toBe(false);
		expect(isPreviewable(story('z', { variants: undefined }))).toBe(false);
	});

	it('the thumbnail is the FIRST variant — registry order is the author’s choice', () => {
		const s = story('m', {
			variants: [
				{ name: 'running', props: { running: true } },
				{ name: 'stopped', props: { running: false } }
			]
		});
		expect(previewVariant(s)?.props).toEqual({ running: true });
	});
});

describe('findStory / neighbors / counts', () => {
	it('resolves a slug and reports which group it was in', () => {
		const hit = findStory(groups, 'booster-row');
		expect(hit?.story.title).toBe('Booster row');
		expect(hit?.group.id).toBe('boosters');
	});

	it('an unknown slug is null, not a throw — a stale bookmark is a not-found', () => {
		expect(findStory(groups, 'nope')).toBeNull();
	});

	it('finds live-only entries too: they are catalogued, only not previewable', () => {
		expect(findStory(groups, 'zone-box')?.story.note).toBe('reads the runtime');
	});

	it('counts and flattens across groups in registry order', () => {
		expect(storyCount(groups)).toBe(4);
		expect(allStories(groups).map((s) => s.slug)).toEqual([
			'well-tank',
			'well-pump',
			'booster-row',
			'zone-box'
		]);
	});

	it('paging crosses a group boundary — the index is one sequence', () => {
		const n = neighbors(groups, 'well-pump');
		expect(n.prev?.slug).toBe('well-tank');
		expect(n.next?.slug).toBe('booster-row');
	});

	it('ends have one neighbour, an unknown slug has none', () => {
		expect(neighbors(groups, 'well-tank').prev).toBe(undefined);
		expect(neighbors(groups, 'zone-box').next).toBe(undefined);
		expect(neighbors(groups, 'nope')).toEqual({});
	});
});

describe('filterGroups', () => {
	it('an empty query returns the registry unchanged', () => {
		expect(filterGroups(groups, '')).toEqual(groups);
		expect(filterGroups(groups, '   ')).toEqual(groups);
	});

	it('matches title, slug, blurb and tags, case-insensitively', () => {
		expect(allStories(filterGroups(groups, 'PUMP')).map((s) => s.slug)).toEqual(['well-pump']);
		expect(allStories(filterGroups(groups, 'groundwater')).map((s) => s.slug)).toEqual([
			'well-tank'
		]);
		expect(allStories(filterGroups(groups, 'vfd')).map((s) => s.slug)).toEqual(['well-pump']);
	});

	it('a group title matches its stories — searching a process system finds it', () => {
		expect(allStories(filterGroups(groups, 'boosters')).map((s) => s.slug)).toEqual([
			'booster-row',
			'zone-box'
		]);
	});

	it('multiple terms narrow (AND), they do not widen', () => {
		expect(allStories(filterGroups(groups, 'well pump')).map((s) => s.slug)).toEqual(['well-pump']);
		expect(filterGroups(groups, 'well booster')).toEqual([]);
	});

	it('empty groups are dropped, not shown with a bare heading', () => {
		expect(filterGroups(groups, 'tank').map((g) => g.id)).toEqual(['wells']);
	});

	it('does not mutate the registry it filters', () => {
		filterGroups(groups, 'tank');
		expect(groups[0].stories.length).toBe(2);
	});
});

describe('formatProps', () => {
	it('renders plain values as JSON does', () => {
		expect(formatProps({ a: 1, b: 'x', c: true, d: null })).toBe(
			'{\n  "a": 1,\n  "b": "x",\n  "c": true,\n  "d": null\n}'
		);
	});

	it('a function becomes ƒ() — a story is full of handlers', () => {
		expect(formatProps({ onclick: () => {} })).toBe('{\n  "onclick": "ƒ()"\n}');
	});

	it('a component prop is a function, so it reads as ƒ() rather than exploding', () => {
		expect(formatProps({ symbol: C })).toBe('{\n  "symbol": "ƒ()"\n}');
	});

	it('a long array elides to its length: 500 trend points are not documentation', () => {
		const points = Array.from({ length: 500 }, (_, i) => i);
		expect(formatProps({ points })).toBe('{\n  "points": "[…500 items]"\n}');
	});

	it('a short array is shown in full, and the threshold is configurable', () => {
		expect(formatProps({ v: [1, 2] })).toBe('{\n  "v": [\n    1,\n    2\n  ]\n}');
		expect(formatProps({ v: [1, 2] }, { maxArray: 1 })).toBe('{\n  "v": "[…2 items]"\n}');
	});

	it('a long string truncates', () => {
		const out = formatProps({ d: 'x'.repeat(200) });
		expect(out.includes('…')).toBe(true);
		expect(out.length < 160).toBe(true);
	});

	it('nested objects and arrays keep their structure', () => {
		expect(formatProps({ pen: { name: 'PV', color: 'var(--s1)' } })).toBe(
			'{\n  "pen": {\n    "name": "PV",\n    "color": "var(--s1)"\n  }\n}'
		);
	});

	it('undefined props are dropped, as JSON drops them', () => {
		expect(formatProps({ a: 1, b: undefined })).toBe('{\n  "a": 1\n}');
	});

	it('an empty object or array stays on one line', () => {
		expect(formatProps({})).toBe('{}');
		expect(formatProps({ a: {}, b: [] })).toBe('{\n  "a": {},\n  "b": []\n}');
	});

	it('a cycle is named, not followed — the readout must not hang the page', () => {
		const a: Record<string, unknown> = { name: 'a' };
		a.self = a;
		expect(formatProps({ a })).toBe('{\n  "a": {\n    "name": "a",\n    "self": "[circular]"\n  }\n}');
	});

	it('a SHARED sibling reference is not a cycle and is rendered twice', () => {
		const shared = { u: 'gpm' };
		expect(formatProps({ x: shared, y: shared })).toBe(
			'{\n  "x": {\n    "u": "gpm"\n  },\n  "y": {\n    "u": "gpm"\n  }\n}'
		);
	});

	it('NaN and Infinity are shown as themselves, not as null', () => {
		expect(formatProps({ a: NaN, b: Infinity })).toBe('{\n  "a": "NaN",\n  "b": "Infinity"\n}');
	});

	it('a throwing getter degrades to a comment instead of taking the screen down', () => {
		const bad = {
			get boom() {
				throw new Error('nope');
			}
		};
		expect(formatProps({ bad })).toBe('{ /* props are not serialisable */ }');
	});
});
