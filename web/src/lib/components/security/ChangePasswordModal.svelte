<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'Change password'
	} satisfies ModalOptions;
</script>

<script lang="ts">
	import { api, ApiError } from '$lib/api/client';
	import { toast } from '$lib/stores/toast.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import ModalHeader from '$lib/components/ui/ModalHeader.svelte';

	let { close }: { close: (changed?: boolean) => void } = $props();

	let current = $state('');
	let next = $state('');
	let confirmNext = $state('');
	let busy = $state(false);

	const mismatch = $derived(confirmNext !== '' && next !== confirmNext);
	const valid = $derived(current !== '' && next.length >= 8 && next === confirmNext);

	const inputClass =
		'w-full rounded-[11px] border border-border-strong bg-surface-input px-3.25 py-2.75 text-lg text-text-primary transition-colors focus:border-accent/50 focus:outline-none';

	// The old password is required even inside sudo mode (it proves knowledge
	// of the secret being replaced). If the session is additionally stale, the
	// submit 403s and the reauth interceptor takes over before the replay —
	// this modal gets replaced by the ReauthModal, but the typed values live on
	// in the request closure, so the change still completes. Cancelling the
	// reauth drops the form input; that is the accepted GitHub-style tradeoff.
	async function change() {
		if (!valid || busy) return;
		busy = true;
		try {
			await api.post('/v1/auth/password', { current_password: current, new_password: next });
			toast.success('Password changed', {
				description: 'All other sessions were signed out.'
			});
			close(true);
		} catch (err) {
			toast.error(err instanceof ApiError ? err.message : 'Could not change the password');
			busy = false;
		}
	}
</script>

<ModalHeader
	title="Change password"
	description="Every other session is signed out immediately; only this one survives."
/>

<form
	class="flex flex-col gap-3.5 px-5.5 py-4"
	onsubmit={(e) => {
		e.preventDefault();
		change();
	}}
>
	<label class="flex flex-col gap-1.5">
		<span class="text-text-tertiary text-base font-medium">Old password</span>
		<input
			bind:value={current}
			type="password"
			required
			autocomplete="current-password"
			placeholder="••••••••••"
			class={inputClass}
		/>
	</label>
	<label class="flex flex-col gap-1.5">
		<span class="text-text-tertiary text-base font-medium">New password</span>
		<input
			bind:value={next}
			type="password"
			required
			minlength={8}
			autocomplete="new-password"
			placeholder="at least 8 characters"
			class={inputClass}
		/>
	</label>
	<label class="flex flex-col gap-1.5">
		<span class="text-text-tertiary text-base font-medium">Confirm new password</span>
		<input
			bind:value={confirmNext}
			type="password"
			required
			autocomplete="new-password"
			placeholder="repeat the new password"
			class={inputClass}
		/>
	</label>

	{#if mismatch}
		<div
			class="border-status-danger/40 bg-status-danger/10 text-status-danger rounded-[11px] border px-3 py-2 text-base"
		>
			The new passwords do not match.
		</div>
	{/if}

	<button type="submit" class="hidden" aria-hidden="true"></button>
</form>

<div class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3">
	<Button variant="ghost" onclick={() => close(false)}>Cancel</Button>
	<Button variant="primary" disabled={!valid} {busy} onclick={change}>Update password</Button>
</div>
