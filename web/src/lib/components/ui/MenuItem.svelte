<script lang="ts">
	import { getContext, type Snippet } from 'svelte';
	import Check from '@lucide/svelte/icons/check';
	import type { NavIcon } from '$lib/navigation';

	let {
		href,
		icon: Icon,
		selected = false,
		danger = false,
		onselect,
		children
	}: {
		/** Pre-resolved href; renders an anchor instead of a button. */
		href?: string;
		icon?: NavIcon;
		/** Marks the current choice with a trailing check (switcher menus). */
		selected?: boolean;
		danger?: boolean;
		onselect?: () => void;
		children: Snippet;
	} = $props();

	const menu = getContext<{ close: () => void } | undefined>('menu');

	function activate() {
		menu?.close();
		onselect?.();
	}

	const itemClass = $derived(
		`flex cursor-pointer items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-[12.5px] font-medium transition-colors text-text-secondary hover:bg-white/5 ${
			danger ? 'hover:text-status-danger' : 'hover:text-text-primary'
		}`
	);
</script>

<!-- eslint-disable svelte/no-navigation-without-resolve -- pass-through: callers hand in resolved hrefs -->
{#if href}
	<a {href} role="menuitem" onclick={activate} class={itemClass}>
		{#if Icon}
			<Icon size={14} class="text-text-ghost flex-none" />
		{/if}
		{@render children()}
		{#if selected}
			<Check size={13} class="text-accent ml-auto flex-none" />
		{/if}
	</a>
{:else}
	<button type="button" role="menuitem" onclick={activate} class={itemClass}>
		{#if Icon}
			<Icon size={14} class="text-text-ghost flex-none" />
		{/if}
		{@render children()}
		{#if selected}
			<Check size={13} class="text-accent ml-auto flex-none" />
		{/if}
	</button>
{/if}
