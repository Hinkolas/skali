<script lang="ts">
	import type { Snippet } from 'svelte';

	let {
		title,
		titleTrailing,
		subtitle,
		actions
	}: {
		title: string;
		/** Rendered next to the title, e.g. a status pill. */
		titleTrailing?: Snippet;
		subtitle?: Snippet;
		actions?: Snippet;
	} = $props();
</script>

<!-- One wrapping row: the actions sit on the title's right while they fit
     and drop underneath, left-aligned, when the pane is too narrow (phones,
     a tablet pane beside the sidebar) instead of pushing past its edge. -->
<div class="mb-5.5 flex flex-wrap items-start gap-x-6 gap-y-3">
	<div class="min-w-0">
		<div class="flex items-center gap-3">
			<h1 class="text-text-primary text-5xl font-semibold tracking-[-0.02em]">{title}</h1>
			{#if titleTrailing}
				{@render titleTrailing()}
			{/if}
		</div>
		{#if subtitle}
			<div class="text-text-muted mt-1.5 flex items-center gap-2 text-base">
				{@render subtitle()}
			</div>
		{/if}
	</div>
	<div class="flex-1"></div>
	{#if actions}
		<div class="flex flex-wrap items-center gap-2.5">
			{@render actions()}
		</div>
	{/if}
</div>
