<script lang="ts">
	import { resolve } from '$app/paths';
	import type { Service } from '$lib/mock/types';
	import ProgressBar from '$lib/components/ui/ProgressBar.svelte';
	import StatusPill from '$lib/components/ui/StatusPill.svelte';
	import TypeBadge from '$lib/components/ui/TypeBadge.svelte';

	let { service }: { service: Service } = $props();
</script>

<a
	href={resolve('/(app)/projects/[project]/services/[service]', {
		project: service.project_slug,
		service: service.slug
	})}
	class="bg-surface-raised border-border-default hover:border-accent/35 flex flex-col gap-3 rounded-[14px] border px-4.5 py-4 transition-colors"
>
	<div class="flex items-center gap-2.5">
		<TypeBadge kind={service.type} form="tile" size="md" />
		<div class="min-w-0">
			<div class="text-text-primary truncate text-[14px] font-semibold">{service.name}</div>
			<div class="font-mono text-text-faint truncate text-[10px]">{service.kind_label}</div>
		</div>
		<span class="ml-auto flex-none">
			<StatusPill status={service.status} />
		</span>
	</div>

	{#if service.type === 'application'}
		<div class="font-mono text-text-muted truncate text-[11px]">
			{service.domain ?? service.endpoint}
		</div>
		<div
			class="font-mono text-text-faint border-border-subtle flex gap-3 border-t pt-2.75 text-[10.5px]"
		>
			<span>cpu <span class="text-text-secondary">{service.cpu_pct}</span></span>
			<span>mem <span class="text-text-secondary">{service.mem}</span></span>
			<span>×<span class="text-text-secondary">{service.instances}</span></span>
		</div>
	{:else if service.type === 'database'}
		<div class="flex flex-col gap-1.5">
			<div class="font-mono text-text-muted text-[11px]">
				{service.storage_used}G / {service.storage_total}G
			</div>
			<ProgressBar pct={service.storage_pct} class="bg-service-db" />
		</div>
		<div
			class="font-mono text-text-faint border-border-subtle flex gap-3 border-t pt-2.75 text-[10.5px]"
		>
			<span>conns <span class="text-text-secondary">{service.connections}</span></span>
			<span>mem <span class="text-text-secondary">{service.mem}</span></span>
		</div>
	{:else if service.type === 'cache'}
		<div class="font-mono text-text-muted truncate text-[11px]">{service.endpoint}</div>
		<div
			class="font-mono text-text-faint border-border-subtle flex gap-3 border-t pt-2.75 text-[10.5px]"
		>
			<span>hits <span class="text-text-secondary">{service.hit_rate}</span></span>
			<span>keys <span class="text-text-secondary">{service.keys}</span></span>
		</div>
	{:else}
		<div class="font-mono text-text-muted truncate text-[11px]">
			{service.size} · {service.objects} objects
		</div>
		<div
			class="font-mono text-text-faint border-border-subtle flex gap-3 border-t pt-2.75 text-[10.5px]"
		>
			<span>egress <span class="text-text-secondary">{service.egress_per_day}</span></span>
		</div>
	{/if}
</a>
