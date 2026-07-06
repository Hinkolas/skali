<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'Confirm access'
	} satisfies ModalOptions;
</script>

<script lang="ts">
	import ShieldCheck from '@lucide/svelte/icons/shield-check';
	import { api, ApiError } from '$lib/api/client';
	import { authState } from '$lib/stores/auth.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import ModalHeader from '$lib/components/ui/ModalHeader.svelte';

	let { close }: { close: (ok?: boolean) => void } = $props();

	// 2FA users confirm with a TOTP/backup code; everyone else with their
	// password (mirrors the server's factor policy on /v1/auth/reauth).
	const twoFactor = $derived(authState.user?.two_factor_enabled ?? false);

	let secret = $state('');
	let busy = $state(false);
	let message = $state('');
	let input = $state<HTMLInputElement | null>(null);

	$effect(() => {
		input?.focus();
	});

	async function confirm() {
		if (!secret || busy) return;
		busy = true;
		message = '';
		try {
			await api.post('/v1/auth/reauth', twoFactor ? { code: secret } : { password: secret });
			close(true);
		} catch (err) {
			message = err instanceof ApiError ? err.message : 'Could not confirm your identity';
			secret = '';
			busy = false;
			input?.focus();
		}
	}
</script>

<ModalHeader title="Confirm access" icon={ShieldCheck}>
	{#if twoFactor}
		You're entering sudo mode. Enter the 6-digit code from your authenticator app, or one of your
		backup codes (backup codes are spent when used).
	{:else}
		You're entering sudo mode. For security, enter your password to continue.
	{/if}
</ModalHeader>

<form
	class="flex flex-col gap-3.5 px-5.5 py-4"
	onsubmit={(e) => {
		e.preventDefault();
		confirm();
	}}
>
	{#if twoFactor}
		<label class="flex flex-col gap-1.5">
			<span class="text-text-tertiary text-[12.5px] font-medium">Code</span>
			<input
				bind:this={input}
				bind:value={secret}
				type="text"
				required
				inputmode="numeric"
				autocomplete="one-time-code"
				spellcheck="false"
				placeholder="123456"
				class="border-border-strong bg-surface-input text-text-primary focus:border-accent/50 w-full rounded-[10px] border px-3.25 py-2.75 font-mono text-[13.5px] tracking-[0.3em] transition-colors focus:outline-none"
			/>
		</label>
	{:else}
		<label class="flex flex-col gap-1.5">
			<span class="text-text-tertiary text-[12.5px] font-medium">Password</span>
			<input
				bind:this={input}
				bind:value={secret}
				type="password"
				required
				autocomplete="current-password"
				placeholder="••••••••••"
				class="border-border-strong bg-surface-input text-text-primary focus:border-accent/50 w-full rounded-[10px] border px-3.25 py-2.75 text-[13.5px] transition-colors focus:outline-none"
			/>
		</label>
	{/if}

	{#if message}
		<div
			class="border-status-danger/40 bg-status-danger/10 text-status-danger rounded-[10px] border px-3 py-2 text-[13px]"
		>
			{message}
		</div>
	{/if}

	<button type="submit" class="hidden" aria-hidden="true"></button>
</form>

<div class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3">
	<Button variant="ghost" onclick={() => close(false)}>Cancel</Button>
	<Button variant="primary" disabled={!secret} {busy} onclick={confirm}>Confirm</Button>
</div>
