<script lang="ts">
	// One user-authored component, mounted as an isolated island via
	// window.__NX_USER_COMPONENTS__ (userComponents.ts's esbuild bundle,
	// compiled with the USER's own node_modules). Falls back to the same
	// dashed placeholder chip the editor has always shown for an unknown
	// component when there's no bundle yet, the name isn't in it, or its
	// compile failed — the failure's message becomes the chip's tooltip.
	//
	// The mount/update/destroy handle (registry.ts's mount() wraps a $state
	// props object) is the seam: creating it hands over the DOM node once,
	// and every later prop change flows through handle.update() rather than
	// remounting — same "gestures mutate, the host confirms" shape as the
	// rest of the editor, just one level down.
	import { untrack } from 'svelte';
	import { hasUserComponent, userComponentApi, userComponentDiagnostic } from './userComponentsState.svelte';

	let { name, props = {} }: { name: string; props?: Record<string, unknown> } = $props();

	let el: HTMLDivElement | null = $state(null);
	let handle: { update(p: Record<string, unknown>): void; destroy(): void } | null = null;

	const available = $derived(hasUserComponent(name));
	const diagnostic = $derived(userComponentDiagnostic(name));

	// A component that throws (mounting, or on a later prop change) shows
	// why in its own box. Uncaught, the throw stopped Svelte's effect flush,
	// and every island placed after it stayed empty with nothing said
	// (a prop named `state` beside `$state(...)` did it to whole screens).
	let failed = $state('');

	$effect(() => {
		if (!available || !el) return;
		const api = userComponentApi(name);
		if (!api) return;
		failed = '';
		try {
			handle = api.mount(el, { ...untrack(() => props) });
		} catch (err) {
			handle = null;
			failed = errorText(err);
			el.replaceChildren();
		}
		return () => {
			try {
				handle?.destroy();
			} catch {
				// a component that threw mounting may throw tearing down too
			}
			handle = null;
		};
	});

	// A separate effect so a props-only change updates the live instance
	// instead of tearing it down and remounting.
	$effect(() => {
		const p = props;
		if (!handle) return;
		try {
			handle.update({ ...p });
		} catch (err) {
			failed = errorText(err);
		}
	});

	function errorText(err: unknown): string {
		return err instanceof Error ? err.message : String(err);
	}
</script>

{#if available}
	<div class="nx-user-island" bind:this={el}></div>
	{#if failed}
		<span class="broken" title={failed}><b>{name}</b> threw: {failed}</span>
	{/if}
{:else if diagnostic}
	<span class="broken" title={diagnostic}><b>{name}</b>: {diagnostic}</span>
{:else}
	<span class="unknown" title={`no compiled component for ${name}`}>{name}?</span>
{/if}

<style>
	.nx-user-island {
		display: contents;
	}
	/* fills the equipment's box, so the reason is readable on the canvas
	   and in a screenshot, not only in a tooltip */
	.broken {
		display: block;
		box-sizing: border-box;
		width: 100%;
		height: 100%;
		min-width: 120px;
		min-height: 40px;
		overflow: auto;
		padding: 6px 8px;
		border: 1px dashed var(--nx-err);
		border-radius: 6px;
		background: color-mix(in srgb, var(--nx-err) 8%, transparent);
		color: var(--nx-err);
		font-size: 12px;
		line-height: 1.35;
		font-family: var(--nx-mono);
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
	.unknown {
		display: inline-block;
		padding: 4px 8px;
		border: 1px dashed var(--nx-err);
		border-radius: 6px;
		color: var(--nx-err);
		font-size: 12px;
		font-family: var(--nx-mono);
	}
</style>
