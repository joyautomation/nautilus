<script lang="ts">
	// A tab bar (the faceplate subview switcher). Renders only the bar —
	// bind `active` and conditionally render your own panels, so content
	// stays plain Svelte.
	let {
		tabs,
		active = $bindable(0)
	}: {
		tabs: string[];
		active?: number;
	} = $props();
</script>

<div class="tabs" role="tablist">
	{#each tabs as t, i (t)}
		<button
			type="button"
			role="tab"
			aria-selected={active === i}
			class:active={active === i}
			onclick={() => (active = i)}
		>
			{t}
		</button>
	{/each}
</div>

<style>
	.tabs {
		display: flex;
		gap: 2px;
		border-bottom: 1px solid var(--border, #21262d);
		overflow-x: auto;
		/* This strip sets overflow-x so long tab lists scroll horizontally
		   instead of wrapping. That overflow also makes it a candidate for the
		   flex/grid "automatic minimum size" trap: a flex or grid item whose
		   overflow isn't `visible` gets an automatic min-size of 0 instead of
		   its content size, so a squeezed ancestor (e.g. a modal body pinned
		   under a max-height) can shrink this row all the way to ~0px before
		   touching any sibling that still has overflow:visible. flex-shrink:0
		   plus an explicit (non-auto) min-height both defeat that: they give
		   the strip a real floor no matter what kind of container it's
		   dropped into. */
		flex-shrink: 0;
		min-height: min-content;
	}
	button {
		flex: none;
		padding: 7px 12px;
		border: none;
		border-radius: 0;
		border-bottom: 2px solid transparent;
		background: transparent;
		color: var(--muted, #8b949e);
		font-size: var(--font-2xs);
		font-weight: 700;
		letter-spacing: 0.06em;
		text-transform: uppercase;
		cursor: pointer;
		white-space: nowrap;
	}
	button:hover {
		color: var(--ink-2, #c3ccd6);
	}
	button.active {
		color: var(--ink, #e6edf3);
		border-bottom-color: var(--s1, #3987e5);
	}
	button:focus-visible {
		outline: 2px solid var(--s1, #3987e5);
		outline-offset: -2px;
	}

	/* Coarse pointer (touch/pen): grow to the minimum hit area. Desktop
	   density (mouse/trackpad) is untouched. */
	@media (pointer: coarse) {
		button {
			min-height: var(--tap);
		}
	}
</style>
