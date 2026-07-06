<script lang="ts">
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import KeyRound from '@lucide/svelte/icons/key-round';
	import LogOut from '@lucide/svelte/icons/log-out';
	import { resolve } from '$app/paths';
	import { authState, signOut } from '$lib/stores/auth.svelte';
	import { dialog } from '$lib/stores/dialog.svelte';

	const user = $derived(authState.user);

	let open = $state(false);
	let root = $state<HTMLDivElement | null>(null);

	const initials = $derived.by(() => {
		const name = user?.name?.trim() || user?.email || '?';
		const parts = name.split(/\s+/);
		return parts.length > 1
			? (parts[0][0] + parts[parts.length - 1][0]).toUpperCase()
			: name.slice(0, 2).toUpperCase();
	});

	function confirmSignOut() {
		open = false;
		dialog.confirm({
			title: 'Sign out?',
			description: 'You will need to sign in again to manage this cluster.',
			confirmLabel: 'Sign out',
			onConfirm: signOut
		});
	}

	// Self-contained dropdown (no menu primitive exists yet): close on any
	// click outside the card or on Escape.
	function onWindowClick(e: MouseEvent) {
		if (open && root && !root.contains(e.target as Node)) open = false;
	}

	function onWindowKeydown(e: KeyboardEvent) {
		if (e.key === 'Escape') open = false;
	}

	const itemClass =
		'flex cursor-pointer items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-[12.5px] font-medium transition-colors';
</script>

<svelte:window onclick={onWindowClick} onkeydown={onWindowKeydown} />

<div bind:this={root} class="relative mx-3 mb-3">
	{#if open}
		<div
			class="bg-surface-overlay border-border-default absolute right-0 bottom-full left-0 z-10 mb-1.5 flex flex-col gap-0.5 rounded-xl border p-1.5 shadow-lg"
			role="menu"
			aria-label="Account menu"
		>
			<a
				href={resolve('/account')}
				onclick={() => (open = false)}
				role="menuitem"
				class="{itemClass} text-text-secondary hover:text-text-primary hover:bg-white/5"
			>
				<KeyRound size={14} class="text-text-ghost" />
				Account security
			</a>
			<button
				type="button"
				onclick={confirmSignOut}
				role="menuitem"
				class="{itemClass} text-text-secondary hover:text-status-danger hover:bg-white/5"
			>
				<LogOut size={14} class="text-text-ghost" />
				Sign out
			</button>
		</div>
	{/if}

	<button
		type="button"
		onclick={() => (open = !open)}
		aria-expanded={open}
		aria-haspopup="menu"
		class="bg-surface-overlay border-border-default flex w-full cursor-pointer items-center gap-2.5 rounded-xl border px-3 py-2.5 text-left transition-colors hover:border-white/14"
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
		<ChevronDown
			size={13}
			class="text-text-ghost ml-auto flex-none transition-transform {open ? 'rotate-180' : ''}"
		/>
	</button>
</div>
