<script lang="ts">
	// Notifications and install for this device (#722). Turn on to be paged
	// by the controller's alarm escalation (`via: push`): Enable asks the
	// browser's permission (it must be a tap), subscribes, and hands the
	// controller the subscription, which belongs to the person signed in.
	// Without https the card says "install needs https" and offers nothing;
	// on an iPhone it says to add the page to the Home Screen first.
	//
	// It asks the controller (GET /api/push/key) before anything else, so a
	// controller with no push never causes a permission prompt. A sign-in is
	// the kit's (createAuth); the controller never returns an endpoint or a
	// key, so the list below names a push service's host only.
	import { onMount, untrack } from 'svelte';
	import {
		PushClient,
		pushState,
		readPushEnv,
		realPushBrowser,
		enablePush,
		disablePush,
		endpointId,
		type PushDevice,
		type PushEnv,
		type PushBrowser
	} from '../push.js';
	import { currentAuth } from '../auth.svelte.js';
	import { signInLink } from '../auth.js';
	import Button from './Button.svelte';

	let {
		client = new PushClient(),
		browser = undefined,
		env = undefined
	}: {
		/** Default: `new PushClient()` (/api/push through the kit's fetch). */
		client?: PushClient;
		/** The browser parts; default the real ones. A host page may pass a test's. */
		browser?: PushBrowser;
		/** What the page can see; default read from the browser. */
		env?: PushEnv;
	} = $props();

	const auth = $derived(currentAuth());
	// no auth: block on the controller (me.auth false) means no one to
	// subscribe as; no createAuth() at all: not known, the controller decides
	const signedIn = $derived(auth ? !!(auth.me?.auth && auth.user) : undefined);

	let seen = $state<PushEnv>(readPushEnv()); // load() re-reads it (or takes `env`)
	let configured = $state<boolean | undefined>(undefined);
	let devices = $state<PushDevice[]>([]);
	let mine = $state(''); // the id of this browser's subscription, when it has one
	let unauthorized = $state(false); // the controller answered 401: nobody is signed in here
	let busy = $state(false);
	let error = $state('');
	let install = $state<{ prompt(): Promise<unknown> } | null>(null);

	const subscribed = $derived(mine !== '' && devices.some((d) => d.id === mine));
	// a 401 from the controller (a session that ended) overrides what the page believes
	const view = $derived(pushState(seen, { configured, signedIn: unauthorized ? false : signedIn, hasAuth: auth ? !!auth.me?.auth : undefined, subscribed }));
	const signin = $derived(signInLink(auth?.me, seen.secure ? globalThis.location : { pathname: '/', search: '' }));
	const others = $derived(devices.filter((d) => d.id !== mine));

	function pb(): PushBrowser {
		return browser ?? realPushBrowser();
	}

	// one load at a time: a second call while one runs asks for another pass
	let running = false;
	let again = false;
	export async function load() {
		if (running) {
			again = true;
			return;
		}
		running = true;
		try {
			do {
				again = false;
				await pass();
			} while (again);
		} finally {
			running = false;
		}
	}

	async function pass() {
		seen = env ?? readPushEnv();
		if (!seen.secure || signedIn === false || view.kind === 'no-auth') return;
		unauthorized = false;
		const k = await client.key();
		unauthorized = !k.ok && k.status === 401;
		if (!k.ok) {
			configured = k.status === 404 ? false : configured;
			return;
		}
		configured = true;
		const l = await client.list();
		if (l.ok) devices = l.value;
		mine = '';
		try {
			const sub = await (await pb().registration()).pushManager.getSubscription();
			if (sub) mine = await endpointId(sub.endpoint);
		} catch {
			/* no worker yet: this browser holds nothing */
		}
	}

	onMount(() => {
		load();
		const before = (e: Event) => {
			e.preventDefault();
			install = e as unknown as { prompt(): Promise<unknown> };
		};
		const done = () => (install = null);
		globalThis.addEventListener?.('beforeinstallprompt', before);
		globalThis.addEventListener?.('appinstalled', done);
		return () => {
			globalThis.removeEventListener?.('beforeinstallprompt', before);
			globalThis.removeEventListener?.('appinstalled', done);
		};
	});

	// the sign-in may arrive after the card
	$effect(() => {
		if (signedIn) untrack(() => void load());
	});

	async function enable() {
		busy = true;
		error = '';
		const r = await enablePush(client, pb());
		busy = false;
		if (!r.ok) error = r.message;
		await load();
	}

	async function disable() {
		busy = true;
		error = '';
		const r = await disablePush(client, pb());
		busy = false;
		if (!r.ok) error = r.message;
		await load();
	}

	async function remove(id: string) {
		busy = true;
		error = '';
		const r = await client.remove({ id });
		busy = false;
		if (!r.ok) error = r.message;
		await load();
	}

	async function installApp() {
		const p = install;
		install = null;
		await p?.prompt();
	}
</script>

<section class="push" aria-label="Notifications">
	<h3>Notifications on this device</h3>
	<p class="state" data-state={view.kind} role="status" aria-live="polite">{view.message}</p>
	<div class="row">
		{#if view.kind === 'signed-out'}
			<a class="signin" href={signin.href}>{signin.label}</a>
		{:else if view.kind === 'on'}
			<Button size="sm" variant="secondary" disabled={busy} onclick={disable}>Turn off</Button>
		{:else if view.canEnable}
			<Button size="sm" variant="primary" disabled={busy} onclick={enable}>Turn on notifications</Button>
		{/if}
		{#if install && seen.secure && !seen.standalone}
			<Button size="sm" variant="ghost" onclick={installApp}>Install this app</Button>
		{/if}
	</div>
	{#if error}
		<p class="err" role="alert">{error}</p>
	{/if}
	{#if others.length > 0}
		<h4>Your other devices</h4>
		<ul>
			{#each others as d (d.id)}
				<li>
					<span class="host">{d.host}</span>
					<span class="at">{new Date(d.at).toLocaleDateString()}</span>
					<Button size="sm" variant="ghost" disabled={busy} onclick={() => remove(d.id)} aria-label={`Remove the device paged through ${d.host}`}>Remove</Button>
				</li>
			{/each}
		</ul>
	{/if}
</section>

<style>
	.push {
		display: grid;
		gap: 8px;
		font-size: var(--font-xs);
		color: var(--ink-2);
	}
	h3,
	h4 {
		margin: 0;
		font-size: var(--font-sm);
		color: var(--ink);
	}
	h4 {
		font-size: var(--font-xs);
		color: var(--muted);
	}
	p {
		margin: 0;
	}
	.row {
		display: flex;
		flex-wrap: wrap;
		gap: 8px;
	}
	.signin {
		color: var(--accent, var(--ink));
		font-weight: 600;
	}
	.err {
		color: var(--crit);
	}
	ul {
		list-style: none;
		margin: 0;
		padding: 0;
		display: grid;
		gap: 4px;
	}
	li {
		display: flex;
		align-items: center;
		gap: 10px;
	}
	.host {
		font-family: var(--mono);
	}
	.at {
		color: var(--muted);
		flex: 1 1 0;
	}
</style>
