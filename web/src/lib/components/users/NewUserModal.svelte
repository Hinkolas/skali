<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'New user'
	} satisfies ModalOptions;
</script>

<script lang="ts">
	import { api, ApiError } from '$lib/api/client';
	import { toast } from '$lib/stores/toast.svelte';
	import type { Role } from '$lib/types/auth';
	import Button from '$lib/components/ui/Button.svelte';
	import ModalHeader from '$lib/components/ui/ModalHeader.svelte';

	let { close }: { close: (created?: boolean) => void } = $props();

	let email = $state('');
	let name = $state('');
	let password = $state('');
	let role = $state<Role>('member');
	let busy = $state(false);

	const valid = $derived(email.trim() !== '' && password.length >= 8);

	async function create() {
		if (!valid || busy) return;
		busy = true;
		try {
			await api.post('/v1/users', {
				email: email.trim(),
				name: name.trim(),
				password,
				role
			});
			toast.success(`Created ${email.trim()}`, {
				description: 'Share the initial password with them securely.'
			});
			close(true);
		} catch (err) {
			toast.error(err instanceof ApiError ? err.message : 'Could not create the user');
			busy = false;
		}
	}
</script>

<ModalHeader
	title="New user"
	description="There is no self-service signup: you set the initial password and hand it over."
/>

<form
	class="flex flex-col gap-3.5 px-5.5 py-4"
	onsubmit={(e) => {
		e.preventDefault();
		create();
	}}
>
	<label class="flex flex-col gap-1.5">
		<span class="text-text-tertiary text-[12.5px] font-medium">Email</span>
		<input
			bind:value={email}
			type="email"
			required
			placeholder="dev@example.com"
			class="border-border-strong bg-surface-input text-text-primary focus:border-accent/50 w-full rounded-[10px] border px-3.25 py-2.75 text-[13.5px] transition-colors focus:outline-none"
		/>
	</label>
	<label class="flex flex-col gap-1.5">
		<span class="text-text-tertiary text-[12.5px] font-medium">Name</span>
		<input
			bind:value={name}
			type="text"
			placeholder="Ada Lovelace"
			class="border-border-strong bg-surface-input text-text-primary focus:border-accent/50 w-full rounded-[10px] border px-3.25 py-2.75 text-[13.5px] transition-colors focus:outline-none"
		/>
	</label>
	<label class="flex flex-col gap-1.5">
		<span class="text-text-tertiary text-[12.5px] font-medium">Initial password</span>
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
	<div class="flex flex-col gap-1.5">
		<span class="text-text-tertiary text-[12.5px] font-medium">Role</span>
		<div class="flex gap-2">
			{#each ['member', 'admin'] as const as r (r)}
				<button
					type="button"
					onclick={() => (role = r)}
					class="flex-1 cursor-pointer rounded-[10px] border px-3 py-2.5 text-[13px] font-medium transition-colors {role ===
					r
						? 'border-accent/50 bg-accent/10 text-accent-nav'
						: 'border-border-strong text-text-tertiary hover:bg-white/4'}"
				>
					{r}
				</button>
			{/each}
		</div>
		<p class="text-text-ghost text-[12px] leading-relaxed">
			Admins additionally manage users and instance settings.
		</p>
	</div>

	<!-- Hidden submit so Enter works; the visible buttons live in the footer. -->
	<button type="submit" class="hidden" aria-hidden="true"></button>
</form>

<div class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3">
	<Button variant="ghost" onclick={() => close(false)}>Cancel</Button>
	<Button variant="primary" disabled={!valid} {busy} onclick={create}>Create user</Button>
</div>
