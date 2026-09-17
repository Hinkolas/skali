<script lang="ts">
	import Gauge from '@lucide/svelte/icons/gauge';
	import { api } from '$lib/api/client';
	import { formatBytes, formatCores, formatCount } from '$lib/format';
	import { pollMetrics } from '$lib/metrics-poll.svelte';
	import { stepLabel } from '$lib/models/pools';
	import { toChartPoints, type EnvironmentMetrics, type MetricsWindow } from '$lib/types/metrics';
	import Card from '$lib/components/ui/Card.svelte';
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
	import TimeSeriesChart from '$lib/components/ui/TimeSeriesChart.svelte';
	import WindowPicker from '$lib/components/ui/WindowPicker.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	// This concrete route shadows the [tab] stub for every service type, so
	// the stateful services' Metrics tabs keep their stub presentation here.
	const designed = $derived(data.service.type === 'application');

	let range = $state<MetricsWindow>('24h');
	// The route load seeds the 24h window; the poller fetches the others
	// and refreshes every 30s (the sampler cadence).
	const poller = pollMetrics<EnvironmentMetrics>({
		seed: () => data.metrics,
		range: () => range,
		load: (window) =>
			api.get<EnvironmentMetrics>(`/v1/environments/${data.env?.id}/metrics?window=${window}`),
		enabled: () => designed && data.env != null
	});
	const metrics = $derived(poller.metrics);

	const app = $derived(metrics?.applications.find((a) => a.key === data.service.key) ?? null);
	const timestamps = $derived(metrics?.timestamps ?? []);
	const hasData = $derived((app?.cpu_millicores ?? []).some((v) => v != null) || app?.edge != null);

	const cpuSeries = $derived(
		app
			? [
					{
						label: 'cpu',
						color: 'var(--color-chart-1)',
						points: toChartPoints(timestamps, app.cpu_millicores)
					}
				]
			: []
	);
	const memSeries = $derived(
		app
			? [
					{
						label: 'memory',
						color: 'var(--color-chart-2)',
						points: toChartPoints(timestamps, app.memory_bytes)
					}
				]
			: []
	);
	const requestSeries = $derived(
		app?.edge
			? [
					{
						label: 'requests',
						color: 'var(--color-chart-1)',
						points: toChartPoints(timestamps, app.edge.requests)
					}
				]
			: []
	);
	const bandwidthSeries = $derived(
		app?.edge
			? [
					{
						label: 'in',
						color: 'var(--color-chart-1)',
						points: toChartPoints(timestamps, app.edge.request_bytes)
					},
					{
						label: 'out',
						color: 'var(--color-chart-2)',
						points: toChartPoints(timestamps, app.edge.response_bytes)
					}
				]
			: []
	);
	// Edge samples are per-bucket counts; label them per interval.
	const step = $derived(stepLabel(metrics?.step_seconds ?? 0));
</script>

<svelte:head>
	<title>Metrics · {data.service.name} — skali</title>
</svelte:head>

{#if !designed}
	<EmptyState
		icon={Gauge}
		title="Metrics isn't available yet"
		description="this page will arrive in a future version"
	/>
{:else}
	<div class="mb-3.5 flex flex-wrap items-center gap-x-3 gap-y-2">
		<div class="flex min-w-56 flex-1 flex-wrap items-baseline gap-x-2.5 gap-y-0.5">
			<h2 class="text-text-primary text-xl font-semibold">Usage</h2>
			<div class="text-text-muted text-md">usage and edge traffic across the app's pods</div>
		</div>
		<div class="ml-auto">
			<WindowPicker value={range} onchange={(window) => (range = window)} />
		</div>
	</div>

	{#if hasData}
		<div class="grid grid-cols-1 gap-3.5 pb-6">
			<Card class="p-4.5">
				<div class="text-text-muted mb-2.5 text-sm tracking-wide uppercase">CPU</div>
				<TimeSeriesChart
					series={cpuSeries}
					height={242}
					formatValue={formatCores}
					label="CPU usage over the selected window"
				/>
			</Card>
			<Card class="p-4.5">
				<div class="text-text-muted mb-2.5 text-sm tracking-wide uppercase">Memory</div>
				<TimeSeriesChart
					series={memSeries}
					height={242}
					formatValue={formatBytes}
					label="Memory usage over the selected window"
				/>
			</Card>
			{#if app?.edge}
				<Card class="p-4.5">
					<div class="text-text-muted mb-2.5 text-sm tracking-wide uppercase">
						Requests <span class="normal-case">/ {step}</span>
					</div>
					<TimeSeriesChart
						series={requestSeries}
						height={242}
						formatValue={formatCount}
						label="Edge requests per bucket over the selected window"
					/>
				</Card>
				<Card class="p-4.5">
					<div class="text-text-muted mb-2.5 text-sm tracking-wide uppercase">
						Bandwidth <span class="normal-case">/ {step}</span>
					</div>
					<TimeSeriesChart
						series={bandwidthSeries}
						height={242}
						formatValue={formatBytes}
						label="Edge request and response bytes per bucket over the selected window"
					/>
				</Card>
			{/if}
		</div>
	{:else}
		<EmptyState
			icon={Gauge}
			title="No usage data yet"
			description="Samples appear about a minute after the app's pods start running."
		/>
	{/if}
{/if}
