<script lang="ts">
	import type { Snippet } from 'svelte';

	let { label, trailing }: { label: string; trailing?: Snippet } = $props();
</script>

<div
	class="text-text-ghost flex items-center px-1 pt-4.5 pb-2 text-xs font-semibold tracking-[0.13em] uppercase"
>
	{label}
	{#if trailing}
		{@render trailing()}
	{/if}
</div>
