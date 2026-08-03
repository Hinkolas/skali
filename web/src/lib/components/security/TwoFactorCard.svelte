<script lang="ts">
	import ShieldCheck from '@lucide/svelte/icons/shield-check';
	import ShieldOff from '@lucide/svelte/icons/shield-off';
	import { invalidateAll } from '$app/navigation';
	import { api, ApiError } from '$lib/api/client';
	import { modal } from '$lib/stores/modal.svelte';
	import { dialog } from '$lib/stores/dialog.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import type { TwoFactorEnrollment } from '$lib/types/auth';
	import Card from '$lib/components/ui/Card.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import TwoFactorEnrollModal, {
		modalOptions as enrollOptions
	} from '$lib/components/security/TwoFactorEnrollModal.svelte';
	import BackupCodesModal, {
		modalOptions as backupCodesOptions
	} from '$lib/components/security/BackupCodesModal.svelte';

	let { enabled }: { enabled: boolean } = $props();

	let busy = $state(false);

	// The gated calls below run while NO modal is open: the reauth interceptor
	// may need to show the ReauthModal, and the modal host holds a single slot.
	// Only after the API call resolves do we open a modal with its result, so
	// nothing that can never be re-fetched (backup codes) is ever displaced.

	async function enable() {
		if (busy) return;
		busy = true;
		try {
			const enrollment = await api.post<TwoFactorEnrollment>('/v1/auth/2fa/enable');
			busy = false;
			if (await modal.open<boolean>(TwoFactorEnrollModal, { enrollment }, enrollOptions).result) {
				await invalidateAll();
			}
		} catch (err) {
			busy = false;
			toast.error(err instanceof ApiError ? err.message : 'Could not start 2FA enrollment');
		}
	}

	async function disable() {
		const ok = await dialog.confirm({
			title: 'Disable two-factor authentication?',
			description:
				'Your account falls back to password-only sign in and all backup codes are deleted.',
			confirmLabel: 'Disable 2FA',
			variant: 'danger'
		});
		if (!ok) return;
		try {
			await api.post('/v1/auth/2fa/disable');
			toast.success('Two-factor authentication disabled');
			await invalidateAll();
		} catch (err) {
			toast.error(err instanceof ApiError ? err.message : 'Could not disable 2FA');
		}
	}

	async function regenerate() {
		const ok = await dialog.confirm({
			title: 'Regenerate backup codes?',
			description: 'All existing backup codes stop working immediately.',
			confirmLabel: 'Regenerate'
		});
		if (!ok) return;
		try {
			const { backup_codes } = await api.post<{ backup_codes: string[] }>(
				'/v1/auth/2fa/backup-codes'
			);
			modal.open(BackupCodesModal, { codes: backup_codes }, backupCodesOptions);
		} catch (err) {
			toast.error(err instanceof ApiError ? err.message : 'Could not regenerate backup codes');
		}
	}
</script>

<Card class="flex items-center gap-4 px-5.5 py-4.5">
	<div
		class="bg-surface-input text-text-tertiary grid size-9 flex-none place-items-center rounded-[11px]"
	>
		{#if enabled}
			<ShieldCheck size={18} strokeWidth={1.75} class="text-status-success" />
		{:else}
			<ShieldOff size={18} strokeWidth={1.75} />
		{/if}
	</div>
	<div class="min-w-0 flex-1">
		<h2 class="flex items-center gap-2 text-lg font-semibold tracking-tight">
			<span class="text-text-primary">Two-factor authentication</span>
			{#if enabled}
				<span
					class="font-mono bg-status-success/10 text-status-success rounded-full px-2 py-0.5 text-2xs"
				>
					enabled
				</span>
			{/if}
		</h2>
		<p class="text-text-muted mt-0.5 text-base">
			{#if enabled}
				Signing in requires a code from your authenticator app or a backup code.
			{:else}
				Protect sign-in with a time-based code from an authenticator app.
			{/if}
		</p>
	</div>
	{#if enabled}
		<Button variant="secondary" onclick={regenerate}>Regenerate backup codes</Button>
		<Button variant="danger" onclick={disable}>Disable</Button>
	{:else}
		<Button variant="primary" {busy} onclick={enable}>Enable 2FA</Button>
	{/if}
</Card>
