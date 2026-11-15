<script lang="ts" module>
	/** Components found to be SVG parts (see `svgHost`), by name: the next
	 * mount of one (every good compile re-keys the island) goes straight
	 * into an svg instead of mounting twice to find out again. */
	const svgParts = new Set<string>();
	const SVG_NS = 'http://www.w3.org/2000/svg';
	/** Room round an SVG part's bounding box, which leaves strokes out. */
	const SVG_PAD = 4;
</script>

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
	//
	// `svgHost` (the component preview): a component whose root is an SVG
	// element other than <svg> (a <g>, a <path>: a part drawn inside a
	// page's own <svg>) draws nothing in a div, so it is mounted in an
	// <svg> fitted to what it draws. Not in the mimic editor: a screen
	// places components in HTML boxes, and showing one there that the
	// built screen would not draw would be a lie.
	//
	// `frame` (the component preview too): a component whose roots are all
	// positioned out of flow (an `aside` laid over the page, absolute or
	// fixed) takes up no room, and sizes itself against a parent that here
	// has none: it collapsed to a sliver over a 0 × 0 box. It is given a
	// frame of this size, in px, to lie over instead, standing in for the
	// page.
	import { untrack } from 'svelte';
	import { hasUserComponent, userComponentApi, userComponentDiagnostic, userComponentPending } from './userComponentsState.svelte';

	let {
		name,
		props = {},
		svgHost = false,
		onsvgpart,
		frame,
		onframed
	}: {
		name: string;
		props?: Record<string, unknown>;
		svgHost?: boolean;
		onsvgpart?: (part: boolean) => void;
		frame?: { w: number; h: number };
		onframed?: (framed: boolean) => void;
	} = $props();

	let el: HTMLDivElement | null = $state(null);
	let svgEl: SVGSVGElement | null = $state(null);
	let groupEl: SVGGElement | null = $state(null);
	let asSvg = $state(untrack(() => svgHost && svgParts.has(name)));
	$effect(() => onsvgpart?.(asSvg));
	let framed = $state(false);
	$effect(() => onframed?.(framed && !asSvg));
	let handle: { update(p: Record<string, unknown>): void; destroy(): void } | null = null;

	const available = $derived(hasUserComponent(name));
	const diagnostic = $derived(userComponentDiagnostic(name));
	const pending = $derived(userComponentPending(name));

	// A component that throws (mounting, or on a later prop change) shows
	// why in its own box. Uncaught, the throw stopped Svelte's effect flush,
	// and every island placed after it stayed empty with nothing said
	// (a prop named `state` beside `$state(...)` did it to whole screens).
	let failed = $state('');

	$effect(() => {
		const target = asSvg ? groupEl : el;
		if (!available || !target) return;
		const api = userComponentApi(name);
		if (!api) return;
		failed = '';
		try {
			handle = api.mount(target, { ...untrack(() => props) });
		} catch (err) {
			handle = null;
			failed = errorText(err);
			target.replaceChildren();
		}
		if (svgHost && handle && !checkRoot(target) && asSvg) fit();
		if (!asSvg) checkFrame(target);
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

	/** Whether the component is mounted in the wrong kind of parent for
	 * its root element: if so, switches (the mount effect's teardown takes
	 * it out and the effect runs again on the other parent). A component
	 * with no root element yet (an {#if} waiting on a prop) stays put. */
	function checkRoot(target: Element): boolean {
		const root = target.firstElementChild;
		const part = !!root && root.namespaceURI === SVG_NS && root.localName !== 'svg';
		if (!root || part === asSvg) return false;
		if (part) svgParts.add(name);
		else svgParts.delete(name);
		asSvg = part;
		return true;
	}
	// A root that arrives later (that {#if}, once its prop is set).
	$effect(() => {
		if (!svgHost || !el) return;
		const div = el;
		const mo = new MutationObserver(() => {
			if (!checkRoot(div)) checkFrame(div);
		});
		mo.observe(div, { childList: true });
		return () => mo.disconnect();
	});

	/** Frames the component when everything it renders at its root is out
	 * of flow; anything in flow (an element, or text) gives the box its
	 * size as ever. Judged on position alone, never on the size the frame
	 * itself changes. */
	function checkFrame(target: Element) {
		if (!frame) return;
		const roots = [...target.children];
		const text = [...target.childNodes].some((n) => n.nodeType === Node.TEXT_NODE && (n.textContent ?? '').trim() !== '');
		framed =
			roots.length > 0 &&
			!text &&
			roots.every((r) => {
				const position = getComputedStyle(r).position;
				return position === 'absolute' || position === 'fixed';
			});
	}

	/** Sizes the host svg to what the part draws, wherever it draws it
	 * (a part takes its own x and y): nothing drawn is 0 × 0. */
	function fit() {
		if (!svgEl || !groupEl) return;
		let b: DOMRect;
		try {
			b = groupEl.getBBox();
		} catch {
			return;
		}
		const drawn = [b.x, b.y, b.width, b.height].every(Number.isFinite) && (b.width > 0 || b.height > 0);
		const w = drawn ? Math.ceil(b.width + 2 * SVG_PAD) : 0;
		const h = drawn ? Math.ceil(b.height + 2 * SVG_PAD) : 0;
		const viewBox = `${b.x - SVG_PAD} ${b.y - SVG_PAD} ${w} ${h}`;
		// Only on a change: these writes are mutations the observer below sees.
		if (svgEl.getAttribute('width') !== String(w)) svgEl.setAttribute('width', String(w));
		if (svgEl.getAttribute('height') !== String(h)) svgEl.setAttribute('height', String(h));
		if (drawn && svgEl.getAttribute('viewBox') !== viewBox) svgEl.setAttribute('viewBox', viewBox);
	}
	// A prop change, an {#if} branch: whatever moves the drawing re-fits it.
	$effect(() => {
		if (!groupEl) return;
		const group = groupEl;
		const mo = new MutationObserver(() => {
			if (!checkRoot(group)) fit();
		});
		mo.observe(group, { subtree: true, childList: true, attributes: true, characterData: true });
		return () => mo.disconnect();
	});

	function errorText(err: unknown): string {
		return err instanceof Error ? err.message : String(err);
	}
</script>

{#if available}
	{#if asSvg}
		<svg class="nx-user-island-svg" width="0" height="0" bind:this={svgEl}><g bind:this={groupEl}></g></svg>
	{:else}
		<div
			class="nx-user-island"
			class:framed
			style:width={framed && frame ? frame.w + 'px' : null}
			style:height={framed && frame ? frame.h + 'px' : null}
			bind:this={el}
		></div>
	{/if}
	{#if failed}
		<span class="broken" title={failed}><b>{name}</b> threw: {failed}</span>
	{/if}
{:else if diagnostic}
	<span class="broken" title={diagnostic}><b>{name}</b>: {diagnostic}</span>
{:else if pending}
	<!-- On its way (the playground compiles components in the browser, which
	     takes a few seconds the first time): a quiet placeholder, not the red
	     chip that means something is wrong. -->
	<span class="pending" role="status" title={`Compiling ${name}.svelte: it appears here when it is built`}>
		<span class="spin" aria-hidden="true"></span><b>{name}</b><span class="what">compiling…</span>
	</span>
{:else}
	<span class="unknown" title={`no compiled component for ${name}`}>{name}?</span>
{/if}

<style>
	.nx-user-island {
		display: contents;
	}
	/* contain: layout makes it the containing block of a fixed root as well
	   as an absolute one */
	.nx-user-island.framed {
		display: block;
		position: relative;
		contain: layout;
		overflow: hidden;
	}
	.nx-user-island-svg {
		display: block;
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
	.pending {
		display: flex;
		align-items: center;
		align-content: center;
		justify-content: center;
		flex-wrap: wrap;
		gap: 2px 6px;
		box-sizing: border-box;
		width: 100%;
		height: 100%;
		min-width: 72px;
		min-height: 26px;
		padding: 4px 8px;
		border: 1px dashed var(--nx-border);
		border-radius: 6px;
		background: color-mix(in srgb, var(--nx-muted) 6%, transparent);
		color: var(--nx-muted);
		font-size: 11px;
		font-family: var(--nx-mono);
		line-height: 1.3;
		overflow: hidden;
		animation: nx-pending-breathe 1.6s ease-in-out infinite;
	}
	/* a long name in a narrow box ends in an ellipsis, not cut at both ends */
	.pending b {
		min-width: 0;
		max-width: 100%;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		font-weight: 600;
	}
	.pending .what {
		opacity: 0.8;
	}
	.spin {
		flex: none;
		width: 9px;
		height: 9px;
		border: 1.5px solid currentColor;
		border-right-color: transparent;
		border-radius: 50%;
		animation: nx-pending-spin 0.9s linear infinite;
	}
	@keyframes nx-pending-spin {
		to {
			transform: rotate(360deg);
		}
	}
	@keyframes nx-pending-breathe {
		50% {
			background: color-mix(in srgb, var(--nx-muted) 14%, transparent);
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.pending,
		.spin {
			animation: none;
		}
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
