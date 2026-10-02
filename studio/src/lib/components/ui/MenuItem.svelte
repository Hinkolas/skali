<script lang="ts">
	import { getContext, type Snippet } from 'svelte';
	import Check from '@lucide/svelte/icons/check';
	import type { NavIcon } from '$lib/navigation';

	let {
		href,
		icon: Icon,
		selected = false,
		danger = false,
		disabled = false,
		title,
		onselect,
		children
	}: {
		/** Pre-resolved href; renders an anchor instead of a button. */
		href?: string;
		icon?: NavIcon;
		/** Marks the current choice with a trailing check (switcher menus). */
		selected?: boolean;
		danger?: boolean;
		/** Shown but inert (locked environments, missing permission); title explains. */
		disabled?: boolean;
		title?: string;
		onselect?: () => void;
		children: Snippet;
	} = $props();

	const menu = getContext<{ close: () => void } | undefined>('menu');

	function activate(e: MouseEvent) {
		if (disabled) {
			e.preventDefault();
			return;
		}
		menu?.close();
		onselect?.();
	}

	const itemClass = $derived(
		`flex items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-base font-medium transition-colors focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent/70 ${
			disabled
				? 'cursor-default text-text-ghost'
				: `cursor-pointer text-text-secondary hover:bg-white/5 ${
						danger ? 'hover:text-status-danger' : 'hover:text-text-primary'
					}`
		}`
	);
</script>

<!-- eslint-disable svelte/no-navigation-without-resolve -- pass-through: callers hand in resolved hrefs -->
{#if href}
	<a
		href={disabled ? undefined : href}
		role="menuitem"
		aria-disabled={disabled || undefined}
		{title}
		onclick={activate}
		class={itemClass}
	>
		{#if Icon}
			<Icon size={15} class="text-text-ghost flex-none" />
		{/if}
		{@render children()}
		{#if selected}
			<Check size={14} class="text-accent ml-auto flex-none" />
		{/if}
	</a>
{:else}
	<button
		type="button"
		role="menuitem"
		aria-disabled={disabled || undefined}
		{title}
		onclick={activate}
		class={itemClass}
	>
		{#if Icon}
			<Icon size={15} class="text-text-ghost flex-none" />
		{/if}
		{@render children()}
		{#if selected}
			<Check size={14} class="text-accent ml-auto flex-none" />
		{/if}
	</button>
{/if}
