<script lang="ts">
	// Who this browser is signed in as, and a way out (#708). Put it in the
	// app bar. It shows nothing without a started `createAuth()` or without
	// an auth: block on the controller.
	//
	// Signed in: the user (roles on hover), whether the password counts for
	// the elevated permissions right now and until when, and Sign out.
	// Not signed in, on a controller that offers it: Sign in.
	import { currentAuth } from '../auth.svelte.js';
	import { passwordState, signInHref } from '../auth.js';
	import { whoIs } from '../areas.js';
	import Button from './Button.svelte';
	import Icon from './Icon.svelte';

	const auth = $derived(currentAuth());
	const me = $derived(auth?.me);
	const pw = $derived(auth ? passwordState(me, auth.clock, auth.skew) : null);
	let error = $state('');

	function hhmm(ms: number): string {
		return new Date(ms).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
	}

	async function signOut() {
		error = (await auth?.signOut()) ?? '';
	}
</script>

{#if auth && me?.auth}
	<div class="acct">
		{#if auth.user}
			<span class="who" title={`Roles: ${me.roles.join(', ') || 'none'}`}>
				<Icon name="user" size={14} />{auth.user}
			</span>
			{#if pw?.held}
				<span
					class="pw"
					class:fresh={pw.fresh}
					title={pw.fresh
						? 'Your password counts for the elevated actions until then'
						: 'An elevated action will ask for your password first'}
				>
					<Icon name={pw.fresh ? 'lock-open' : 'lock-closed'} size={12} />
					{pw.fresh ? `until ${hhmm(pw.untilLocal)}` : 'asks for password'}
				</span>
			{/if}
			<Button size="sm" variant="ghost" onclick={signOut}>Sign out</Button>
		{:else if me.signIn}
			<a class="signin" href={signInHref(globalThis.location)}>Sign in</a>
		{:else}
			<span class="who">{whoIs(me)}</span>
		{/if}
		{#if error}<span class="err" role="alert">{error}</span>{/if}
	</div>
{/if}

<style>
	.acct {
		display: inline-flex;
		align-items: center;
		gap: 8px;
		font-size: var(--font-xs);
		color: var(--ink-2);
		min-width: 0;
	}
	.who {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.pw {
		display: inline-flex;
		align-items: center;
		gap: 3px;
		font-size: var(--font-2xs);
		color: var(--muted);
		white-space: nowrap;
	}
	.pw.fresh {
		color: var(--good);
	}
	.signin {
		color: var(--accent);
		text-decoration: none;
	}
	.signin:hover {
		text-decoration: underline;
	}
	.err {
		color: var(--crit);
		font-size: var(--font-2xs);
	}
</style>
