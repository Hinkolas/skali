<script lang="ts">
	import type { ApplicationView, ServiceView } from '$lib/models/service';
	import { usageLimits, usageStats } from '$lib/models/usage';
	import type { StatCardData } from '$lib/models/view';
	import type { EnvironmentMetrics, ServiceStorage } from '$lib/types/metrics';
	import type { Run } from '$lib/types/runs';
	import { envStatus } from '$lib/stores/envstatus.svelte';
	import Skeleton from '$lib/components/ui/Skeleton.svelte';
	import StatCard from '$lib/components/ui/StatCard.svelte';
	import ApplicationStoragePanel from './ApplicationStoragePanel.svelte';
	import ConnectedServicesPanel from './ConnectedServicesPanel.svelte';
	import RoutesPanel from './RoutesPanel.svelte';
	import RuntimePanel from './RuntimePanel.svelte';
	import ServiceChangesSection from './ServiceChangesSection.svelte';

	// The application's overview: its usage over the last day, the process
	// and pods that run it, what it talks to and is reached at, its storage,
	// and the rollouts that changed it.
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

	// The replica and restart tiles read pods from the live status; until
	// the stream's first document they hold their place instead of saying
	// "0 / 0 ready".
	const pendingStats = $derived(
		envStatus.pending && !routed ? new Set(['REPLICAS', 'RESTARTS']) : new Set<string>()
	);
</script>

<div class="mb-6.5 grid grid-cols-2 gap-3.5 @4xl:grid-cols-4">
	{#each stats as stat (stat.label)}
		{#if pendingStats.has(stat.label)}
			<Skeleton variant="card" class="h-full min-h-28" />
		{:else}
			<StatCard {stat} />
		{/if}
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
	<div class="mb-3.5 flex flex-wrap items-baseline gap-x-2.5 gap-y-0.5">
		<h2 class="text-text-primary text-xl font-semibold">Storage</h2>
		<div class="text-text-muted text-md">this application's footprint</div>
	</div>
	<div class="mb-6.5">
		<ApplicationStoragePanel {storage} {temporaryStorage} />
	</div>
{/if}

<ServiceChangesSection {service} {envId} {runs} />
