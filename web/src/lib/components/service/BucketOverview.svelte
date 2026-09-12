<script lang="ts">
	import type { BucketView, ServiceView } from '$lib/models/service';
	import type { StatCardData } from '$lib/models/view';
	import type { BucketConnection } from '$lib/types/connections';
	import type { Backup } from '$lib/types/definition';
	import type { ServiceStorage } from '$lib/types/metrics';
	import type { Run } from '$lib/types/runs';
	import { envStatus } from '$lib/stores/envstatus.svelte';
	import { describeCron } from '$lib/cron';
	import {
		backupSchedule,
		connectedStat,
		dependents,
		footprintStat,
		lastBackupStat,
		phaseStat
	} from '$lib/models/claims';
	import StatCard from '$lib/components/ui/StatCard.svelte';
	import RunsSection from '$lib/components/run/RunsSection.svelte';
	import BucketConnectionPanel from './BucketConnectionPanel.svelte';
	import BucketDetailsPanel from './BucketDetailsPanel.svelte';
	import ConnectedAppsList from './ConnectedAppsList.svelte';

	// The bucket's overview: how full it is against its quota, how it is
	// backed up, who holds its keys, what the claim asked for and how to
	// reach it, then the newest snapshots of its environment.
	let {
		service,
		services,
		connection,
		envId,
		runs = null,
		backups = {},
		storage = null
	}: {
		service: BucketView;
		services: ServiceView[];
		connection: BucketConnection | null;
		envId: string | null;
		runs?: Run[] | null;
		/** The project's backup schedules, keyed by name. */
		backups?: Record<string, Backup>;
		storage?: ServiceStorage | null;
	} = $props();

	const live = (key: string) => envStatus.service('application', key);
	const apps = $derived(dependents(services, 'bucket', service.key));
	const schedule = $derived(backupSchedule(backups, 'bucket', service.key));

	// Measured object bytes from the sampler when they exist; the declared
	// quota stays the fallback and the denominator.
	const stats = $derived.by((): StatCardData[] => [
		footprintStat(
			'USED',
			storage?.used_bytes,
			service.config.storageQuotaBytes,
			'bg-service-storage',
			{
				declared: 'quota, not yet measured',
				undeclared: 'no quota, not yet measured',
				measured: 'no quota'
			}
		),
		lastBackupStat(schedule, runs, 'bucket'),
		connectedStat(apps, live, 'keys injected'),
		phaseStat(connection)
	]);

	const RECENT_BACKUPS = 5;
</script>

<div class="mb-6.5 grid grid-cols-2 gap-3.5 @4xl:grid-cols-4">
	{#each stats as stat (stat.label)}
		<StatCard {stat} />
	{/each}
</div>

<div class="mb-6.5 grid grid-cols-1 gap-3.5 @4xl:grid-cols-2">
	<BucketDetailsPanel {service} />
	<BucketConnectionPanel {service} {connection} {envId} />
</div>

<div class="mb-3.5 flex items-baseline gap-2.5">
	<h2 class="text-text-primary text-xl font-semibold">Connected applications</h2>
	<div class="text-text-muted text-md">keys injected as env values</div>
</div>

<div class="mb-6.5">
	<ConnectedAppsList {service} {services} />
</div>

<div class="mb-3.5 flex items-baseline gap-2.5">
	<h2 class="text-text-primary text-xl font-semibold">Recent backups</h2>
	<div class="text-text-muted text-md">
		snapshots of this environment{schedule ? ` · ${describeCron(schedule.backup.schedule)}` : ''}
	</div>
</div>

<div class="pb-6">
	<RunsSection
		{envId}
		seed={runs}
		kinds={['backup', 'restore']}
		limit={RECENT_BACKUPS}
		emptyTitle="No backups yet"
		emptyDescription="snapshots and restores of this environment appear here"
	/>
</div>
