<script lang="ts">
	import { resolve } from '$app/paths';
	import TerminalSquare from '@lucide/svelte/icons/terminal-square';
	import ShieldCheck from '@lucide/svelte/icons/shield-check';
	import { api, ApiError } from '$lib/api/client';
	import { setUser } from '$lib/stores/auth.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	// The reauth checkpoint decides password versus code from the auth
	// store, which the app shell normally hydrates; this page lives outside
	// the shell, so hydrate it here.
	$effect(() => {
		setUser(data.user);
	});

	// svelte-ignore state_referenced_locally
	let code = $state(data.code);
	let busy = $state(false);
	let message = $state('');
	let outcome = $state<'approved' | 'denied' | null>(null);

	const info = $derived(data.info);
	const reauth = $derived(info?.intent === 'reauth');
	const foreign = $derived(reauth && info !== null && !info.mine);

	async function decide(action: 'approve' | 'deny') {
		if (busy || !info) return;
		busy = true;
		message = '';
		try {
			await api.post(`/v1/auth/device/codes/${encodeURIComponent(data.code)}/${action}`);
			outcome = action === 'approve' ? 'approved' : 'denied';
		} catch (err) {
			// A cancelled reauth checkpoint surfaces as reauth_required: no
			// decision was made, say nothing alarming.
			if (err instanceof ApiError && err.code === 'reauth_required') {
				message = '';
			} else {
				message = err instanceof ApiError ? err.message : 'Something went wrong.';
			}
		} finally {
			busy = false;
		}
	}

	const inputClass =
		'w-full rounded-[11px] border border-border-strong bg-surface-input px-3.25 py-2.75 text-center font-mono text-lg tracking-[0.3em] uppercase text-text-primary transition-colors focus:border-accent/50 focus:outline-none';
</script>

<svelte:head>
	<title>Authorize the CLI - skali</title>
</svelte:head>

<div class="bg-surface-raised border-border-raised flex flex-col gap-3.5 rounded-2xl border p-6.5">
	{#if outcome === 'approved'}
		<div class="flex flex-col items-center gap-2.5 text-center">
			<ShieldCheck size={26} strokeWidth={1.75} class="text-status-ok" />
			<h2 class="text-text-primary text-xl font-semibold">Approved</h2>
			<p class="text-text-muted text-base">
				{reauth ? 'The command continues in your terminal.' : 'You are signed in on the terminal.'}
				You can close this tab.
			</p>
		</div>
	{:else if outcome === 'denied'}
		<div class="flex flex-col items-center gap-2.5 text-center">
			<h2 class="text-text-primary text-xl font-semibold">Denied</h2>
			<p class="text-text-muted text-base">
				The terminal was told no. If this was not you, consider changing your password.
			</p>
		</div>
	{:else if info}
		<div class="flex items-center gap-2.5">
			<TerminalSquare size={22} strokeWidth={1.75} class="text-accent-light" />
			<h2 class="text-text-primary text-xl font-semibold">
				{reauth ? 'Confirm a command' : 'Sign in the CLI'}
			</h2>
		</div>
		<p class="text-text-muted -mt-1.5 text-base leading-relaxed">
			{#if reauth}
				<span class="text-text-primary font-medium">{info.client_label}</span> is running a command that
				needs recent authentication. Approving confirms it for your terminal session.
			{:else}
				This signs in
				<span class="text-text-primary font-medium">{info.client_label}</span>
				as
				<span class="text-text-primary font-medium">{data.user.email}</span>.
			{/if}
		</p>
		<div
			class="border-border-default bg-surface-input flex items-center justify-between rounded-[11px] border px-3.25 py-2.5"
		>
			<span class="text-text-tertiary text-base">Code</span>
			<span class="text-text-primary font-mono text-lg tracking-[0.2em]"
				>{data.code.toUpperCase()}</span
			>
		</div>
		{#if foreign}
			<div
				class="border-status-danger/40 bg-status-danger/10 text-status-danger rounded-[11px] border px-3 py-2 text-base"
			>
				This request belongs to another account. Only the account that started it can approve it.
			</div>
		{/if}
		{#if message}
			<div
				class="border-status-danger/40 bg-status-danger/10 text-status-danger rounded-[11px] border px-3 py-2 text-base"
				role="alert"
			>
				{message}
			</div>
		{/if}
		<Button
			variant="primary"
			disabled={foreign}
			{busy}
			onclick={() => decide('approve')}
			class="mt-1 w-full"
		>
			Approve
		</Button>
		<Button variant="ghost" disabled={busy} onclick={() => decide('deny')} class="w-full">
			Deny
		</Button>
	{:else}
		<div class="flex items-center gap-2.5">
			<TerminalSquare size={22} strokeWidth={1.75} class="text-accent-light" />
			<h2 class="text-text-primary text-xl font-semibold">Enter the code</h2>
		</div>
		<p class="text-text-muted -mt-1.5 text-base">
			Type the code your terminal shows to sign it in or confirm a command.
		</p>
		<form method="get" action={resolve('/auth/device')} class="flex flex-col gap-3.5">
			<input
				bind:value={code}
				type="text"
				name="code"
				required
				autocomplete="off"
				spellcheck="false"
				placeholder="XXXX-XXXX"
				aria-label="Device code"
				class={inputClass}
			/>
			{#if data.notFound}
				<div
					class="border-status-danger/40 bg-status-danger/10 text-status-danger rounded-[11px] border px-3 py-2 text-base"
				>
					No pending request has that code. It may have expired; run the command again.
				</div>
			{/if}
			<Button type="submit" variant="primary" class="mt-1 w-full">Continue</Button>
		</form>
	{/if}
</div>
