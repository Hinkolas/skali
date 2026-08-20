<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'Confirm access',
		archetype: 'checkpoint'
	} satisfies ModalOptions;
</script>

<script lang="ts">
	import ShieldCheck from '@lucide/svelte/icons/shield-check';
	import { api, ApiError } from '$lib/api/client';
	import { authState } from '$lib/stores/auth.svelte';
	import Button from '$lib/components/ui/Button.svelte';

	// The sudo gate. A checkpoint, not a form: centered ceremony, one secret,
	// one button. A wrong secret shakes the input and stays.
	let { close }: { close: (ok?: boolean) => void } = $props();

	// 2FA users confirm with a TOTP/backup code; everyone else with their
	// password (mirrors the server's factor policy on /v1/auth/reauth).
	const twoFactor = $derived(authState.user?.two_factor_enabled ?? false);

	let secret = $state('');
	let busy = $state(false);
	let message = $state('');
	let attempt = $state(0);
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
			attempt += 1;
			secret = '';
			busy = false;
			input?.focus();
		}
	}
</script>

<div class="flex flex-col items-center px-6 pt-6 pb-3 text-center">
	<div
		class="border-accent/25 bg-accent/10 text-accent-light shadow-glow mb-3.5 flex size-11 items-center justify-center rounded-[13px] border"
	>
		<ShieldCheck size={22} strokeWidth={1.75} />
	</div>
	<h2 class="text-text-primary text-xl font-semibold tracking-tight">Confirm access</h2>
	<p class="text-text-muted mt-1.5 text-base leading-relaxed">
		{#if twoFactor}
			You're entering sudo mode. Enter the 6-digit code from your authenticator app, or a backup
			code (backup codes are spent when used).
		{:else}
			You're entering sudo mode. Enter your password to continue.
		{/if}
	</p>
</div>

<form
	class="flex flex-col gap-3 px-6 pt-1 pb-6"
	onsubmit={(e) => {
		e.preventDefault();
		confirm();
	}}
>
	{#key attempt}
		<div class={attempt > 0 ? 'checkpoint-shake' : ''}>
			{#if twoFactor}
				<input
					bind:this={input}
					bind:value={secret}
					type="text"
					required
					inputmode="numeric"
					autocomplete="one-time-code"
					spellcheck="false"
					placeholder="123456"
					aria-label="Authentication code"
					class="border-border-strong bg-surface-input text-text-primary focus:border-accent/50 focus:ring-3 focus:ring-accent/10 w-full rounded-[11px] border px-3.25 py-2.75 text-center font-mono text-lg tracking-[0.3em] transition-colors focus:outline-none"
				/>
			{:else}
				<input
					bind:this={input}
					bind:value={secret}
					type="password"
					required
					autocomplete="current-password"
					placeholder="••••••••••"
					aria-label="Password"
					class="border-border-strong bg-surface-input text-text-primary focus:border-accent/50 focus:ring-3 focus:ring-accent/10 w-full rounded-[11px] border px-3.25 py-2.75 text-lg transition-colors focus:outline-none"
				/>
			{/if}
		</div>
	{/key}

	{#if message}
		<p class="text-status-danger text-center text-md" role="alert">{message}</p>
	{/if}

	<Button type="submit" variant="primary" disabled={!secret} {busy} class="mt-1 w-full">
		Confirm
	</Button>
	<Button variant="ghost" onclick={() => close(false)} class="w-full">Cancel</Button>
</form>

<style>
	.checkpoint-shake {
		animation: checkpoint-shake 320ms cubic-bezier(0.36, 0.07, 0.19, 0.97);
	}
	@keyframes checkpoint-shake {
		20% {
			transform: translateX(-6px);
		}
		40% {
			transform: translateX(5px);
		}
		60% {
			transform: translateX(-3px);
		}
		80% {
			transform: translateX(2px);
		}
	}
</style>
