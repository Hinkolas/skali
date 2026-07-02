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

<div class="mb-5.5 flex items-start">
	<div>
		<div class="flex items-center gap-3">
			<h1 class="text-text-primary text-[26px] font-semibold tracking-[-0.02em]">{title}</h1>
			{#if titleTrailing}
				{@render titleTrailing()}
			{/if}
		</div>
		{#if subtitle}
			<div class="text-text-muted mt-1.5 flex items-center gap-2 text-[13px]">
				{@render subtitle()}
			</div>
		{/if}
	</div>
	<div class="flex-1"></div>
	{#if actions}
		<div class="flex items-center gap-2.5">
			{@render actions()}
		</div>
	{/if}
</div>
