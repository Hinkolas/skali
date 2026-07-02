<script lang="ts">
	import { resolve } from '$app/paths';
	import type { Project } from '$lib/mock/types';
	import Pill from '$lib/components/ui/Pill.svelte';
	import TypeBadge from '$lib/components/ui/TypeBadge.svelte';

	let { project }: { project: Project } = $props();

	const statusDot: Record<Project['status'], string> = {
		healthy: 'bg-status-success',
		building: 'bg-status-warning',
		degraded: 'bg-status-danger'
	};
</script>

<a
	href={resolve('/(app)/projects/[project]', { project: project.slug })}
	class="bg-surface-raised border-border-default hover:border-accent/35 flex flex-col gap-3.5 rounded-[14px] border px-5 py-4.5 transition-colors"
>
	<div class="flex items-center gap-2.5">
		<div class="text-text-primary text-[15.5px] font-semibold">{project.name}</div>
		<Pill
			text={project.environment}
			tone={project.environment === 'production' ? 'success' : 'neutral'}
		/>
		<span class="ml-auto size-[7px] flex-none rounded-full {statusDot[project.status]}"></span>
	</div>
	<div class="flex gap-1.5">
		{#each project.service_badges as badge (badge.kind)}
			<TypeBadge kind={badge.kind} count={badge.count} />
		{/each}
	</div>
	<div
		class="font-mono text-text-faint border-border-subtle flex gap-3.5 border-t pt-3 text-[10.5px]"
	>
		<span>{project.service_count} services</span>
		{#if project.building_note}
			<span class="text-status-warning">{project.building_note}</span>
		{:else if project.deploy_note}
			<span>deploy <span class="text-text-secondary">{project.deploy_note}</span></span>
		{/if}
	</div>
</a>
