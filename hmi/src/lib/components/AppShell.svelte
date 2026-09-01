<script lang="ts">
	// Layout scaffold for a full HMI application: a top menu bar, a left nav
	// rail, and a content area. Purely structural — compose it from MenuBar /
	// Nav / your own content; AppShell just owns the grid and lets Nav's own
	// `collapsed` state (bindable, pass it through) resize the rail smoothly.
	import type { Snippet } from 'svelte';

	let {
		menubar,
		nav,
		children
	}: {
		/** Rendered full-width along the top, typically a <MenuBar/>. */
		menubar?: Snippet;
		/** Rendered as the left rail, typically a <Nav/>. */
		nav?: Snippet;
		/** Main content area. */
		children?: Snippet;
	} = $props();
</script>

<div class="shell">
	{#if menubar}<div class="shell-menubar">{@render menubar()}</div>{/if}
	<div class="shell-body">
		{#if nav}<div class="shell-nav">{@render nav()}</div>{/if}
		<main class="shell-main">
			{#if children}{@render children()}{/if}
		</main>
	</div>
</div>

<style>
	.shell {
		display: flex;
		flex-direction: column;
		height: 100%;
		min-height: 100vh;
		min-height: 100dvh;
		background: var(--bg);
		color: var(--ink);
	}
	.shell-menubar {
		flex: none;
	}
	.shell-body {
		flex: 1;
		display: flex;
		min-height: 0;
	}
	.shell-nav {
		flex: none;
		display: flex;
	}
	.shell-main {
		flex: 1;
		min-width: 0;
		overflow-y: auto;
		padding: 1.5rem;
	}
</style>
