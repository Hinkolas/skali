<script lang="ts">
	import type { StatCardData } from '$lib/models/view';
	import Card from './Card.svelte';
	import ProgressBar from './ProgressBar.svelte';
	import Sparkline from './Sparkline.svelte';
	import TrendChip from './TrendChip.svelte';

	let { stat }: { stat: StatCardData } = $props();

	// The preview slot holds one of: a sparkline (series data), a progress
	// bar (usage against a limit), or nothing. Chip and note sit below it
	// either way so the footer line lands at the same height across tiles.
	const hasSparkline = $derived((stat.sparkline?.length ?? 0) > 1);
	const hasFooter = $derived(Boolean(stat.chip || stat.note));
</script>

<Card class="px-4.5 py-4">
	<div class="text-text-ghost mb-3 text-xs font-semibold tracking-[0.12em] uppercase">
		{stat.label}
	</div>
	<div class="text-text-primary text-4xl font-semibold tracking-[-0.02em]">
		{stat.value}{#if stat.unit}<span class="text-text-muted ml-1 text-base font-medium"
				>{stat.unit}</span
			>{/if}
	</div>
	{#if hasSparkline}
		<Sparkline points={stat.sparkline!} class="mt-3" />
	{:else if stat.progress}
		<div class="mt-3 flex h-7 items-center">
			<div class="w-full">
				<ProgressBar pct={stat.progress.pct} class={stat.progress.class} />
			</div>
		</div>
	{/if}
	{#if hasFooter}
		<div class="mt-2.5 flex items-center gap-2">
			{#if stat.chip}
				<TrendChip text={stat.chip.text} tone={stat.chip.tone} />
			{/if}
			{#if stat.note}
				<span class="text-text-muted text-md">{stat.note}</span>
			{/if}
		</div>
	{/if}
</Card>
