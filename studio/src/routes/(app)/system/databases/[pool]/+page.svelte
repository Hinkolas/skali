<script lang="ts">
	import Gauge from '@lucide/svelte/icons/gauge';
	import { api } from '$lib/api/client';
	import { pollMetrics } from '$lib/metrics-poll.svelte';
	import { poolCharts, poolStats } from '$lib/models/pools';
	import type { MetricsWindow } from '$lib/types/metrics';
	import type { PoolMetrics } from '$lib/types/pools';
	import PoolDatabasesCard from '$lib/components/system/PoolDatabasesCard.svelte';
	import PoolInstancesPanel from '$lib/components/system/PoolInstancesPanel.svelte';
	import Card from '$lib/components/ui/Card.svelte';
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
	import StatCard from '$lib/components/ui/StatCard.svelte';
	import TimeSeriesChart from '$lib/components/ui/TimeSeriesChart.svelte';
	import WindowPicker from '$lib/components/ui/WindowPicker.svelte';
	import type { PageData } from './$types';

	// The pool's overview: usage headlines, the instances and the databases
	// on it, then the usage charts over a selectable window.
	let { data }: { data: PageData } = $props();

	let range = $state<MetricsWindow>('24h');
	const poller = pollMetrics<PoolMetrics>({
		seed: () => data.metrics,
		range: () => range,
		load: (window) =>
			api.get<PoolMetrics>(
				`/v1/system/database-pools/${encodeURIComponent(data.pool.name)}/metrics?window=${window}`
			)
	});
	const metrics = $derived(poller.metrics);

	const stats = $derived(poolStats(metrics, data.pool));
	// A single sample anywhere in the window is enough to draw; before the
	// sampler has seen the pool every series is gaps.
	const hasData = $derived(
		metrics != null &&
			(metrics.cpu_millicores.some((v) => v != null) || metrics.connections.some((v) => v != null))
	);
	const charts = $derived(metrics && hasData ? poolCharts(metrics) : []);
</script>

<svelte:head>
	<title>{data.pool.name} · Databases — skali</title>
</svelte:head>

<div class="mb-6.5 grid grid-cols-2 gap-3.5 @4xl:grid-cols-4">
	{#each stats as stat (stat.label)}
		<StatCard {stat} />
	{/each}
</div>

<div class="mb-6.5 grid grid-cols-1 gap-3.5 @4xl:grid-cols-2">
	<PoolInstancesPanel pool={data.pool} />
	<PoolDatabasesCard pool={data.pool} />
</div>

<div class="mb-3.5 flex flex-wrap items-center gap-x-3 gap-y-2">
	<div class="flex min-w-56 flex-1 flex-wrap items-baseline gap-x-2.5 gap-y-0.5">
		<h2 class="text-text-primary text-xl font-semibold">Usage</h2>
		<div class="text-text-muted text-md">
			the instances summed; connections, transactions and cache from the primary
		</div>
	</div>
	<div class="ml-auto">
		<WindowPicker value={range} onchange={(window) => (range = window)} />
	</div>
</div>

{#if charts.length > 0}
	<div class="grid grid-cols-1 gap-3.5 pb-6 @4xl:grid-cols-2">
		{#each charts as chart (chart.id)}
			<Card class="p-4.5">
				<div class="text-text-muted mb-2.5 text-sm tracking-wide uppercase">
					{chart.title}{#if chart.suffix}
						<span class="normal-case">{chart.suffix}</span>{/if}
				</div>
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
			description="samples appear once the sampler observes the pool; an api-only daemon reports none"
		/>
	</div>
{/if}
