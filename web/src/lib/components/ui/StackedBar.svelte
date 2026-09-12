<script lang="ts">
	// An iOS-storage-style segmented capacity bar. Segments render in order
	// as shares of total; the unfilled remainder is the free space. When the
	// segments (estimates included) exceed the total, everything scales down
	// proportionally so the bar never overflows. Sub-0.4% slivers are
	// dropped rather than rendered as invisible hairlines.
	export interface StackedSegment {
		label: string;
		value: number;
		class: string;
	}

	let {
		segments,
		total,
		class: className = 'h-1.5'
	}: {
		segments: StackedSegment[];
		total: number;
		/** Height (and any track overrides); the default is the dense table size. */
		class?: string;
	} = $props();

	const shares = $derived.by(() => {
		if (total <= 0) return [];
		const sum = segments.reduce((acc, s) => acc + Math.max(0, s.value), 0);
		const scale = sum > total ? total / sum : 1;
		return segments
			.map((s) => ({ ...s, pct: (Math.max(0, s.value) * scale * 100) / total }))
			.filter((s) => s.pct >= 0.4);
	});
</script>

<div class="flex gap-px overflow-hidden rounded-full bg-white/6 {className}">
	{#each shares as segment (segment.label)}
		<div class="h-full {segment.class}" style:width="{segment.pct}%" title={segment.label}></div>
	{/each}
</div>
