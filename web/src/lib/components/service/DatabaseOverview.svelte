<script lang="ts">
	import type { DatabaseView, ServiceView } from '$lib/models/service';
	import type { StatCardData } from '$lib/models/view';
	import type { DatabaseConnection } from '$lib/types/connections';
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
	import ConnectedAppsList from './ConnectedAppsList.svelte';
	import DbConnectionPanel from './DbConnectionPanel.svelte';
	import DbInstancePanel from './DbInstancePanel.svelte';

	// The database's overview: how full it is, how it is backed up, who
	// talks to it, the server it runs on and how to reach it, then the
	// newest snapshots of its environment.
	let {
		service,
		services,
		connection,
		envId,
		runs = null,
		backups = {},
		storage = null
	}: {
		service: DatabaseView;
		services: ServiceView[];
		connection: DatabaseConnection | null;
		envId: string | null;
		runs?: Run[] | null;
		/** The project's backup schedules, keyed by name. */
		backups?: Record<string, Backup>;
		storage?: ServiceStorage | null;
	} = $props();

	const live = (key: string) => envStatus.service('application', key);
	const apps = $derived(dependents(services, 'database', service.key));
	const schedule = $derived(backupSchedule(backups, 'database', service.key));

	// Measured logical size from the sampler when it exists; the declared
	// request stays the fallback and the denominator.
	const stats = $derived.by((): StatCardData[] => [
		footprintStat('SIZE', storage?.used_bytes, service.config.storageBytes, 'bg-service-db', {
			declared: 'requested, not yet measured',
			undeclared: 'default size, not yet measured',
			measured: 'logical size'
		}),
		lastBackupStat(schedule, runs, 'database'),
		connectedStat(apps, live, 'private network'),
		phaseStat(connection)
	]);

	const RECENT_BACKUPS = 5;
</script>

<div class="mb-6.5 grid grid-cols-4 gap-3.5">
	{#each stats as stat (stat.label)}
		<StatCard {stat} />
	{/each}
</div>

<div class="mb-6.5 grid grid-cols-2 gap-3.5">
	<DbInstancePanel {service} {connection} />
	<DbConnectionPanel {service} {connection} {envId} />
</div>

<div class="mb-3.5 flex items-baseline gap-2.5">
	<h2 class="text-text-primary text-xl font-semibold">Connected applications</h2>
	<div class="text-text-muted text-md">via private network</div>
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
