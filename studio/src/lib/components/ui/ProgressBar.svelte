<script lang="ts">
	let {
		pct,
		size = 'sm',
		active = false,
		class: fillClass = 'bg-accent'
	}: {
		pct: number;
		/** sm is the hairline used in lists; md is a bar the eye rests on. */
		size?: 'sm' | 'md';
		/** Sweep a highlight across the fill while work is in progress. */
		active?: boolean;
		class?: string;
	} = $props();

	// An active bar at 0% still needs a sliver to carry the sheen.
	const width = $derived(Math.min(100, Math.max(pct, active ? 3 : 0)));
</script>

<div class="overflow-hidden rounded-full bg-white/6 {size === 'md' ? 'h-2.5' : 'h-1'}">
	<div
		class="relative h-full overflow-hidden rounded-full transition-[width] duration-500 ease-out {fillClass}"
		style:width="{width}%"
	>
		{#if active}
			<div
				class="animate-progress-sheen absolute inset-0 bg-linear-to-r from-transparent via-white/30 to-transparent"
			></div>
		{/if}
	</div>
</div>
