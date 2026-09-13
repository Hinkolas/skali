<script lang="ts">
	import { resolve } from '$app/paths';
	import type { Project } from '$lib/types/project';
	import { relativeTime } from '$lib/format';
	import { HEALTH_META } from '$lib/service-types';
	import { projectServiceCount, projectWorstHealth } from '$lib/models/project';
	import Pill from '$lib/components/ui/Pill.svelte';
	import Table from '$lib/components/ui/Table.svelte';
	import TypeBadge from '$lib/components/ui/TypeBadge.svelte';

	// The dense counterpart of the card grid: one row per project, the same
	// facts. Follows the Table contract: grid cells on a wide pane, a
	// wrapping row (identity, health on the right, one meta line beneath)
	// on a narrow one.
	let { projects }: { projects: Project[] } = $props();

	const grid = 'grid-cols-[1.6fr_1.4fr_1fr_0.9fr_0.7fr]';
</script>

<Table columns={['Project', 'Environments', 'Services', 'Health', 'Updated']} {grid}>
	{#each projects as project (project.id)}
		{@const environments = project.summary?.environments ?? []}
		{@const counts = project.summary?.service_counts}
		{@const serviceCount = projectServiceCount(project)}
		{@const health = HEALTH_META[projectWorstHealth(project)]}
		{@const updated = relativeTime(project.updated_at)}
		<a
			href={resolve('/(app)/projects/[project]', { project: project.name })}
			class="border-border-subtle border-b px-4.5 py-3 transition-colors last:border-0 hover:bg-white/2 @max-2xl:flex @max-2xl:flex-wrap @max-2xl:items-center @max-2xl:gap-x-3 @max-2xl:gap-y-1.5 @2xl:grid @2xl:items-center {grid}"
		>
			<div class="flex min-w-0 items-baseline gap-2 pr-3">
				<span class="text-text-primary truncate text-lg font-semibold">
					{project.display_name || project.name}
				</span>
				{#if project.display_name && project.display_name !== project.name}
					<span class="font-mono text-text-faint truncate text-xs">{project.name}</span>
				{/if}
			</div>
			<!-- Environments, services and (narrow only) the update time: grid
			     cells on a wide pane, one meta line under the name on a narrow one. -->
			<div
				class="@2xl:contents @max-2xl:order-1 @max-2xl:flex @max-2xl:basis-full @max-2xl:flex-wrap @max-2xl:items-center @max-2xl:gap-x-3 @max-2xl:gap-y-1.5"
			>
				<!-- Same rollup as the card: the first environment and a +N for the
				     rest, so a project with many environments stays one line. -->
				<div class="flex items-center gap-2 pr-3 whitespace-nowrap">
					{#if environments.length > 0}
						<Pill
							text={environments[0].name}
							tone={environments[0].name === 'production' ? 'success' : 'neutral'}
						/>
						{#if environments.length > 1}
							<span
								title={environments
									.slice(1)
									.map((e) => e.name)
									.join(', ')}
							>
								<Pill text="+{environments.length - 1}" tone="neutral" />
							</span>
						{/if}
					{:else}
						<span class="font-mono text-text-faint text-xs">no environments</span>
					{/if}
				</div>
				<div class="flex items-center gap-1.5 pr-3">
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
						<span class="font-mono text-text-faint text-xs">no services yet</span>
					{/if}
				</div>
				<div class="font-mono text-text-muted text-sm whitespace-nowrap @2xl:hidden">
					updated {updated}
				</div>
			</div>
			<div
				class="flex items-center gap-1.5 text-md whitespace-nowrap {health.text} @max-2xl:ml-auto"
			>
				<span class="size-[8px] rounded-full {health.dot}"></span>
				{health.label}
			</div>
			<div class="font-mono text-text-muted text-sm whitespace-nowrap @max-2xl:hidden">
				{updated}
			</div>
		</a>
	{/each}
</Table>
