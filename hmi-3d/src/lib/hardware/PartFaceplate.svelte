<script lang="ts">
	// Click-to-inspect for a server part, in the kit's standard faceplate:
	// label and tag in the header, the part's state as a chip, the headline
	// (capacity and temperature for a drive, speed for a fan…) as the hero,
	// then every fact the part's UDT carries. Read-only.
	import { FaceplateShell, type Quality } from '@joyautomation/nautilus-hmi';
	import { partFacts, partState, partStatus, serverFacts, type ServerPart, type PartState } from './profile.js';

	let {
		part,
		server,
		value,
		quality = 'good',
		note,
		onclose
	}: {
		/** The part picked, or undefined for the chassis. */
		part?: ServerPart;
		/** The chassis: its label and tag, when `part` is undefined. */
		server?: { label: string; tag?: string; model?: string };
		value: unknown;
		quality?: Quality;
		/** A line under the facts, e.g. where a stubbed value came from. */
		note?: string;
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
	let facts = $derived(part ? partFacts(part.kind, shown) : serverFacts(shown));
	let headline = $derived(part ? partStatus(part.kind, shown, state === 'assumed' ? 'ok' : state) : '');
	let title = $derived(part ? part.slot : (server?.label ?? ''));
	let tag = $derived(part ? (part.tag ?? '') : (server?.tag ?? ''));
</script>

<FaceplateShell label={title} {tag} {quality} present={state !== 'missing' || !part} size="md" chips={[CHIP[state]]} {onclose}>
	{#snippet hero()}
		{#if headline && state !== 'unbound' && state !== 'missing'}
			<p class="hero">{headline}</p>
		{/if}
	{/snippet}
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

<style>
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
