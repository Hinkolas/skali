<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	// Not dismissable: Esc/backdrop on the backup-codes step would lose them
	// forever. Only the explicit buttons close this modal.
	export const modalOptions = {
		label: 'Set up two-factor authentication',
		dismissable: false
	} satisfies ModalOptions;
</script>

<script lang="ts">
	import { api, ApiError } from '$lib/api/client';
	import type { TwoFactorEnrollment } from '$lib/types/auth';
	import Button from '$lib/components/ui/Button.svelte';
	import CopyField from '$lib/components/ui/CopyField.svelte';
	import BackupCodes from '$lib/components/security/BackupCodes.svelte';

	let {
		enrollment,
		close
	}: { enrollment: TwoFactorEnrollment; close: (confirmed?: boolean) => void } = $props();

	let step = $state<'confirm' | 'codes'>('confirm');
	let code = $state('');
	let busy = $state(false);
	let message = $state('');
	let codeInput = $state<HTMLInputElement | null>(null);

	$effect(() => {
		if (step === 'confirm') codeInput?.focus();
	});

	async function confirm() {
		if (!code || busy) return;
		busy = true;
		message = '';
		try {
			// Not sudo-gated: the code itself proves possession of the secret.
			await api.post('/v1/auth/2fa/confirm', { code });
			step = 'codes';
		} catch (err) {
			message = err instanceof ApiError ? err.message : 'Could not confirm the code';
			code = '';
			codeInput?.focus();
		} finally {
			busy = false;
		}
	}
</script>

{#if step === 'confirm'}
	<div class="px-5.5 pt-5 pb-2">
		<h2 class="text-text-primary text-[16px] font-semibold tracking-tight">
			Set up two-factor authentication
		</h2>
		<p class="text-text-muted mt-1 text-[13px]">
			Add skali to your authenticator app with the secret below (or the otpauth URI), then confirm
			with the current 6-digit code. Abandoning this step leaves 2FA disabled.
		</p>
	</div>

	<form
		class="flex flex-col gap-3.5 px-5.5 py-4"
		onsubmit={(e) => {
			e.preventDefault();
			confirm();
		}}
	>
		<!-- No QR rendering on purpose: the app ships zero extra dependencies.
		     If wanted later, `uqr` generates an SVG from enrollment.otpauth_uri
		     in one call. -->
		<CopyField label="Secret" value={enrollment.secret} />
		<CopyField label="URI" value={enrollment.otpauth_uri} />

		<label class="mt-1 flex flex-col gap-1.5">
			<span class="text-text-tertiary text-[12.5px] font-medium">Code</span>
			<input
				bind:this={codeInput}
				bind:value={code}
				type="text"
				required
				inputmode="numeric"
				autocomplete="one-time-code"
				spellcheck="false"
				placeholder="123456"
				class="border-border-strong bg-surface-input text-text-primary focus:border-accent/50 w-full rounded-[10px] border px-3.25 py-2.75 font-mono text-[13.5px] tracking-[0.3em] transition-colors focus:outline-none"
			/>
		</label>

		{#if message}
			<div
				class="border-status-danger/40 bg-status-danger/10 text-status-danger rounded-[10px] border px-3 py-2 text-[13px]"
			>
				{message}
			</div>
		{/if}

		<button type="submit" class="hidden" aria-hidden="true"></button>
	</form>

	<div
		class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3"
	>
		<Button variant="ghost" onclick={() => close(false)}>Cancel</Button>
		<Button variant="primary" disabled={!code} {busy} onclick={confirm}>Confirm</Button>
	</div>
{:else}
	<div class="px-5.5 pt-5 pb-2">
		<h2 class="text-text-primary text-[16px] font-semibold tracking-tight">
			Save your backup codes
		</h2>
		<p class="text-text-muted mt-1 text-[13px]">
			Two-factor authentication is now enabled. Store these codes somewhere safe before closing —
			this is the only time they are shown.
		</p>
	</div>

	<div class="px-5.5 py-4">
		<BackupCodes codes={enrollment.backup_codes} />
	</div>

	<div
		class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3"
	>
		<Button variant="primary" onclick={() => close(true)}>I saved my backup codes</Button>
	</div>
{/if}
