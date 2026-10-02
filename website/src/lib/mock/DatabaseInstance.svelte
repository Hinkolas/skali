<script lang="ts">
	// The Studio's database instance panel.
	import ServiceTile from './ServiceTile.svelte';

	const rows = [
		['Engine', 'postgresql 17'],
		['Isolation', 'project · one cluster'],
		['Availability', 'asynchronous']
	];
</script>

<div
	class="w-full rounded-[15px] border border-border-raised bg-surface-card px-[19.8px] py-[18px] leading-normal text-text-primary"
>
	<div class="mb-2 flex items-center gap-[11px]">
		<ServiceTile kind="database" size={33} />
		<span class="text-[17px] font-semibold">Instance</span>
		<span class="ml-auto flex items-center gap-[6.6px] text-[13px] text-status-success">
			<span class="size-2 rounded-full bg-status-success"></span>healthy
		</span>
	</div>
	{#each rows as [key, value] (key)}
		<div class="flex border-b border-border-subtle py-[8.8px] text-[13px]">
			<span class="w-32 shrink-0 text-text-faint sm:w-37.5">{key}</span>
			<span class="min-w-0 truncate font-mono text-text-secondary">{value}</span>
		</div>
	{/each}
	<div class="flex items-center border-b border-border-subtle py-[8.8px] text-[13px]">
		<span class="w-32 shrink-0 text-text-faint sm:w-37.5">Extensions</span>
		<span class="flex gap-1.5 font-mono text-[10px]">
			<span class="rounded-full bg-service-db/12 px-[8.8px] py-[2.2px] text-text-secondary"
				>vector</span
			>
			<span class="rounded-full bg-white/6 px-[8.8px] py-[2.2px] text-text-muted">pg_trgm</span>
		</span>
	</div>
	<div class="flex py-[8.8px] text-[13px]">
		<span class="w-32 shrink-0 text-text-faint sm:w-37.5">Point-in-time recovery</span>
		<span class="font-mono text-text-secondary">7 days window</span>
	</div>
</div>
