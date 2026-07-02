<script lang="ts">
	import type { DatabaseService, StatCardData } from '$lib/mock/types';
	import { toast } from '$lib/stores/toast.svelte';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import StatCard from '$lib/components/ui/StatCard.svelte';
	import StatusPill from '$lib/components/ui/StatusPill.svelte';
	import ConnectedAppsList from './ConnectedAppsList.svelte';
	import DbConnectionPanel from './DbConnectionPanel.svelte';
	import DbExternalPanel from './DbExternalPanel.svelte';

	let { service }: { service: DatabaseService } = $props();

	const stats = $derived.by((): StatCardData[] => {
		const [computeValue, ...computeRest] = service.compute.split(' ');
		return [
			{
				label: 'STORAGE',
				value: service.storage_used,
				unit: `/ ${service.storage_total} GB`,
				progress: { pct: service.storage_pct, class: 'bg-service-db' }
			},
			{
				label: 'CONNECTIONS',
				value: String(service.connections),
				unit: `/ ${service.max_connections}`,
				chip: { text: `${service.connected_apps.length} apps`, tone: 'neutral' }
			},
			{
				label: 'COMPUTE',
				value: computeValue,
				unit: computeRest.join(' '),
				chip: { text: service.node, tone: 'neutral' }
			},
			{
				label: 'LAST BACKUP',
				value: service.last_backup.time,
				unit: 'today',
				chip: { text: service.last_backup.note, tone: 'success' }
			}
		];
	});

	function backupNow() {
		void toast.promise(new Promise((resolve) => setTimeout(resolve, 1800)), {
			loading: `Creating snapshot of ${service.name}…`,
			success: {
				title: 'Backup complete',
				description: `${service.storage_used}G snapshot stored — mock only.`
			},
			error: 'Backup failed'
		});
	}
</script>

<PageHeader title={service.name}>
	{#snippet titleTrailing()}
		<StatusPill status={service.status} pill />
	{/snippet}
	{#snippet subtitle()}
		<span class="font-mono text-text-faint text-[11.5px]">
			{service.engine}
			{service.version} · created {service.created_at} · id {service.short_id}
		</span>
	{/snippet}
	{#snippet actions()}
		<Button onclick={() => toast.info('The database studio is coming soon')}>Open studio</Button>
		<Button onclick={backupNow}>Back up now</Button>
	{/snippet}
</PageHeader>

<div class="mb-6 grid grid-cols-4 gap-3.5">
	{#each stats as stat (stat.label)}
		<StatCard {stat} />
	{/each}
</div>

<div class="mb-6 grid grid-cols-2 gap-3.5">
	<DbConnectionPanel {service} />
	<DbExternalPanel {service} />
</div>

<div class="mb-3.5 flex items-baseline gap-2.5">
	<h2 class="text-text-primary text-[16px] font-semibold">Connected applications</h2>
	<div class="text-text-ghost text-[12px]">via private network · zero-latency</div>
</div>

<div class="pb-6">
	<ConnectedAppsList {service} />
</div>
