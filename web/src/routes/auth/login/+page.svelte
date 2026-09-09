<script lang="ts">
	import { resolve } from '$app/paths';
	import LoaderCircle from '@lucide/svelte/icons/loader-circle';
	import ShieldCheck from '@lucide/svelte/icons/shield-check';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();
	let form = $state<{ step?: 'totp'; challengeToken?: string; email?: string; message?: string }>(
		{}
	);
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

	async function submit(event: SubmitEvent) {
		event.preventDefault();
		if (submitting) return;
		submitting = true;
		const fields = new FormData(event.currentTarget as HTMLFormElement);
		const verifying = totpStep;
		const email = String(fields.get('email') ?? '').trim();
		try {
			const response = await fetch('/api/v1/auth/' + (verifying ? '2fa/verify' : 'login'), {
				method: 'POST',
				credentials: 'same-origin',
				headers: { 'content-type': 'application/json', 'X-Requested-With': 'skali' },
				body: JSON.stringify(
					verifying
						? {
								session_transport: 'cookie',
								challenge_token: form.challengeToken,
								code: String(fields.get('code') ?? '').trim()
							}
						: { session_transport: 'cookie', email, password: String(fields.get('password') ?? '') }
				)
			});
			const body = await response.json().catch(() => null);
			if (!response.ok) {
				const code = body?.error?.code;
				const messages: Record<string, string> = {
					invalid_credentials: 'Wrong email or password.',
					invalid_code: 'That code is not valid.',
					invalid_token: 'The code expired — sign in again.',
					rate_limited: 'Too many attempts. Wait a moment and try again.'
				};
				form = {
					...form,
					email: verifying ? form.email : email,
					...(code === 'invalid_token' ? { step: undefined, challengeToken: undefined } : {}),
					message:
						messages[code] ??
						(response.status >= 500
							? 'The server is unreachable. Try again in a moment.'
							: (body?.error?.message ?? 'Something went wrong.'))
				};
			} else if (body?.challenge) {
				form = { step: 'totp', challengeToken: body.challenge.token, email };
			} else {
				window.location.assign(data.next);
			}
		} catch {
			form = { ...form, message: 'The server is unreachable. Try again in a moment.' };
		} finally {
			submitting = false;
		}
	}
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

		<form onsubmit={submit} class="flex flex-col gap-3.5">
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
		<form onsubmit={submit} class="flex flex-col gap-3.5">
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
