<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'Reset password'
	} satisfies ModalOptions;
</script>

<script lang="ts">
	import { api, ApiError } from '$lib/api/client';
	import { toast } from '$lib/stores/toast.svelte';
	import type { AuthUser } from '$lib/types/auth';
	import Button from '$lib/components/ui/Button.svelte';

	let { user, close }: { user: AuthUser; close: (reset?: boolean) => void } = $props();

	let password = $state('');
	let busy = $state(false);

	const valid = $derived(password.length >= 8);

	async function reset() {
		if (!valid || busy) return;
		busy = true;
		try {
			await api.post(`/v1/users/${user.id}/password`, { new_password: password });
			toast.success(`Password reset for ${user.email}`, {
				description: 'All of their sessions were signed out.'
			});
			close(true);
		} catch (err) {
			toast.error(err instanceof ApiError ? err.message : 'Could not reset the password');
			busy = false;
		}
	}
</script>

<div class="px-5.5 pt-5 pb-2">
	<h2 class="text-text-primary text-[16px] font-semibold tracking-tight">Reset password</h2>
	<p class="text-text-muted mt-1 text-[13px]">
		Sets a new password for <span class="text-text-secondary">{user.email}</span> and signs them out everywhere.
		This is the recovery path — there is no reset email.
	</p>
</div>

<form
	class="flex flex-col gap-3.5 px-5.5 py-4"
	onsubmit={(e) => {
		e.preventDefault();
		reset();
	}}
>
	<label class="flex flex-col gap-1.5">
		<span class="text-text-tertiary text-[12.5px] font-medium">New password</span>
		<input
			bind:value={password}
			type="password"
			required
			minlength={8}
			placeholder="at least 8 characters"
			autocomplete="new-password"
			class="border-border-strong bg-surface-input text-text-primary focus:border-accent/50 w-full rounded-[10px] border px-3.25 py-2.75 text-[13.5px] transition-colors focus:outline-none"
		/>
	</label>

	<button type="submit" class="hidden" aria-hidden="true"></button>
</form>

<div class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3">
	<Button variant="ghost" onclick={() => close(false)}>Cancel</Button>
	<Button variant="primary" disabled={!valid} {busy} onclick={reset}>Reset password</Button>
</div>
