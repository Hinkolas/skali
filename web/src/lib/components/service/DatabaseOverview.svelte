<script lang="ts">
	import type { DatabaseView, ServiceView } from '$lib/models/service';
	import type { StatCardData } from '$lib/models/view';
	import type { DatabaseConnection } from '$lib/types/connections';
	import type { Backup } from '$lib/types/definition';
	import type { ServiceStorage } from '$lib/types/metrics';
	import type { Run } from '$lib/types/runs';
	import { envStatus } from '$lib/stores/envstatus.svelte';
	import { formatBytes, relativeTime } from '$lib/format';
	import { describeCron, describeSeconds } from '$lib/cron';
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

	const ref = $derived(`databases.${service.key}`);
	const apps = $derived(
		services.filter((s) => s.type === 'application' && s.dependencies.includes(ref))
	);
	const healthyApps = $derived(
		apps.filter((a) => envStatus.service('application', a.key)?.health === 'healthy').length
	);

	// The first schedule that includes this database, by name.
	const schedule = $derived(
		Object.entries(backups)
			.toSorted(([a], [b]) => a.localeCompare(b))
			.find(
				([, b]) => b.include.allDatabases || (b.include.databases ?? []).includes(service.key)
			) ?? null
	);
	// Snapshots are taken per environment, so its backup runs are this
	// database's backups too.
	const lastBackup = $derived(
		(runs ?? []).find((r) => r.kind === 'backup' && r.status === 'succeeded') ?? null
	);

	// Measured logical size from the sampler when it exists; the declared
	// request stays the fallback and the denominator.
	const sizeStat = $derived.by((): StatCardData => {
		const declared = service.config.storageBytes;
		if (storage?.used_bytes != null) {
			const parts = formatBytes(storage.used_bytes).split(' ');
			return declared
				? {
						label: 'SIZE',
						value: parts[0],
						unit: `${parts[1]} / ${formatBytes(declared)}`,
						progress: {
							pct: Math.min(100, (storage.used_bytes / declared) * 100),
							class: 'bg-service-db'
						}
					}
				: { label: 'SIZE', value: parts[0], unit: parts[1], note: 'logical size' };
		}
		return {
			label: 'SIZE',
			value: declared ? formatBytes(declared) : 'default',
			note: 'requested, not yet measured'
		};
	});

	const backupStat = $derived.by((): StatCardData => {
		const split: StatCardData['split'] = [];
		if (schedule) {
			split.push({ label: 'schedule', value: describeCron(schedule[1].schedule) });
			split.push({ label: 'keep', value: describeSeconds(schedule[1].retentionSeconds) });
		}
		if (lastBackup) {
			const [ago, ...rest] = relativeTime(lastBackup.finished_at ?? lastBackup.created_at).split(
				' '
			);
			return { label: 'LAST BACKUP', value: ago, unit: rest.join(' '), split };
		}
		return {
			label: 'LAST BACKUP',
			value: 'none',
			note: schedule ? undefined : 'no schedule covers this database',
			split
		};
	});

	const stats = $derived.by((): StatCardData[] => [
		sizeStat,
		backupStat,
		{
			label: 'CONNECTED',
			value: `${apps.length}`,
			unit: `app${apps.length === 1 ? '' : 's'}`,
			note:
				apps.length === 0
					? 'no application depends on it'
					: `${healthyApps}/${apps.length} healthy · private network`
		},
		{
			label: 'PHASE',
			value: connection?.phase ?? 'unknown',
			chip:
				connection?.phase === 'provisioned'
					? { text: 'ready', tone: 'success' }
					: { text: 'settling', tone: 'neutral' },
			note: connection?.credential_version
				? `credentials v${connection.credential_version}`
				: undefined
		}
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
		snapshots of this environment{schedule ? ` · ${describeCron(schedule[1].schedule)}` : ''}
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
