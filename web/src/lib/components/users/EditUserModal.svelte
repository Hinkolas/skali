<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'Edit user'
	} satisfies ModalOptions;
</script>

<script lang="ts">
	import { api, ApiError } from '$lib/api/client';
	import { toast } from '$lib/stores/toast.svelte';
	import type { AuthUser, Role } from '$lib/types/auth';
	import Button from '$lib/components/ui/Button.svelte';

	let {
		user,
		self,
		close
	}: {
		user: AuthUser;
		/** Editing your own account: the role is locked (another admin must change it). */
		self: boolean;
		close: (updated?: boolean) => void;
	} = $props();

	// The modal host mounts this component fresh per open, so seeding the form
	// from the initial prop value is exactly right.
	// svelte-ignore state_referenced_locally
	let name = $state(user.name);
	// svelte-ignore state_referenced_locally
	let role = $state<Role>(user.role);
	let busy = $state(false);

	async function save() {
		if (busy) return;
		const patch: { name?: string; role?: Role } = {};
		if (name.trim() !== user.name) patch.name = name.trim();
		if (!self && role !== user.role) patch.role = role;
		if (Object.keys(patch).length === 0) {
			close(false);
			return;
		}
		busy = true;
		try {
			await api.patch(`/v1/users/${user.id}`, patch);
			toast.success(`Updated ${user.email}`);
			close(true);
		} catch (err) {
			toast.error(err instanceof ApiError ? err.message : 'Could not update the user');
			busy = false;
		}
	}
</script>

<div class="px-5.5 pt-5 pb-2">
	<h2 class="text-text-primary text-[16px] font-semibold tracking-tight">Edit user</h2>
	<p class="text-text-muted mt-1 text-[13px]">{user.email}</p>
</div>

<form
	class="flex flex-col gap-3.5 px-5.5 py-4"
	onsubmit={(e) => {
		e.preventDefault();
		save();
	}}
>
	<label class="flex flex-col gap-1.5">
		<span class="text-text-tertiary text-[12.5px] font-medium">Name</span>
		<input
			bind:value={name}
			type="text"
			placeholder="Ada Lovelace"
			class="border-border-strong bg-surface-input text-text-primary focus:border-accent/50 w-full rounded-[10px] border px-3.25 py-2.75 text-[13.5px] transition-colors focus:outline-none"
		/>
	</label>
	<div class="flex flex-col gap-1.5">
		<span class="text-text-tertiary text-[12.5px] font-medium">Role</span>
		<div class="flex gap-2">
			{#each ['member', 'admin'] as const as r (r)}
				<button
					type="button"
					disabled={self}
					onclick={() => (role = r)}
					class="flex-1 rounded-[10px] border px-3 py-2.5 text-[13px] font-medium transition-colors {role ===
					r
						? 'border-accent/50 bg-accent/10 text-accent-nav'
						: 'border-border-strong text-text-tertiary'} {self
						? 'cursor-default opacity-60'
						: 'cursor-pointer hover:bg-white/4'}"
				>
					{r}
				</button>
			{/each}
		</div>
		{#if self}
			<p class="text-text-ghost text-[11.5px]">
				You cannot change your own role — ask another admin.
			</p>
		{/if}
	</div>

	<button type="submit" class="hidden" aria-hidden="true"></button>
</form>

<div class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3">
	<Button variant="ghost" onclick={() => close(false)}>Cancel</Button>
	<Button variant="primary" {busy} onclick={save}>Save changes</Button>
</div>
