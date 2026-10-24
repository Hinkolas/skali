<script lang="ts">
	import Gauge from '@lucide/svelte/icons/gauge';
	import HardDrive from '@lucide/svelte/icons/hard-drive';
	import { api } from '$lib/api/client';
	import { formatBytes, formatDateTime, relativeTime } from '$lib/format';
	import { pollMetrics } from '$lib/metrics-poll.svelte';
	import {
		kubernetesLabel,
		nodeCharts,
		nodeSeries,
		nodeStats,
		nodeStatus
	} from '$lib/models/nodes';
	import { NODE_ROLE_META, STORAGE_CATEGORY_META } from '$lib/service-types';
	import type { MetricsWindow, NodeMetrics } from '$lib/types/metrics';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Card from '$lib/components/ui/Card.svelte';
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
	import KeyValueRow from '$lib/components/ui/KeyValueRow.svelte';
	import Pill from '$lib/components/ui/Pill.svelte';
	import StackedBar from '$lib/components/ui/StackedBar.svelte';
	import StatCard from '$lib/components/ui/StatCard.svelte';
	import TimeSeriesChart from '$lib/components/ui/TimeSeriesChart.svelte';
	import WindowPicker from '$lib/components/ui/WindowPicker.svelte';
	import type { PageData } from './$types';

	// One node: what it uses now, what it is, where its disk goes, and how
	// CPU and memory moved over a selectable window.
	let { data }: { data: PageData } = $props();

	let range = $state<MetricsWindow>('24h');
	const poller = pollMetrics<NodeMetrics>({
		seed: () => data.metrics,
		range: () => range,
		load: (window) => api.get<NodeMetrics>(`/v1/nodes/metrics?window=${window}`)
	});
	const metrics = $derived(poller.metrics);
	const series = $derived(nodeSeries(metrics, data.node.name));

	const status = $derived(nodeStatus(data.node));
	const stats = $derived(nodeStats(metrics, series, data.storage));
	const hasData = $derived(
		series != null &&
			(series.cpu_millicores.some((v) => v != null) || series.memory_bytes.some((v) => v != null))
	);
	const charts = $derived(metrics && series && hasData ? nodeCharts(metrics, series) : []);

	const platform = $derived(
		[data.node.arch, data.node.os].filter(Boolean).join(' · ') || 'platform not reported'
	);

	const storageRows = $derived.by(() => {
		const storage = data.storage;
		if (!storage) return [];
		const categories = storage.categories as unknown as Record<string, number>;
		return STORAGE_CATEGORY_META.map((meta) => ({
			...meta,
			value: categories[meta.key] ?? 0
		}));
	});
</script>

<svelte:head>
	<title>{data.node.name} · Nodes — Skali</title>
</svelte:head>

<PageHeader title={data.node.name} mono>
	{#snippet titleTrailing()}
		<Pill text={status.text} tone={status.tone} />
	{/snippet}
	{#snippet subtitle()}
		<span class="flex flex-wrap items-center gap-1">
			{#each [data.node.role, ...data.node.capabilities] as role (role)}
				{@const meta = NODE_ROLE_META[role] ?? NODE_ROLE_META.agent}
				<span class="font-mono rounded-full px-2 py-0.5 text-2xs {meta.text} {meta.bg}">
					{role}
				</span>
			{/each}
		</span>
		<span class="font-mono text-text-faint text-md">{platform}</span>
	{/snippet}
</PageHeader>

<div class="mb-6.5 grid grid-cols-1 gap-3.5 @2xl:grid-cols-3">
	{#each stats as stat (stat.label)}
		<StatCard {stat} />
	{/each}
</div>

<div class="mb-6.5 grid grid-cols-1 gap-3.5 @4xl:grid-cols-2">
	<Card class="flex flex-col p-5">
		<h3 class="text-text-primary mb-3.5 text-xl font-semibold">Node</h3>
		<div class="flex flex-col">
			<KeyValueRow k="Role" v={data.node.role} labelWidth="w-36" />
			<KeyValueRow
				k="Capabilities"
				v={data.node.capabilities.length ? data.node.capabilities.join(', ') : 'none'}
				labelWidth="w-36"
			/>
			<KeyValueRow k="Architecture" v={data.node.arch ?? 'unknown'} labelWidth="w-36" />
			<KeyValueRow k="OS" v={data.node.os ?? 'unknown'} labelWidth="w-36" />
			<KeyValueRow k="Internal IP" v={data.node.internal_ip ?? 'unknown'} labelWidth="w-36" />
			<KeyValueRow k="Public IP" v={data.node.external_ip ?? 'none'} labelWidth="w-36" />
			<KeyValueRow k="Host agent" v={data.agentVersion ?? 'not reported'} labelWidth="w-36" />
			<KeyValueRow
				k="Kubernetes"
				v={data.node.kubelet_version ? kubernetesLabel(data.node.kubelet_version) : 'unknown'}
				labelWidth="w-36"
			/>
			<KeyValueRow
				k="Scheduling"
				v={data.node.schedulable ? 'schedulable' : 'cordoned: takes no new pods'}
				labelWidth="w-36"
			/>
			<KeyValueRow
				k="Last heartbeat"
				v={data.node.last_heartbeat ? relativeTime(data.node.last_heartbeat) : 'never'}
				labelWidth="w-36"
			/>
		</div>
	</Card>

	<Card class="flex flex-col p-5">
		<div class="mb-3.5 flex items-baseline gap-2.5">
			<h3 class="text-text-primary text-xl font-semibold">Storage</h3>
			{#if data.storage}
				<span class="font-mono text-text-faint text-xs">
					sampled {relativeTime(data.storage.sampled_at)}
				</span>
			{/if}
		</div>
		{#if data.storage}
			{@const storage = data.storage}
			<div class="mb-1.5 flex items-baseline justify-between gap-3">
				<span class="text-text-primary font-mono text-md">
					{formatBytes(storage.used_bytes)} used
				</span>
				<span class="text-text-muted font-mono text-sm">
					{formatBytes(storage.available_bytes)} free of {formatBytes(storage.capacity_bytes)}
				</span>
			</div>
			<StackedBar
				segments={storageRows.map((row) => ({
					label: `${row.label} ${formatBytes(row.value)}`,
					value: row.value,
					class: row.class
				}))}
				total={storage.capacity_bytes}
				class="h-2.5"
			/>
			<div class="mt-3.5 flex flex-col">
				{#each storageRows as row (row.key)}
					<div
						class="border-border-subtle flex items-center gap-2.5 border-b py-2 text-md last:border-0"
					>
						<span class="size-[8px] flex-none rounded-full {row.class}"></span>
						<span class="text-text-faint flex-1">{row.label}</span>
						<span class="font-mono text-text-secondary">{formatBytes(row.value)}</span>
					</div>
				{/each}
			</div>
			<p class="text-text-faint mt-3 text-sm">
				Databases and objects are logical-size estimates; system is what remains of the used space.
				Sampled {formatDateTime(storage.sampled_at)}.
			</p>
		{:else}
			<EmptyState
				icon={HardDrive}
				title="No storage sample yet"
				description="the sampler measures the disk once the host agent reports"
				class="min-h-0 flex-1"
			/>
		{/if}
	</Card>
</div>

<div class="mb-3.5 flex flex-wrap items-center gap-x-3 gap-y-2">
	<div class="flex min-w-56 flex-1 flex-wrap items-baseline gap-x-2.5 gap-y-0.5">
		<h2 class="text-text-primary text-xl font-semibold">Usage</h2>
		<div class="text-text-muted text-md">up to what the node can allocate</div>
	</div>
	<div class="ml-auto">
		<WindowPicker value={range} onchange={(window) => (range = window)} />
	</div>
</div>

{#if charts.length > 0}
	<div class="grid grid-cols-1 gap-3.5 pb-6 @4xl:grid-cols-2">
		{#each charts as chart (chart.id)}
			<Card class="p-4.5">
				<div class="text-text-muted mb-2.5 text-sm tracking-wide uppercase">{chart.title}</div>
				<TimeSeriesChart
					series={chart.series}
					height={242}
					formatValue={chart.format}
					yDomain={chart.yDomain}
					label={chart.label}
				/>
			</Card>
		{/each}
	</div>
{:else}
	<div class="pb-6">
		<EmptyState
			icon={Gauge}
			title="No usage data yet"
			description="samples appear once the sampler observes the node; an api-only daemon reports none"
		/>
	</div>
{/if}
