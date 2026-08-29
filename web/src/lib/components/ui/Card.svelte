<script lang="ts">
	import type { Snippet } from 'svelte';

	let {
		class: className = '',
		children
	}: {
		class?: string;
		children: Snippet;
	} = $props();
</script>

<div class="bg-surface-card border-border-raised rounded-[15px] border {className}">
	{@render children()}
</div>
