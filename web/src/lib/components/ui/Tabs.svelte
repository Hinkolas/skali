<script lang="ts" module>
	import type { Component } from 'svelte';
	import type { IconProps } from '@lucide/svelte';

	export type TabDef = {
		id: string;
		label: string;
		icon?: Component<IconProps, object, ''>;
	};
</script>

<script lang="ts">
	import { tick } from 'svelte';

	// Segmented tab switcher (sidepanel headers, card sections). Controlled:
	// the host owns `active` and decides what each tab shows — this renders
	// the tablist only, so panes can live anywhere (e.g. inside a scroll
	// region the tabs themselves stay out of).
	let {
		tabs,
		active,
		onchange,
		label
	}: {
		tabs: TabDef[];
		active: string;
		onchange: (id: string) => void;
		/** aria-label for the tablist. */
		label: string;
	} = $props();

	let listEl: HTMLDivElement;

	// Roving tabindex: Tab lands on the active segment, arrows move+activate.
	async function onkeydown(e: KeyboardEvent) {
		const idx = tabs.findIndex((t) => t.id === active);
		let next: number;
		if (e.key === 'ArrowRight') next = (idx + 1) % tabs.length;
		else if (e.key === 'ArrowLeft') next = (idx - 1 + tabs.length) % tabs.length;
		else if (e.key === 'Home') next = 0;
		else if (e.key === 'End') next = tabs.length - 1;
		else return;
		e.preventDefault();
		onchange(tabs[next].id);
		await tick();
		listEl.querySelector<HTMLButtonElement>('[aria-selected="true"]')?.focus();
	}
</script>

<div
	bind:this={listEl}
	role="tablist"
	aria-label={label}
	class="grid w-full auto-cols-fr grid-flow-col gap-0.5 rounded-[11px] bg-white/3 p-0.5"
>
	{#each tabs as t (t.id)}
		{@const isActive = t.id === active}
		<button
			type="button"
			role="tab"
			aria-selected={isActive}
			tabindex={isActive ? 0 : -1}
			{onkeydown}
			onclick={() => onchange(t.id)}
			class="flex cursor-pointer items-center justify-center gap-1.5 rounded-lg px-2 py-1.5 text-sm transition-colors {isActive
				? 'bg-accent/10 inset-ring inset-ring-accent/25 text-accent-nav font-medium'
				: 'text-text-faint hover:text-text-secondary hover:bg-white/4'}"
		>
			{#if t.icon}
				<t.icon size={14} strokeWidth={1.75} class="flex-none opacity-90" />
			{/if}
			{t.label}
		</button>
	{/each}
</div>
