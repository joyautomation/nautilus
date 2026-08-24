<script lang="ts">
	// App shell: sticky sidebar (brand, nav, theme switch) beside a main
	// column with a sticky header (breadcrumb + status pills). Pulls in the
	// kit's theme tokens once, initializes the theme store (reads the saved
	// preference, follows the OS otherwise), and starts the one shared
	// realtime client the header pills and every page read from.
	import '@joyautomation/nautilus-hmi/theme.css';
	// The house faces, self-hosted via @fontsource — never a CDN link. Optional:
	// theme.css only names the families, this ships them.
	import '@joyautomation/nautilus-hmi/fonts.css';
	import './app.css';
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { theme, ConfirmDialog, StatusPill, ThemeSwitch } from '@joyautomation/nautilus-hmi';
	import { rt } from '$lib/client.svelte';

	let { children } = $props();
	onMount(() => {
		theme.init();
		rt.start();
		return () => rt.stop();
	});

	const nav = [
		{ href: '/', label: 'Overview', icon: '⬡' },
		{ href: '/trends', label: 'Trends', icon: '∿' },
		{ href: '/primitives', label: 'Primitives', icon: '▦' },
		{ href: '/legacy', label: 'Legacy port', icon: '⌸' }
	];

	function isActive(href: string) {
		return href === '/' ? page.url.pathname === '/' : page.url.pathname.startsWith(href);
	}

	// Header pills: alarm summary from the annunciator tags, link health from
	// the client's data freshness. No tag behind a pill, no pill.
	const tags = $derived((rt.frame?.tags ?? {}) as Record<string, unknown>);
	const hiAlm = $derived(tags.HiTempAlm === true);
	const lowAlm = $derived(tags.TempLowAlm === true);
	const alarmCount = $derived((hiAlm ? 1 : 0) + (lowAlm ? 1 : 0));
	const alarmKind = $derived(hiAlm ? ('critical' as const) : lowAlm ? ('warning' as const) : ('good' as const));
</script>

<!-- Mounted ONCE, app-wide: every `await confirm({…})` anywhere renders here. -->
<ConfirmDialog />

<div class="shell">
	<aside>
		<div class="brand">
			<span class="logo">⬢</span>
			<div>
				<div class="name display">NAUTILUS</div>
				<div class="subtle">heated tank demo</div>
			</div>
		</div>
		<nav>
			{#each nav as n (n.href)}
				<a href={n.href} class:active={isActive(n.href)}>
					<span class="icon" aria-hidden="true">{n.icon}</span>{n.label}
				</a>
			{/each}
		</nav>
		<div class="spacer"></div>
		<div class="swgroup">
			<span class="swlabel subtle">Theme</span>
			<ThemeSwitch />
		</div>
		<div class="foot subtle">
			nautilus controller · HMI kit<br />SSE via /api/stream
		</div>
	</aside>

	<div class="main">
		<header>
			<div class="crumb subtle">
				{nav.find((n) => isActive(n.href))?.label ?? ''}
			</div>
			<div class="status">
				{#if rt.frame}
					<StatusPill
						kind={alarmKind}
						label={alarmCount ? `${alarmCount} alarm${alarmCount > 1 ? 's' : ''}` : 'No alarms'}
					/>
				{/if}
				<StatusPill kind={rt.connected ? 'good' : 'critical'} label={rt.connected ? 'Connected' : 'Offline'} />
			</div>
		</header>
		<main>
			{@render children()}
		</main>
	</div>
</div>

<style>
	.shell {
		display: flex;
		min-height: 100vh;
	}
	.subtle {
		color: var(--muted);
		font-size: var(--font-2xs);
	}
	aside {
		width: 210px;
		flex-shrink: 0;
		border-right: 1px solid var(--border);
		padding: 18px 12px;
		display: flex;
		flex-direction: column;
		gap: 20px;
		position: sticky;
		top: 0;
		height: 100vh;
		box-sizing: border-box;
	}
	.brand {
		display: flex;
		gap: 10px;
		align-items: center;
		padding: 0 8px;
	}
	.logo {
		font-size: 24px;
		color: var(--s1);
	}
	.name {
		font-weight: 750;
		letter-spacing: 0.04em;
	}
	/* Righteous is CHROME ONLY — a wordmark, never a process value. */
	.name.display {
		font-family: var(--font-display);
		font-weight: 400;
		font-size: var(--font-md);
	}
	.spacer {
		flex: 1;
	}
	.swgroup {
		display: flex;
		flex-direction: column;
		gap: 4px;
		padding: 0 8px;
	}
	.swlabel {
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.06em;
	}
	nav {
		display: flex;
		flex-direction: column;
		gap: 2px;
	}
	nav a {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 9px 12px;
		border-radius: 8px;
		color: var(--ink-2);
		font-weight: 550;
		text-decoration: none;
	}
	nav a:hover {
		background: var(--surface);
	}
	nav a.active {
		background: var(--surface-2);
		color: var(--ink);
		box-shadow: inset 2px 0 0 var(--s1);
	}
	.icon {
		width: 16px;
		text-align: center;
		color: var(--muted);
	}
	nav a.active .icon {
		color: var(--s1);
	}
	.foot {
		padding: 0 8px;
		line-height: 1.6;
	}
	.main {
		flex: 1;
		min-width: 0;
	}
	header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		padding: 14px 24px;
		border-bottom: 1px solid var(--border);
		position: sticky;
		top: 0;
		background: color-mix(in srgb, var(--bg) 88%, transparent);
		backdrop-filter: blur(8px);
		z-index: 10;
	}
	.status {
		display: flex;
		gap: 8px;
	}
	main {
		padding: 24px;
	}
</style>
