<script lang="ts">
	import { enhance } from '$app/forms';
	import { resolve } from '$app/paths';
	import LoaderCircle from '@lucide/svelte/icons/loader-circle';
	import ShieldCheck from '@lucide/svelte/icons/shield-check';
	import type { ActionData } from './$types';

	let { form }: { form: ActionData } = $props();
	let submitting = $state(false);
	let emailInput = $state<HTMLInputElement | null>(null);
	let codeInput = $state<HTMLInputElement | null>(null);

	const totpStep = $derived(form?.step === 'totp');

	$effect(() => {
		if (totpStep) {
			codeInput?.focus();
		} else {
			emailInput?.focus();
		}
	});

	const inputClass =
		'h-10 w-full rounded-md border border-border-default bg-surface-base px-3 text-[14px] text-text-primary transition-colors placeholder:text-text-muted focus:border-accent focus:bg-surface-overlay focus:outline-none';

	const submitEnhance = () => {
		submitting = true;
		return async ({ update }: { update: () => Promise<void> }) => {
			await update();
			submitting = false;
		};
	};
</script>

<svelte:head>
	<title>Sign in — skali</title>
</svelte:head>

<div class="rounded-xl border border-border-default bg-surface-raised p-7 shadow-2xl">
	{#if totpStep}
		<div class="flex items-center gap-2.5">
			<ShieldCheck size={20} strokeWidth={1.75} class="text-accent" />
			<h1 class="text-[20px] font-semibold text-text-primary">Two-factor code</h1>
		</div>
		<p class="mt-1 text-[13.5px] text-text-muted">
			Enter the 6-digit code from your authenticator app, or one of your backup codes.
		</p>

		<form
			method="post"
			action="?/verify"
			use:enhance={submitEnhance}
			class="mt-6 flex flex-col gap-4"
		>
			<input type="hidden" name="challenge_token" value={form?.challengeToken ?? ''} />
			<label class="flex flex-col gap-1.5">
				<span class="text-[13px] font-medium text-text-secondary">Code</span>
				<input
					bind:this={codeInput}
					type="text"
					name="code"
					required
					inputmode="numeric"
					autocomplete="one-time-code"
					spellcheck="false"
					placeholder="123456"
					class="{inputClass} tracking-[0.3em]"
				/>
			</label>

			{#if form?.message}
				<div
					class="rounded-md border border-status-danger/40 bg-status-danger/10 px-3 py-2 text-[13px] text-status-danger"
				>
					{form.message}
				</div>
			{/if}

			<button
				type="submit"
				disabled={submitting}
				class="mt-2 inline-flex h-10 cursor-pointer items-center justify-center gap-2 rounded-md bg-accent text-[14px] font-semibold text-white transition-colors hover:bg-accent-hover disabled:cursor-default disabled:opacity-70"
			>
				{#if submitting}
					<LoaderCircle size={16} strokeWidth={2.25} class="animate-spin" />
					<span>Verifying…</span>
				{:else}
					<span>Verify</span>
				{/if}
			</button>
		</form>

		<p class="mt-5 text-center text-[13px] text-text-secondary">
			<a
				href={resolve('/auth/login')}
				data-sveltekit-reload
				class="font-medium text-accent hover:text-accent-hover"
			>
				Back to sign in
			</a>
		</p>
	{:else}
		<h1 class="text-[20px] font-semibold text-text-primary">Sign in</h1>
		<p class="mt-1 text-[13.5px] text-text-muted">Welcome back — sign in to your cluster.</p>

		<form
			method="post"
			action="?/login"
			use:enhance={submitEnhance}
			class="mt-6 flex flex-col gap-4"
		>
			<label class="flex flex-col gap-1.5">
				<span class="text-[13px] font-medium text-text-secondary">Email</span>
				<input
					bind:this={emailInput}
					type="email"
					name="email"
					required
					autocomplete="username"
					spellcheck="false"
					value={form?.email ?? ''}
					placeholder="you@example.com"
					class={inputClass}
				/>
			</label>

			<label class="flex flex-col gap-1.5">
				<span class="text-[13px] font-medium text-text-secondary">Password</span>
				<input
					type="password"
					name="password"
					required
					autocomplete="current-password"
					placeholder="••••••••"
					class={inputClass}
				/>
			</label>

			{#if form?.message}
				<div
					class="rounded-md border border-status-danger/40 bg-status-danger/10 px-3 py-2 text-[13px] text-status-danger"
				>
					{form.message}
				</div>
			{/if}

			<button
				type="submit"
				disabled={submitting}
				class="mt-2 inline-flex h-10 cursor-pointer items-center justify-center gap-2 rounded-md bg-accent text-[14px] font-semibold text-white transition-colors hover:bg-accent-hover disabled:cursor-default disabled:opacity-70"
			>
				{#if submitting}
					<LoaderCircle size={16} strokeWidth={2.25} class="animate-spin" />
					<span>Signing in…</span>
				{:else}
					<span>Sign in</span>
				{/if}
			</button>
		</form>
	{/if}
</div>
