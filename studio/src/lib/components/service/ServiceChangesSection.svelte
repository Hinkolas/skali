<script lang="ts">
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import ArrowRight from '@lucide/svelte/icons/arrow-right';
	import type { ServiceView } from '$lib/models/service';
	import type { Run } from '$lib/types/runs';
	import { withEnv } from '$lib/urls';
	import RunsSection from '$lib/components/run/RunsSection.svelte';

	// A service's own history: the newest deploys, rollbacks, and restarts
	// that changed it, each saying what it did to this service. The
	// environment's full history is the project's Deployments page.
	let {
		service,
		envId,
		runs
	}: {
		service: ServiceView;
		envId: string | null;
		runs: Run[] | null;
	} = $props();

	const RECENT_CHANGES = 5;
	const COLLECTIONS = {
		application: 'applications',
		database: 'databases',
		bucket: 'buckets'
	} as const;
	const dotted = $derived(`${COLLECTIONS[service.type]}.${service.key}`);

	const projectName = $derived((page.data.project as { name: string }).name);
	const envName = $derived((page.data.env as { name: string } | null)?.name ?? null);
</script>

<div class="mb-3.5 flex items-center gap-3">
	<div class="flex min-w-0 flex-1 flex-wrap items-baseline gap-x-2.5 gap-y-0.5">
		<h2 class="text-text-primary text-xl font-semibold">Recent changes</h2>
		<div class="text-text-muted text-md">deploys, rollbacks, and restarts that changed it</div>
	</div>
	<!-- eslint-disable svelte/no-navigation-without-resolve -- path built with resolve(), env appended by $lib/urls -->
	<a
		href={withEnv(
			resolve('/(app)/projects/[project]/deployments', { project: projectName }),
			envName
		)}
		class="text-text-muted hover:text-text-primary flex flex-none items-center gap-1 text-md whitespace-nowrap transition-colors"
	>
		All deployments <ArrowRight size={13} />
	</a>
	<!-- eslint-enable svelte/no-navigation-without-resolve -->
</div>

<div class="mb-6.5">
	<RunsSection
		{envId}
		seed={runs}
		service={dotted}
		limit={RECENT_CHANGES}
		emptyTitle="No changes recorded yet"
		emptyDescription="deploys, rollbacks, and restarts that change {service.key} appear here"
	/>
</div>
