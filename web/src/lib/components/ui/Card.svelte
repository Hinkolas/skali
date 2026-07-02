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

<div class="bg-surface-raised border-border-default rounded-[14px] border {className}">
	{@render children()}
</div>
