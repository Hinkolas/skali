<script lang="ts">
	import { resolve } from '$app/paths';
	import ChevronRight from '@lucide/svelte/icons/chevron-right';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import Server from '@lucide/svelte/icons/server';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Card from '$lib/components/ui/Card.svelte';
	import Pill from '$lib/components/ui/Pill.svelte';
	import { FEED_ERROR_PILL, operationSettled } from '$lib/types/updates';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	// One line about the cluster: how many nodes are online, or that the
	// cluster has not been observed yet.
	const nodesSummary = $derived.by(() => {
		const total = data.nodes.length;
		if (total === 0) {
			return data.nodesObservation?.state === 'fresh'
				? { text: 'no nodes', tone: 'neutral' as const }
				: { text: 'not observed', tone: 'neutral' as const };
		}
		const online = data.nodes.filter((n) => n.ready).length;
		return online === total
			? { text: `${total} online`, tone: 'success' as const }
			: { text: `${online}/${total} online`, tone: 'warning' as const };
	});

	// One line about updates: running, available, up to date, or unknown.
	const summary = $derived.by(() => {
		const status = data.updates;
		if (!status) return { text: 'status unavailable', tone: 'neutral' as const };
		if (status.operation && !operationSettled(status.operation)) {
			return {
				text: `updating to ${status.operation.target_version ?? ''}`,
				tone: 'warning' as const
			};
		}
		if (status.update_available && status.latest) {
			return { text: `${status.latest.version} available`, tone: 'warning' as const };
		}
		if (status.last_error) {
			const text = status.last_error_kind
				? FEED_ERROR_PILL[status.last_error_kind]
				: 'check failed';
			return { text, tone: 'warning' as const };
		}
		if (status.last_checked_at) return { text: 'up to date', tone: 'success' as const };
		return { text: 'not checked yet', tone: 'neutral' as const };
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

<div class="flex max-w-3xl flex-col gap-3.5 pb-6">
	<Card>
		<a
			href={resolve('/(app)/system/nodes')}
			class="flex items-center gap-3.5 px-5 py-4 transition-colors hover:bg-white/2"
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
	<Card>
		<a
			href={resolve('/(app)/system/updates')}
			class="flex items-center gap-3.5 px-5 py-4 transition-colors hover:bg-white/2"
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
</div>
