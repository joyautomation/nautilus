<script lang="ts">
	// Click-to-inspect for a server part, in the kit's standard faceplate:
	// label and tag in the header, the part's state as a chip, the headline
	// (capacity and temperature for a drive, speed for a fan…) as the hero,
	// then every fact the part's UDT carries. Read-only.
	import { FaceplateShell, type Quality } from '@joyautomation/nautilus-hmi';
	import { partFacts, partState, partStatus, serverFacts, switchFacts, type Fact, type ServerPart, type PartState } from './profile.js';

	let {
		part,
		server,
		value,
		quality = 'good',
		note,
		extra = [],
		onclose
	}: {
		/** The part picked, or undefined for the chassis. */
		part?: ServerPart;
		/** The chassis: its label and tag, when `part` is undefined. */
		server?: { label: string; tag?: string; model?: string; kind?: 'server' | 'switch'; portsUp?: number };
		value: unknown;
		quality?: Quality;
		/** A line under the facts, e.g. where a stubbed value came from. */
		note?: string;
		/** More rows after the part's own facts: a port's cable, say. */
		extra?: Fact[];
		onclose: () => void;
	} = $props();

	const CHIP: Record<PartState, { label: string; kind: 'good' | 'warning' | 'critical' | 'off' | 'stale' | 'notPublished' | 'neutral' }> = {
		ok: { label: 'Healthy', kind: 'good' },
		warning: { label: 'Warning', kind: 'warning' },
		critical: { label: 'Critical', kind: 'critical' },
		stale: { label: 'Stale', kind: 'stale' },
		absent: { label: 'Absent', kind: 'critical' },
		missing: { label: 'Empty', kind: 'off' },
		assumed: { label: 'Unverified', kind: 'neutral' },
		unbound: { label: 'Empty', kind: 'off' }
	};

	let shown = $derived(value ?? part?.static);
	let state = $derived<PartState>(part ? partState(part, value, quality === 'good') : value ? 'ok' : 'missing');
	let facts = $derived([...(part ? partFacts(part.kind, shown) : server?.kind === 'switch' ? switchFacts(shown, server.portsUp) : serverFacts(shown)), ...extra]);
	let headline = $derived(part ? partStatus(part.kind, shown, state === 'assumed' ? 'ok' : state) : '');
	let title = $derived(part ? part.slot : (server?.label ?? ''));
	let tag = $derived(part ? (part.tag ?? '') : (server?.tag ?? ''));
</script>

<!-- Below the shell's breakpoint it is a page, not a dialog, and a page
     sits in the document flow — under a 3D view that fills the screen. The
     host lifts it into its own full-screen layer there; above the
     breakpoint the dialog is in the top layer and the host steps aside. -->
<div class="host">
{#snippet heroLine()}
	<p class="hero">{headline}</p>
{/snippet}
<FaceplateShell
	label={title}
	{tag}
	{quality}
	present={state !== 'missing' || !part}
	size="md"
	chips={[CHIP[state]]}
	hero={headline && state !== 'unbound' && state !== 'missing' ? heroLine : undefined}
	{onclose}
>
	{#if state === 'unbound'}
		<p class="muted">Nothing is fitted here, and the profile binds no tag to this position.</p>
	{:else if state === 'missing'}
		<p class="muted">Empty: the controller publishes no <code>{tag}</code>, which a driver only does for a fitted part.</p>
	{/if}
	{#if facts.length}
		<dl>
			{#each facts as f}
				<dt>{f.label}</dt>
				<dd>{f.value}</dd>
			{/each}
		</dl>
	{/if}
	{#if state === 'assumed'}
		<p class="muted">From the chassis profile, not a driver: to be confirmed on the hardware.</p>
	{/if}
	{#if note}
		<p class="muted">{note}</p>
	{/if}
</FaceplateShell>
</div>

<style>
	.host {
		display: contents;
	}
	@media (max-width: 899.98px) {
		.host {
			display: block;
			position: fixed;
			inset: 0;
			z-index: 20;
			overflow: auto;
			overscroll-behavior: contain;
			/* Mildly see-through, so the server stays in view behind the
			   faceplate; the blur keeps the text readable over it. */
			background: transparent;
			backdrop-filter: blur(3px);
			-webkit-backdrop-filter: blur(3px);
		}
		.host :global(.fp.page) {
			background: transparent;
		}
		.host :global(.fp.page > .box) {
			background: color-mix(in srgb, var(--surface, #1a1a19) 86%, transparent);
		}
		.host :global(.fp.page header) {
			background: color-mix(in srgb, var(--surface, #1a1a19) 92%, transparent);
		}
	}
	.hero {
		margin: 0;
		font: 600 22px/1.2 system-ui, sans-serif;
		color: var(--ink, #e8e6e1);
	}
	dl {
		display: grid;
		grid-template-columns: max-content 1fr;
		gap: 4px 16px;
		margin: 0;
		font: 14px/1.4 system-ui, sans-serif;
	}
	dt {
		color: var(--ink-2, #a8a6a1);
	}
	dd {
		margin: 0;
		font-family: ui-monospace, monospace;
	}
	.muted {
		color: var(--ink-2, #a8a6a1);
		font-size: 13px;
	}
</style>
