<script lang="ts">
	import type { DatabaseView, ServiceView } from '$lib/models/service';
	import type { StatCardData } from '$lib/models/view';
	import type { DatabaseConnection } from '$lib/types/connections';
	import type { ServiceStorage } from '$lib/types/metrics';
	import { formatBytes } from '$lib/format';
	import StatCard from '$lib/components/ui/StatCard.svelte';
	import ConnectedAppsList from './ConnectedAppsList.svelte';
	import DbConnectionPanel from './DbConnectionPanel.svelte';
	import DbExternalPanel from './DbExternalPanel.svelte';

	let {
		service,
		services,
		connection,
		envId,
		storage = null
	}: {
		service: DatabaseView;
		services: ServiceView[];
		connection: DatabaseConnection | null;
		envId: string | null;
		storage?: ServiceStorage | null;
	} = $props();

	// Measured logical size from the sampler when it exists; the declared
	// request stays the fallback and the denominator.
	const storageStat = $derived.by((): StatCardData => {
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
			label: 'STORAGE',
			value: declared ? formatBytes(declared) : 'default',
			note: 'requested in skali.yaml'
		};
	});

	const stats = $derived.by((): StatCardData[] => [
		storageStat,
		{
			label: 'ENGINE',
			value: service.config.engine,
			unit: connection ? `v${connection.major}` : service.config.version
		},
		{
			label: 'ISOLATION',
			value: service.config.isolation,
			chip: { text: service.config.availability, tone: 'neutral' }
		},
		{
			label: 'PHASE',
			value: connection?.phase ?? 'unknown',
			chip:
				connection?.phase === 'provisioned'
					? { text: 'ready', tone: 'success' }
					: { text: 'settling', tone: 'neutral' }
		}
	]);
</script>

<div class="mb-6 grid grid-cols-4 gap-3.5">
	{#each stats as stat (stat.label)}
		<StatCard {stat} />
	{/each}
</div>

<div class="mb-6 grid grid-cols-2 gap-3.5">
	<DbConnectionPanel {service} {connection} {envId} />
	<DbExternalPanel />
</div>

<div class="mb-3.5 flex items-baseline gap-2.5">
	<h2 class="text-text-primary text-xl font-semibold">Connected applications</h2>
	<div class="text-text-muted text-md">via private network</div>
</div>

<div class="pb-6">
	<ConnectedAppsList {service} {services} />
</div>
