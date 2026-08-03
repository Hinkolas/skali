<script lang="ts">
	import type { DatabaseView, ServiceView } from '$lib/models/service';
	import type { StatCardData } from '$lib/models/view';
	import type { DatabaseConnection } from '$lib/types/connections';
	import { formatBytes } from '$lib/format';
	import StatCard from '$lib/components/ui/StatCard.svelte';
	import ConnectedAppsList from './ConnectedAppsList.svelte';
	import DbConnectionPanel from './DbConnectionPanel.svelte';
	import DbExternalPanel from './DbExternalPanel.svelte';

	let {
		service,
		services,
		connection,
		envId
	}: {
		service: DatabaseView;
		services: ServiceView[];
		connection: DatabaseConnection | null;
		envId: string | null;
	} = $props();

	const stats = $derived.by((): StatCardData[] => [
		{
			label: 'STORAGE',
			value: service.config.storageBytes ? formatBytes(service.config.storageBytes) : 'default',
			note: 'requested in skali.yaml'
		},
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
	<div class="text-text-ghost text-md">via private network</div>
</div>

<div class="pb-6">
	<ConnectedAppsList {service} {services} />
</div>
