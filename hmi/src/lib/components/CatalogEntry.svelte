<script lang="ts">
	// One story's detail page: title, blurb, and every variant rendered on its
	// own stage with the note that says why that state is worth looking at and a
	// collapsed readout of the exact props that produced it.
	//
	// The props readout is the part that earns the page. A picture of a component
	// tells you it exists; the props tell you how to get that picture in your own
	// screen, which is the question anyone opening a catalog actually has. It
	// stays inside `<details>` so the visual comparison — the reason for the grid
	// — is not pushed off the fold by JSON.
	//
	// See ../catalog.ts for the registry shape.
	import type { Snippet } from 'svelte';
	import { formatProps, isPreviewable, type Story, type StoryGroup } from '../catalog.js';

	let {
		story,
		group,
		backPath = '/components',
		backLabel = 'Component catalog',
		prev,
		next,
		basePath = '/components',
		href,
		showProps = true,
		propsLabel = 'props',
		maxArray = 6,
		minColumn = 340,
		stageMinHeight = 160,
		notFound
	}: {
		/** The resolved story. `undefined` renders the not-found state. */
		story?: Story;
		/** Its group, shown as an eyebrow above the title. */
		group?: StoryGroup;
		/** Back-link target. Empty string hides the back link. */
		backPath?: string;
		backLabel?: string;
		/** Neighbouring stories, for prev/next paging. Omit to hide. */
		prev?: Story;
		next?: Story;
		/** Prefix for the prev/next links. */
		basePath?: string;
		/** Full control over a prev/next link, if `basePath` is not enough. */
		href?: (story: Story) => string;
		/** Off for a demo page where the JSON would be clutter. */
		showProps?: boolean;
		propsLabel?: string;
		/** Arrays longer than this elide in the readout — see `formatProps`. */
		maxArray?: number;
		/** Minimum variant-card width; the grid auto-fills above it. */
		minColumn?: number;
		/** Variant stage min-height, px. */
		stageMinHeight?: number;
		/** Replaces the built-in "unknown component" state. */
		notFound?: Snippet<[]>;
	} = $props();

	const linkTo = (s: Story) => href?.(s) ?? `${basePath}/${s.slug}`;
	const variants = $derived(story?.variants ?? []);
</script>

<div class="entry" style="--min-col: {minColumn}px; --stage-min: {stageMinHeight}px">
	{#if !story}
		{#if notFound}
			{@render notFound()}
		{:else}
			<h1>Unknown component</h1>
			<p class="sub">
				No story with that slug is registered.
				{#if backPath}<a href={backPath}>Back to the {backLabel.toLowerCase()}</a>.{/if}
			</p>
		{/if}
	{:else}
		{#if backPath}
			<p class="back"><a href={backPath}>← {backLabel}</a></p>
		{/if}
		{#if group}<p class="eyebrow">{group.title}</p>{/if}
		<h1>{story.title}</h1>
		<p class="sub blurb">{story.blurb}</p>

		{#if !isPreviewable(story)}
			<div class="live-only">
				<span class="tag">Live only</span>
				<p>
					{story.note ??
						'This component reads its own data from the runtime, so it has no state that can be shown from static props.'}
				</p>
			</div>
		{:else}
			{@const Comp = story.component!}
			<div class="variants">
				{#each variants as v (v.name)}
					<section class="card">
						<h2>{v.name}</h2>
						<div class="stage">
							<Comp {...v.props} />
						</div>
						{#if v.note}<p class="sub note">{v.note}</p>{/if}
						{#if showProps}
							<details>
								<summary>{propsLabel}</summary>
								<pre>{formatProps(v.props, { maxArray })}</pre>
							</details>
						{/if}
					</section>
				{/each}
			</div>
		{/if}

		{#if prev || next}
			<nav class="paging">
				{#if prev}<a href={linkTo(prev)}>← {prev.title}</a>{:else}<span></span>{/if}
				{#if next}<a href={linkTo(next)}>{next.title} →</a>{/if}
			</nav>
		{/if}
	{/if}
</div>

<style>
	.entry {
		color: var(--ink);
	}
	.back {
		margin: 0 0 var(--space-2);
		font-size: var(--font-xs);
	}
	a {
		color: var(--accent);
		text-decoration: none;
	}
	a:hover {
		text-decoration: underline;
	}
	.eyebrow {
		margin: 0;
		color: var(--ink-2);
		font-size: var(--font-2xs);
		font-weight: var(--weight-eyebrow);
		letter-spacing: 0.08em;
		text-transform: uppercase;
	}
	h1 {
		margin: 0;
		font-family: var(--font-display, var(--font));
		font-size: var(--font-lg);
	}
	h2 {
		margin: 0 0 var(--space-2);
		font-size: var(--font-sm);
		font-weight: var(--weight-control);
	}
	.sub {
		color: var(--ink-2);
		font-size: var(--font-xs);
		line-height: 1.5;
	}
	.blurb {
		margin: var(--space-1) 0 0;
		max-width: 72ch;
	}
	.live-only {
		margin-top: var(--space-4);
		padding: var(--space-4);
		border: 1px dashed var(--border);
		border-radius: var(--radius);
		background: var(--surface);
		max-width: 72ch;
	}
	.live-only p {
		margin: var(--space-1) 0 0;
		color: var(--ink-2);
		font-size: var(--font-sm);
		line-height: 1.5;
	}
	.tag {
		color: var(--ink-2);
		font-size: var(--font-2xs);
		font-weight: var(--weight-eyebrow);
		letter-spacing: 0.08em;
		text-transform: uppercase;
	}
	.variants {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(var(--min-col), 1fr));
		gap: var(--space-3);
		margin-top: var(--space-4);
	}
	.card {
		padding: var(--space-3);
		border: 1px solid var(--border);
		border-radius: var(--radius);
		background: var(--surface);
	}
	.stage {
		display: flex;
		align-items: center;
		justify-content: center;
		background: var(--bg);
		border-radius: var(--radius-sm);
		padding: var(--space-4);
		min-height: var(--stage-min);
		overflow: auto;
	}
	.stage :global(> *) {
		max-width: 100%;
	}
	.note {
		margin: var(--space-2) 0 0;
	}
	details {
		margin-top: var(--space-2);
	}
	summary {
		cursor: pointer;
		color: var(--ink-2);
		font-size: var(--font-2xs);
	}
	pre {
		margin: var(--space-1) 0 0;
		padding: var(--space-2);
		background: var(--bg);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		font-family: var(--mono);
		font-size: var(--font-2xs);
		color: var(--ink-2);
		overflow-x: auto;
	}
	.paging {
		display: flex;
		justify-content: space-between;
		gap: var(--space-3);
		margin-top: var(--space-6);
		padding-top: var(--space-3);
		border-top: 1px solid var(--border);
		font-size: var(--font-xs);
	}
</style>
