<script lang="ts">
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import { authState, signOut } from '$lib/stores/auth.svelte';
	import { dialog } from '$lib/stores/dialog.svelte';

	const user = $derived(authState.user);

	const initials = $derived.by(() => {
		const name = user?.name?.trim() || user?.email || '?';
		const parts = name.split(/\s+/);
		return parts.length > 1
			? (parts[0][0] + parts[parts.length - 1][0]).toUpperCase()
			: name.slice(0, 2).toUpperCase();
	});

	function confirmSignOut() {
		dialog.confirm({
			title: 'Sign out?',
			description: 'You will need to sign in again to manage this cluster.',
			confirmLabel: 'Sign out',
			onConfirm: signOut
		});
	}
</script>

<button
	type="button"
	onclick={confirmSignOut}
	class="bg-surface-overlay border-border-default mx-3 mb-3 flex cursor-pointer items-center gap-2.5 rounded-xl border px-3 py-2.5 text-left transition-colors hover:border-white/14"
>
	<span
		class="text-accent-nav grid size-7 flex-none place-items-center rounded-full bg-linear-135 from-[#37324e] to-[#232030] text-[11px] font-semibold"
	>
		{initials}
	</span>
	<span class="flex min-w-0 flex-col gap-px">
		<span class="text-text-primary truncate text-[12.5px] font-semibold">
			{user?.name || 'Account'}
		</span>
		<span class="text-text-faint truncate text-[10.5px]">{user?.email}</span>
	</span>
	<ChevronDown size={13} class="text-text-ghost ml-auto flex-none" />
</button>
