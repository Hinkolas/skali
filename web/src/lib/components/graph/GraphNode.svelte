<script lang="ts">
	import type { GraphNodeData } from '$lib/mock/types';
	import StatusDot from '$lib/components/ui/StatusDot.svelte';
	import TrendChip from '$lib/components/ui/TrendChip.svelte';
	import TypeBadge from '$lib/components/ui/TypeBadge.svelte';

	let {
		node,
		selected = false,
		onselect
	}: {
		node: GraphNodeData;
		selected?: boolean;
		onselect: (slug: string) => void;
	} = $props();
</script>

<button
	type="button"
	onclick={() => onselect(node.slug)}
	class="bg-surface-node border-border-raised hover:border-accent/40 absolute z-3 flex cursor-pointer flex-col gap-2.5 rounded-[14px] border px-4 py-3.5 text-left transition-colors"
	style:left="{node.x}px"
	style:top="{node.y}px"
	style:width="{node.w}px"
>
	{#if selected}
		<div
			class="border-accent shadow-glow pointer-events-none absolute -inset-px rounded-[14px] border-[1.5px]"
		></div>
	{/if}
	<div class="flex w-full items-center gap-2.25">
		<TypeBadge kind={node.kind} form="tile" />
		<div class="min-w-0">
			<div class="text-text-primary truncate text-[13.5px] font-semibold">{node.title}</div>
			<div class="font-mono text-text-faint truncate text-[9.5px]">{node.subtitle}</div>
		</div>
		{#if node.status}
			<span class="ml-auto flex-none">
				<StatusDot status={node.status} />
			</span>
		{/if}
	</div>
	{#if node.mono}
		<div class="font-mono text-text-muted truncate text-[10.5px]">{node.mono}</div>
	{/if}
	<div class="flex gap-1.75">
		{#each node.chips as chip (chip.text)}
			<TrendChip text={chip.text} tone={chip.tone ?? 'neutral'} />
		{/each}
	</div>
</button>
