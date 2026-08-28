<script lang="ts">
	import { enhance } from '$app/forms';
	import { resolve } from '$app/paths';
	import LoaderCircle from '@lucide/svelte/icons/loader-circle';
	import ShieldCheck from '@lucide/svelte/icons/shield-check';
	import type { ActionData, PageData } from './$types';

	let { form, data }: { form: ActionData; data: PageData } = $props();
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
		'w-full rounded-[11px] border border-border-strong bg-surface-input px-3.25 py-2.75 text-lg text-text-primary transition-colors focus:border-accent/50 focus:outline-none';

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

<div class="bg-surface-raised border-border-raised flex flex-col gap-3.5 rounded-2xl border p-6.5">
	{#if totpStep}
		<div class="flex items-center gap-2.5">
			<ShieldCheck size={22} strokeWidth={1.75} class="text-accent-light" />
			<h2 class="text-text-primary text-xl font-semibold">Two-factor code</h2>
		</div>
		<p class="text-text-muted -mt-1.5 text-base">
			Enter the 6-digit code from your authenticator app, or one of your backup codes.
		</p>

		<form method="post" action="?/verify" use:enhance={submitEnhance} class="flex flex-col gap-3.5">
			<input type="hidden" name="challenge_token" value={form?.challengeToken ?? ''} />
			<input type="hidden" name="next" value={data.next} />
			<label class="flex flex-col gap-1.5">
				<span class="text-text-tertiary text-base font-medium">Code</span>
				<input
					bind:this={codeInput}
					type="text"
					name="code"
					required
					inputmode="numeric"
					autocomplete="one-time-code"
					spellcheck="false"
					placeholder="123456"
					class="{inputClass} font-mono tracking-[0.3em]"
				/>
			</label>

			{#if form?.message}
				<div
					class="border-status-danger/40 bg-status-danger/10 text-status-danger rounded-[11px] border px-3 py-2 text-base"
				>
					{form.message}
				</div>
			{/if}

			<button
				type="submit"
				disabled={submitting}
				class="from-accent-from to-accent-to text-surface-base shadow-glow mt-1 inline-flex cursor-pointer items-center justify-center gap-2 rounded-[11px] bg-linear-135 p-3 text-lg font-semibold transition-[filter] hover:brightness-108 disabled:cursor-default disabled:opacity-70"
			>
				{#if submitting}
					<LoaderCircle size={18} strokeWidth={2.25} class="animate-spin" />
					<span>Verifying…</span>
				{:else}
					<span>Verify</span>
				{/if}
			</button>
		</form>

		<p class="text-center text-base">
			<a
				href={resolve('/auth/login')}
				data-sveltekit-reload
				class="text-text-faint hover:text-text-secondary font-medium transition-colors"
			>
				Back to sign in
			</a>
		</p>
	{:else}
		<form method="post" action="?/login" use:enhance={submitEnhance} class="flex flex-col gap-3.5">
			<input type="hidden" name="next" value={data.next} />
			<label class="flex flex-col gap-1.5">
				<span class="text-text-tertiary text-base font-medium">Email</span>
				<input
					bind:this={emailInput}
					type="email"
					name="email"
					required
					autocomplete="username"
					spellcheck="false"
					value={form?.email ?? ''}
					placeholder="you@company.dev"
					class={inputClass}
				/>
			</label>

			<label class="flex flex-col gap-1.5">
				<span class="text-text-tertiary text-base font-medium">Password</span>
				<input
					type="password"
					name="password"
					required
					autocomplete="current-password"
					placeholder="••••••••••"
					class={inputClass}
				/>
			</label>

			{#if form?.message}
				<div
					class="border-status-danger/40 bg-status-danger/10 text-status-danger rounded-[11px] border px-3 py-2 text-base"
				>
					{form.message}
				</div>
			{/if}

			<button
				type="submit"
				disabled={submitting}
				class="from-accent-from to-accent-to text-surface-base shadow-glow mt-1 inline-flex cursor-pointer items-center justify-center gap-2 rounded-[11px] bg-linear-135 p-3 text-lg font-semibold transition-[filter] hover:brightness-108 disabled:cursor-default disabled:opacity-70"
			>
				{#if submitting}
					<LoaderCircle size={18} strokeWidth={2.25} class="animate-spin" />
					<span>Signing in…</span>
				{:else}
					<span>Sign in</span>
				{/if}
			</button>
		</form>
	{/if}
</div>
