<script lang="ts">
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import KeyRound from '@lucide/svelte/icons/key-round';
	import LogOut from '@lucide/svelte/icons/log-out';
	import { resolve } from '$app/paths';
	import { authState, signOut } from '$lib/stores/auth.svelte';
	import { dialog } from '$lib/stores/dialog.svelte';
	import Menu from '$lib/components/ui/Menu.svelte';
	import MenuItem from '$lib/components/ui/MenuItem.svelte';
	import MenuSeparator from '$lib/components/ui/MenuSeparator.svelte';

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

<Menu
	label="Account menu"
	align="end"
	panelClass="w-56"
	triggerClass="flex cursor-pointer items-center gap-1 rounded-full p-0.5 transition-colors hover:bg-white/4"
>
	{#snippet trigger({ open })}
		<span
			class="text-accent-nav grid size-7 flex-none place-items-center rounded-full bg-linear-135 from-[#37324e] to-[#232030] text-[11px] font-semibold"
		>
			{initials}
		</span>
		<ChevronDown
			size={13}
			class="text-text-ghost flex-none transition-transform {open ? 'rotate-180' : ''}"
		/>
	{/snippet}

	<div class="px-2.5 pt-2 pb-1.5">
		<div class="text-text-primary truncate text-[12.5px] font-semibold">
			{user?.name || 'Account'}
		</div>
		<div class="text-text-faint truncate text-[10.5px]">{user?.email}</div>
	</div>
	<MenuSeparator />
	<MenuItem href={resolve('/account')} icon={KeyRound}>Account security</MenuItem>
	<MenuItem danger icon={LogOut} onselect={confirmSignOut}>Sign out</MenuItem>
</Menu>
