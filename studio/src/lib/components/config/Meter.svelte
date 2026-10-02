<script lang="ts">
	// A resource's request against its limit as one bar: the limit is the
	// full track, the request the filled part. Either may be absent, which
	// the caption says plainly instead of drawing a misleading bar.
	let {
		label,
		request,
		limit,
		format,
		class: fillClass = 'bg-accent'
	}: {
		label: string;
		request?: number;
		limit?: number;
		format: (v: number) => string;
		class?: string;
	} = $props();

	const pct = $derived(
		request != null && limit ? Math.min(100, (request / limit) * 100) : request != null ? 100 : 0
	);
</script>

<div class="flex flex-col gap-1.5">
	<div class="flex items-baseline gap-3">
		<span class="text-text-tertiary text-md font-medium">{label}</span>
		<span class="text-text-faint ml-auto font-mono text-xs">
			{#if request != null && limit}
				request <span class="text-text-primary">{format(request)}</span> · limit
				<span class="text-text-primary">{format(limit)}</span>
			{:else if limit}
				limit <span class="text-text-primary">{format(limit)}</span> · no request
			{:else if request != null}
				request <span class="text-text-primary">{format(request)}</span> · no limit
			{:else}
				not declared
			{/if}
		</span>
	</div>
	<div class="h-1.5 rounded-full bg-white/6">
		{#if request != null || limit}
			<div
				class="h-full rounded-full {request != null ? fillClass : 'bg-white/12'}"
				style:width="{request != null ? pct : 100}%"
			></div>
		{/if}
	</div>
</div>
