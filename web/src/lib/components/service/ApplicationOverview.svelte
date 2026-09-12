<script lang="ts">
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import ArrowRight from '@lucide/svelte/icons/arrow-right';
	import type { ApplicationView, ServiceView } from '$lib/models/service';
	import { usageLimits, usageStats } from '$lib/models/usage';
	import type { StatCardData } from '$lib/models/view';
	import type { EnvironmentMetrics, ServiceStorage } from '$lib/types/metrics';
	import type { Run } from '$lib/types/runs';
	import { envStatus } from '$lib/stores/envstatus.svelte';
	import { withEnv } from '$lib/urls';
	import StatCard from '$lib/components/ui/StatCard.svelte';
	import RunsSection from '$lib/components/run/RunsSection.svelte';
	import ApplicationStoragePanel from './ApplicationStoragePanel.svelte';
	import ConnectedServicesPanel from './ConnectedServicesPanel.svelte';
	import RoutesPanel from './RoutesPanel.svelte';
	import RuntimePanel from './RuntimePanel.svelte';

	// The application's overview: its usage over the last day, the process
	// and pods that run it, what it talks to and is reached at, its storage,
	// and the newest runs of its environment.
	let {
		service,
		services,
		envId,
		runs,
		metrics = null,
		storage = null,
		temporaryStorage = null
	}: {
		service: ApplicationView;
		services: ServiceView[];
		envId: string | null;
		runs: Run[] | null;
		metrics?: EnvironmentMetrics | null;
		storage?: ServiceStorage | null;
		temporaryStorage?: ServiceStorage | null;
	} = $props();

	const projectName = $derived((page.data.project as { name: string }).name);
	const envName = $derived((page.data.env as { name: string } | null)?.name ?? null);

	// The same four tiles as the project overview, scoped to this
	// application's series and its own declared limits. An application
	// without a public route has no edge traffic by construction, so its
	// requests and traffic tiles give way to the replica and restart counts
	// (what a worker's health is actually about).
	const live = $derived(envStatus.service('application', service.key));
	const limits = $derived(usageLimits({ [service.key]: service.config }, () => live));
	const routed = $derived(Object.keys(service.config.routes ?? {}).length > 0);
	const stats = $derived.by((): StatCardData[] => {
		const [requests, cpu, mem, traffic] = usageStats(
			metrics,
			metrics?.applications.filter((a) => a.key === service.key) ?? [],
			limits
		);
		if (routed) return [requests, cpu, mem, traffic];
		const pods = live?.pods ?? [];
		const ready = pods.filter((p) => p.ready).length;
		const restarts = pods.reduce((acc, p) => acc + p.restarts, 0);
		const { minReplicas, maxReplicas } = service.config.scaling;
		const scale = minReplicas === maxReplicas ? `${minReplicas}` : `${minReplicas}-${maxReplicas}`;
		return [
			{
				label: 'REPLICAS',
				value: `${ready}`,
				unit: `/ ${pods.length} ready`,
				note: `scale ${scale}`
			},
			cpu,
			mem,
			{
				label: 'RESTARTS',
				value: `${restarts}`,
				note:
					pods.length > 0
						? `across ${pods.length} pod${pods.length === 1 ? '' : 's'}`
						: 'no pods observed'
			}
		];
	});

	const RECENT_RUNS = 5;
</script>

<div class="mb-6.5 grid grid-cols-2 gap-3.5 @4xl:grid-cols-4">
	{#each stats as stat (stat.label)}
		<StatCard {stat} />
	{/each}
</div>

<div class="mb-6.5 grid grid-cols-1 gap-3.5 @4xl:grid-cols-[1.5fr_1fr]">
	<RuntimePanel {service} />
	<div class="flex flex-col gap-3.5">
		<ConnectedServicesPanel {service} {services} />
		<RoutesPanel serviceKey={service.key} />
	</div>
</div>

{#if storage || temporaryStorage}
	<div class="mb-3.5 flex items-baseline gap-2.5">
		<h2 class="text-text-primary text-xl font-semibold">Storage</h2>
		<div class="text-text-muted text-md">this application's footprint</div>
	</div>
	<div class="mb-6.5">
		<ApplicationStoragePanel {storage} {temporaryStorage} />
	</div>
{/if}

<div class="mb-3.5 flex items-baseline gap-2.5">
	<h2 class="text-text-primary text-xl font-semibold">Recent runs</h2>
	<div class="text-text-muted text-md">the newest {RECENT_RUNS} in this environment</div>
	<!-- eslint-disable svelte/no-navigation-without-resolve -- path built with resolve(), env appended by $lib/urls -->
	<a
		href={withEnv(
			resolve('/(app)/projects/[project]/services/[service]/deployments', {
				project: projectName,
				service: service.key
			}),
			envName
		)}
		class="text-text-muted hover:text-text-primary ml-auto flex items-center gap-1 text-md transition-colors"
	>
		All deployments <ArrowRight size={13} />
	</a>
	<!-- eslint-enable svelte/no-navigation-without-resolve -->
</div>

<div class="pb-6">
	<RunsSection {envId} seed={runs} limit={RECENT_RUNS} />
</div>
