<script lang="ts">
	import { resolve } from '$app/paths';
	import type { Project, ServiceHealth } from '$lib/types/project';
	import { relativeTime } from '$lib/format';
	import Pill from '$lib/components/ui/Pill.svelte';
	import TypeBadge from '$lib/components/ui/TypeBadge.svelte';

	let { project }: { project: Project } = $props();

	const environments = $derived(project.summary?.environments ?? []);
	const counts = $derived(project.summary?.service_counts);
	const serviceCount = $derived(
		(counts?.applications ?? 0) + (counts?.databases ?? 0) + (counts?.buckets ?? 0)
	);

	const healthDot: Record<ServiceHealth, string> = {
		healthy: 'bg-status-success',
		progressing: 'bg-status-warning',
		degraded: 'bg-status-warning',
		unhealthy: 'bg-status-danger',
		unknown: 'bg-text-ghost'
	};
	// The card dot shows the worst environment health.
	const rank: Record<ServiceHealth, number> = {
		healthy: 1,
		unknown: 2,
		progressing: 3,
		degraded: 4,
		unhealthy: 5
	};
	// Locked environments carry no health and stay out of the rollup.
	const worst = $derived(
		environments.reduce<ServiceHealth>(
			(acc, e) => (e.health && rank[e.health] > rank[acc] ? e.health : acc),
			environments.length ? 'healthy' : 'unknown'
		)
	);
</script>

<a
	href={resolve('/(app)/projects/[project]', { project: project.name })}
	class="bg-surface-card border-border-raised hover:border-accent/35 flex flex-col gap-3.5 rounded-[15px] border px-5 py-4.5 transition-colors"
>
	<div class="flex flex-wrap items-center gap-x-2.5 gap-y-1.5">
		<div class="text-text-primary truncate text-xl font-semibold">
			{project.display_name || project.name}
		</div>
		{#if environments.length > 0}
			<span class="flex items-center gap-2.5">
				<Pill
					text={environments[0].name}
					tone={environments[0].name === 'production' ? 'success' : 'neutral'}
				/>
				{#if environments.length > 1}
					<Pill text="+{environments.length - 1}" tone="neutral" />
				{/if}
			</span>
		{/if}
		<span class="ml-auto size-[8px] flex-none rounded-full {healthDot[worst]}"></span>
	</div>
	<div class="flex gap-1.5">
		{#if counts?.applications}
			<TypeBadge kind="application" count={counts.applications} />
		{/if}
		{#if counts?.databases}
			<TypeBadge kind="database" count={counts.databases} />
		{/if}
		{#if counts?.buckets}
			<TypeBadge kind="bucket" count={counts.buckets} />
		{/if}
		{#if serviceCount === 0}
			<span class="font-mono text-text-faint text-md">no services defined yet</span>
		{/if}
	</div>
	<div class="font-mono text-text-faint border-border-subtle flex gap-3.5 border-t pt-3 text-xs">
		<span>{serviceCount} service{serviceCount === 1 ? '' : 's'}</span>
		<span>updated <span class="text-text-secondary">{relativeTime(project.updated_at)}</span></span>
	</div>
</a>
