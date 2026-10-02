<script lang="ts">
	import type { Snippet } from 'svelte';
	import Card from '$lib/components/ui/Card.svelte';

	// One section of a configuration page: a titled card with an optional
	// one-line hint next to the title. `class` places it in the page grid.
	let {
		title,
		hint = '',
		class: className = '',
		children
	}: { title: string; hint?: string; class?: string; children: Snippet } = $props();
</script>

<Card class="flex flex-col p-5 {className}">
	<div class="mb-4 flex flex-wrap items-baseline gap-x-2.5 gap-y-0.5">
		<h3 class="text-text-primary text-xl font-semibold">{title}</h3>
		{#if hint}
			<span class="text-text-muted text-md">{hint}</span>
		{/if}
	</div>
	{@render children()}
</Card>
