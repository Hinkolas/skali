<script lang="ts">
	import type { Snippet } from 'svelte';
	import Card from './Card.svelte';

	// Card-wrapped data table: tracked uppercase header + caller-rendered rows.
	// `grid` must be a literal grid-template class shared with the row markup,
	// e.g. 'grid-cols-[1.4fr_1.6fr_1fr_1fr_1fr_0.9fr]'.
	let {
		columns,
		grid,
		children
	}: {
		columns: string[];
		grid: string;
		children: Snippet;
	} = $props();
</script>

<Card class="overflow-hidden">
	<div
		class="border-border-subtle text-text-ghost grid border-b px-4.5 py-2.5 text-[10px] font-semibold tracking-[0.12em] uppercase {grid}"
	>
		<!-- Keyed by position: header labels are positional and may repeat
		     (e.g. two unlabeled spacer columns). -->
		{#each columns as column, i (i)}
			<div>{column}</div>
		{/each}
	</div>
	{@render children()}
</Card>
