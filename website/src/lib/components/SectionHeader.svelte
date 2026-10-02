<script lang="ts">
	/**
	 * The numbered label in a narrow left column with the section's heading
	 * (and optional children, e.g. a lead paragraph) beside it.
	 */
	import type { Snippet } from 'svelte';

	let {
		index,
		label,
		title,
		children
	}: { index: string; label: string; title: string; children?: Snippet } = $props();
</script>

<div class="flex flex-wrap gap-x-12 gap-y-6">
	<div class="flex shrink-0 basis-55 gap-3 pt-3 font-mono text-xs">
		<span class="text-accent-light">{index}</span>
		<span class="text-text-faint">{label}</span>
	</div>
	<div class="flex flex-[1_1_600px] flex-col gap-5">
		<h2
			class="m-0 max-w-200 text-[clamp(32px,4.6vw,52px)] leading-[1.05] font-medium tracking-[-0.04em]"
		>
			{title}
		</h2>
		{@render children?.()}
	</div>
</div>
