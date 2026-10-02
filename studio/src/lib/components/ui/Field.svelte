<script lang="ts">
	// Label + control + optional description/error, the shared form row.
	import type { Snippet } from 'svelte';

	let {
		label,
		description = '',
		error = '',
		children
	}: {
		label: string;
		description?: string;
		error?: string;
		children: Snippet;
	} = $props();
</script>

<label class="flex flex-col gap-1.5">
	<span class="text-text-tertiary text-base font-medium">{label}</span>
	{@render children()}
	{#if error}
		<span class="text-status-danger text-base">{error}</span>
	{:else if description}
		<span class="text-text-muted text-base">{description}</span>
	{/if}
</label>
