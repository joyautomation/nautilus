<script lang="ts">
	// The catalog index: every story in the registry as a card carrying a LIVE
	// preview of its first variant, sectioned by group.
	//
	// The card shows the component itself, not a screenshot — a screenshot goes
	// stale the moment someone changes a fill, and a catalog that lies about the
	// current look is worse than none. The cost is that a preview must render
	// from static props, which is exactly the discipline `Story` asks for; a
	// story that cannot (`isPreviewable` false) shows a note card saying so.
	//
	// See ../catalog.ts for the registry shape.
	import type { Snippet } from 'svelte';
	import {
		filterGroups,
		isPreviewable,
		previewVariant,
		storyCount,
		type Story,
		type StoryGroup
	} from '../catalog.js';

	let {
		groups,
		title = 'Component catalog',
		blurb = '',
		basePath = '/components',
		href,
		search = false,
		query = $bindable(''),
		searchPlaceholder = 'Filter components…',
		minColumn = 260,
		previewHeight = 190,
		showGroupHeadings = true,
		preview,
		empty
	}: {
		groups: StoryGroup[];
		/** Page heading. Empty string renders no heading. */
		title?: string;
		/** Line under the heading. The story count is appended automatically. */
		blurb?: string;
		/** Detail-route prefix; a card links to `${basePath}/${slug}`. */
		basePath?: string;
		/** Full control over a card's link, if `basePath` is not enough. */
		href?: (story: Story, group: StoryGroup) => string;
		/** Show the filter box. Off by default — a short registry does not need one. */
		search?: boolean;
		/** Bindable filter text, so a host can seed it from the URL. */
		query?: string;
		searchPlaceholder?: string;
		/** Minimum card width; the grid auto-fills above it. */
		minColumn?: number;
		/** Thumbnail stage height, px. */
		previewHeight?: number;
		/** Off for a single-group registry, where one heading is noise. */
		showGroupHeadings?: boolean;
		/** Override the thumbnail entirely (e.g. to wrap it in a fixed viewBox). */
		preview?: Snippet<[Story, StoryGroup]>;
		/** Shown when the filter matches nothing. */
		empty?: Snippet<[string]>;
	} = $props();

	const shown = $derived(search ? filterGroups(groups, query) : groups);
	const total = $derived(storyCount(groups));
	const matched = $derived(storyCount(shown));
	const linkTo = (s: Story, g: StoryGroup) => href?.(s, g) ?? `${basePath}/${s.slug}`;
</script>

<div class="catalog" style="--min-col: {minColumn}px; --preview-h: {previewHeight}px">
	{#if title || blurb || search}
		<header class="head">
			<div>
				{#if title}<h1>{title}</h1>{/if}
				<p class="sub">
					{#if blurb}{blurb} · {/if}{search && matched !== total
						? `${matched} of ${total}`
						: `${total}`} component{total === 1 ? '' : 's'}
				</p>
			</div>
			{#if search}
				<input
					class="search"
					type="search"
					bind:value={query}
					placeholder={searchPlaceholder}
					aria-label={searchPlaceholder}
				/>
			{/if}
		</header>
	{/if}

	{#if shown.length === 0}
		<div class="none">
			{#if empty}{@render empty(query)}{:else}No component matches “{query}”.{/if}
		</div>
	{/if}

	{#each shown as group (group.id)}
		<section class="group" id={group.id}>
			{#if showGroupHeadings}
				<div class="group-head">
					<h2>{group.title}</h2>
					<span class="count">{group.stories.length}</span>
				</div>
				{#if group.blurb}<p class="sub group-blurb">{group.blurb}</p>{/if}
			{/if}
			<div class="grid">
				{#each group.stories as story (story.slug)}
					<!-- The card is a DIV with a stretched link on the title, not an
					     `<a>` wrapping the preview. A real component library is full of
					     components that render their own `<a>` or `<button>` — a card,
					     a nav node, anything clickable — and an anchor inside an anchor
					     is invalid HTML: the parser silently splits it, so the server's
					     markup and the client's DOM disagree and hydration fails on the
					     whole page. The `::after` overlay keeps the entire card
					     clickable without ever nesting interactive content. -->
					<div class="card">
						<div class="stage" class:note-stage={!isPreviewable(story)}>
							{#if preview}
								{@render preview(story, group)}
							{:else if isPreviewable(story)}
								{@const Preview = story.component!}
								{@const v = previewVariant(story)!}
								<Preview {...v.props} />
							{:else}
								<span class="live-only">live only</span>
								{#if story.note}<span class="note">{story.note}</span>{/if}
							{/if}
						</div>
						<div class="meta">
							<a class="title" href={linkTo(story, group)}>{story.title}</a>
							<span class="sub">
								{#if isPreviewable(story)}
									{story.variants!.length} variant{story.variants!.length === 1 ? '' : 's'}
								{:else}
									no static preview
								{/if}
							</span>
						</div>
					</div>
				{/each}
			</div>
		</section>
	{/each}
</div>

<style>
	.catalog {
		display: flex;
		flex-direction: column;
		gap: var(--space-6);
		color: var(--ink);
	}
	.head {
		display: flex;
		flex-wrap: wrap;
		align-items: flex-end;
		justify-content: space-between;
		gap: var(--space-3);
	}
	h1 {
		margin: 0;
		font-family: var(--font-display, var(--font));
		font-size: var(--font-lg);
	}
	h2 {
		margin: 0;
		font-size: var(--font-md);
	}
	.sub {
		margin: 2px 0 0;
		color: var(--ink-2);
		font-size: var(--font-xs);
	}
	.search {
		flex: 0 1 260px;
		min-width: 160px;
		padding: var(--space-1) var(--space-2);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		background: var(--surface);
		color: var(--ink);
		font: inherit;
		font-size: var(--font-sm);
	}
	.search:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: 1px;
	}
	.group {
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
		/* So an in-page anchor does not tuck the heading under a sticky header. */
		scroll-margin-top: var(--space-6);
	}
	.group-head {
		display: flex;
		align-items: baseline;
		gap: var(--space-2);
		border-bottom: 1px solid var(--border);
		padding-bottom: var(--space-1);
	}
	.count {
		color: var(--ink-2);
		font-size: var(--font-2xs);
		font-variant-numeric: tabular-nums;
	}
	.group-blurb {
		margin: 0;
	}
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(min(var(--min-col), 100%), 1fr));
		gap: var(--space-3);
	}
	.card {
		position: relative;
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
		padding: var(--space-2);
		border: 1px solid var(--border);
		border-radius: var(--radius);
		background: var(--surface);
		color: inherit;
		transition: border-color var(--transition), background var(--transition);
	}
	.card:hover,
	.card:has(a:focus-visible) {
		border-color: var(--accent);
		background: var(--hover, var(--surface-2));
	}
	.stage {
		height: var(--preview-h);
		display: flex;
		align-items: center;
		justify-content: center;
		overflow: hidden;
		background: var(--bg);
		border-radius: var(--radius-sm);
		padding: var(--space-2);
		/* A thumbnail is a picture of the component, never a control: clicks belong
		   to the card's link, and a stray tab stop inside every card would make the
		   index unnavigable by keyboard. */
		pointer-events: none;
	}
	.stage :global(> *) {
		max-width: 100%;
	}
	.note-stage {
		flex-direction: column;
		gap: var(--space-1);
		text-align: center;
	}
	.live-only {
		color: var(--ink-2);
		font-size: var(--font-2xs);
		font-weight: var(--weight-eyebrow);
		letter-spacing: 0.08em;
		text-transform: uppercase;
	}
	.note {
		color: var(--muted);
		font-size: var(--font-xs);
		line-height: 1.4;
		max-width: 28ch;
	}
	.meta {
		display: flex;
		align-items: baseline;
		justify-content: space-between;
		gap: var(--space-2);
	}
	.title {
		font-weight: var(--weight-control);
		font-size: var(--font-sm);
		color: inherit;
		text-decoration: none;
	}
	/* The stretched link: one anchor, the whole card's worth of hit area, and
	   no interactive element ever nested inside another. */
	.title::after {
		content: '';
		position: absolute;
		inset: 0;
		border-radius: inherit;
	}
	.title:focus-visible {
		outline: none;
	}
	.card:has(a:focus-visible) {
		outline: 2px solid var(--accent);
		outline-offset: 1px;
	}
	.none {
		padding: var(--space-6);
		border: 1px dashed var(--border);
		border-radius: var(--radius);
		color: var(--ink-2);
		text-align: center;
		font-size: var(--font-sm);
	}
</style>
