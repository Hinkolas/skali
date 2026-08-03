<script lang="ts">
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import type { Service } from '$lib/mock/types';
	import { currentEnv, withEnv } from '$lib/urls';
	import ProgressBar from '$lib/components/ui/ProgressBar.svelte';
	import StatusPill from '$lib/components/ui/StatusPill.svelte';
	import TypeBadge from '$lib/components/ui/TypeBadge.svelte';

	let { service }: { service: Service } = $props();

	// Only rendered in project scope, so page.data.project is present.
	const env = $derived(currentEnv(page.data.project, page.url));
</script>

<!-- eslint-disable svelte/no-navigation-without-resolve -- path built with resolve(), env appended by $lib/urls -->
<a
	href={withEnv(
		resolve('/(app)/projects/[project]/services/[service]', {
			project: service.project_slug,
			service: service.slug
		}),
		env
	)}
	class="bg-surface-raised border-border-default hover:border-accent/35 flex flex-col gap-3 rounded-[15px] border px-4.5 py-4 transition-colors"
>
	<div class="flex items-center gap-2.5">
		<TypeBadge kind={service.type} form="tile" size="md" />
		<div class="min-w-0">
			<div class="text-text-primary truncate text-lg font-semibold">{service.name}</div>
			<div class="font-mono text-text-faint truncate text-xs">{service.kind_label}</div>
		</div>
		<span class="ml-auto flex-none">
			<StatusPill status={service.status} />
		</span>
	</div>

	{#if service.type === 'application'}
		<div class="font-mono text-text-muted truncate text-sm">
			{service.domain ?? service.endpoint}
		</div>
		<div class="font-mono text-text-faint border-border-subtle flex gap-3 border-t pt-2.75 text-xs">
			<span>cpu <span class="text-text-secondary">{service.cpu_pct}</span></span>
			<span>mem <span class="text-text-secondary">{service.mem}</span></span>
			<span>×<span class="text-text-secondary">{service.instances}</span></span>
		</div>
	{:else if service.type === 'database'}
		<div class="flex flex-col gap-1.5">
			<div class="font-mono text-text-muted text-sm">
				{service.storage_used}G / {service.storage_total}G
			</div>
			<ProgressBar pct={service.storage_pct} class="bg-service-db" />
		</div>
		<div class="font-mono text-text-faint border-border-subtle flex gap-3 border-t pt-2.75 text-xs">
			<span>conns <span class="text-text-secondary">{service.connections}</span></span>
			<span>mem <span class="text-text-secondary">{service.mem}</span></span>
		</div>
	{:else if service.type === 'cache'}
		<div class="font-mono text-text-muted truncate text-sm">{service.endpoint}</div>
		<div class="font-mono text-text-faint border-border-subtle flex gap-3 border-t pt-2.75 text-xs">
			<span>hits <span class="text-text-secondary">{service.hit_rate}</span></span>
			<span>keys <span class="text-text-secondary">{service.keys}</span></span>
		</div>
	{:else}
		<div class="font-mono text-text-muted truncate text-sm">
			{service.size} · {service.objects} objects
		</div>
		<div class="font-mono text-text-faint border-border-subtle flex gap-3 border-t pt-2.75 text-xs">
			<span>egress <span class="text-text-secondary">{service.egress_per_day}</span></span>
		</div>
	{/if}
</a>
