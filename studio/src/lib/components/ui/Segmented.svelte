<script lang="ts" module>
	import type { Component } from 'svelte';
	import type { IconProps } from '@lucide/svelte';

	export type SegmentDef = {
		id: string;
		/** Visible text; an icon-only segment keeps its name in `title`. */
		label?: string;
		icon?: Component<IconProps, object, ''>;
		/** Trailing count, e.g. how many rows the segment would show. */
		count?: number;
		/** Tooltip and accessible name when there is no label. */
		title?: string;
	};
</script>

<script lang="ts">
	// Compact inline toggle group for page toolbars (a view switch, a
	// scope filter). Controlled like Tabs, but sized to its content and
	// exposed as pressed buttons: the segments change how a page shows one
	// set of data rather than which pane is visible.
	let {
		segments,
		value,
		onchange,
		label
	}: {
		segments: SegmentDef[];
		value: string;
		onchange: (id: string) => void;
		/** aria-label for the group. */
		label: string;
	} = $props();
</script>

<div
	role="group"
	aria-label={label}
	class="inline-flex flex-none items-center gap-0.5 rounded-[11px] bg-white/3 p-0.5"
>
	{#each segments as segment (segment.id)}
		{@const active = segment.id === value}
		<button
			type="button"
			aria-pressed={active}
			aria-label={segment.label ? undefined : segment.title}
			title={segment.title}
			onclick={() => onchange(segment.id)}
			class="flex h-7 cursor-pointer items-center justify-center gap-1.5 rounded-lg text-sm transition-colors {segment.label
				? 'px-2.5'
				: 'w-8'} {active
				? 'bg-accent/10 inset-ring inset-ring-accent/25 text-accent-nav font-medium'
				: 'text-text-faint hover:bg-white/4 hover:text-text-secondary'}"
		>
			{#if segment.icon}
				<segment.icon size={14} strokeWidth={1.75} class="flex-none opacity-90" />
			{/if}
			{#if segment.label}
				{segment.label}
			{/if}
			{#if segment.count != null}
				<span class="font-mono text-xs {active ? 'text-accent-nav/70' : 'text-text-ghost'}">
					{segment.count}
				</span>
			{/if}
		</button>
	{/each}
</div>
