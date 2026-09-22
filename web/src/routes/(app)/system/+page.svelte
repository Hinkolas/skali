<script lang="ts">
	import { resolve } from '$app/paths';
	import Archive from '@lucide/svelte/icons/archive';
	import ChevronRight from '@lucide/svelte/icons/chevron-right';
	import Database from '@lucide/svelte/icons/database';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import Server from '@lucide/svelte/icons/server';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Card from '$lib/components/ui/Card.svelte';
	import Pill from '$lib/components/ui/Pill.svelte';
	import { updatePresentation } from '$lib/types/updates';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	// Admin only, so the shell always asked for nodes; the fallback merely
	// satisfies the shell's nullable type.
	const nodes = $derived(data.nodes ?? []);

	// One line about the cluster: how many nodes are online, or that the
	// cluster has not been observed yet.
	const nodesSummary = $derived.by(() => {
		const total = nodes.length;
		if (total === 0) {
			return data.nodesObservation?.state === 'fresh'
				? { text: 'no nodes', tone: 'neutral' as const }
				: { text: 'not observed', tone: 'neutral' as const };
		}
		const online = nodes.filter((n) => n.ready).length;
		return online === total
			? { text: `${total} online`, tone: 'success' as const }
			: { text: `${online}/${total} online`, tone: 'warning' as const };
	});

	// One line about the backup target: set, or not yet.
	const backupSummary = $derived(
		data.backupTarget
			? { text: data.backupTarget.bucket, tone: 'success' as const }
			: { text: 'not set', tone: 'neutral' as const }
	);

	// One line about the pools: how many there are, or that they are unknown.
	const poolsSummary = $derived(
		data.pools === null
			? { text: 'unavailable', tone: 'neutral' as const }
			: data.pools.length === 0
				? { text: 'no pools', tone: 'neutral' as const }
				: {
						text: data.pools.length === 1 ? '1 pool' : `${data.pools.length} pools`,
						tone: 'success' as const
					}
	);

	// One line about updates: running, available, up to date, or unknown.
	const summary = $derived.by(() => {
		const status = data.updates;
		if (!status) return { text: 'status unavailable', tone: 'neutral' as const };
		const presentation = updatePresentation(status);
		return { text: presentation.pill, tone: presentation.tone };
	});
</script>

<svelte:head>
	<title>System — skali</title>
</svelte:head>

<PageHeader title="System">
	{#snippet subtitle()}
		{data.org.version ? `skalid ${data.org.version}` : 'installation settings'}
	{/snippet}
</PageHeader>

<div class="grid grid-cols-1 gap-3.5 @4xl:grid-cols-2 pb-6">
	<Card class="flex overflow-hidden">
		<a
			href={resolve('/(app)/system/nodes')}
			class="flex flex-1 items-center gap-3.5 px-5 py-4 transition-colors hover:bg-white/2"
		>
			<div
				class="bg-accent/10 text-accent-nav grid size-9 flex-none place-items-center rounded-[11px]"
			>
				<Server size={17} strokeWidth={1.75} />
			</div>
			<div class="flex min-w-0 flex-1 flex-col">
				<span class="text-text-primary text-lg font-medium">Nodes</span>
				<span class="text-text-muted text-md">Cluster machines, roles, usage and storage</span>
			</div>
			<Pill text={nodesSummary.text} tone={nodesSummary.tone} />
			<ChevronRight size={16} class="text-text-faint flex-none" />
		</a>
	</Card>
	<Card class="flex overflow-hidden">
		<a
			href={resolve('/(app)/system/updates')}
			class="flex flex-1 items-center gap-3.5 px-5 py-4 transition-colors hover:bg-white/2"
		>
			<div
				class="bg-accent/10 text-accent-nav grid size-9 flex-none place-items-center rounded-[11px]"
			>
				<RefreshCw size={17} strokeWidth={1.75} />
			</div>
			<div class="flex min-w-0 flex-1 flex-col">
				<span class="text-text-primary text-lg font-medium">Software update</span>
				<span class="text-text-muted text-md"
					>Check for releases, update the platform, channel and automatic updates</span
				>
			</div>
			<Pill text={summary.text} tone={summary.tone} />
			<ChevronRight size={16} class="text-text-faint flex-none" />
		</a>
	</Card>
	<Card class="flex overflow-hidden">
		<a
			href={resolve('/(app)/system/databases')}
			class="flex flex-1 items-center gap-3.5 px-5 py-4 transition-colors hover:bg-white/2"
		>
			<div
				class="bg-accent/10 text-accent-nav grid size-9 flex-none place-items-center rounded-[11px]"
			>
				<Database size={17} strokeWidth={1.75} />
			</div>
			<div class="flex min-w-0 flex-1 flex-col">
				<span class="text-text-primary text-lg font-medium">Databases</span>
				<span class="text-text-muted text-md"
					>Managed PostgreSQL pools: their usage, tuning and the databases on them</span
				>
			</div>
			<Pill text={poolsSummary.text} tone={poolsSummary.tone} />
			<ChevronRight size={16} class="text-text-faint flex-none" />
		</a>
	</Card>
	<Card class="flex overflow-hidden">
		<a
			href={resolve('/(app)/system/backups')}
			class="flex flex-1 items-center gap-3.5 px-5 py-4 transition-colors hover:bg-white/2"
		>
			<div
				class="bg-accent/10 text-accent-nav grid size-9 flex-none place-items-center rounded-[11px]"
			>
				<Archive size={17} strokeWidth={1.75} />
			</div>
			<div class="flex min-w-0 flex-1 flex-col">
				<span class="text-text-primary text-lg font-medium">Backup target</span>
				<span class="text-text-muted text-md"
					>The S3 location every snapshot and schedule writes to</span
				>
			</div>
			<Pill text={backupSummary.text} tone={backupSummary.tone} />
			<ChevronRight size={16} class="text-text-faint flex-none" />
		</a>
	</Card>
</div>
