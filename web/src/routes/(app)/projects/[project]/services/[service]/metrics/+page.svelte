<script lang="ts">
	import Gauge from '@lucide/svelte/icons/gauge';
	import { api } from '$lib/api/client';
	import { formatBytes, formatCores, formatCount } from '$lib/format';
	import { toChartPoints, type EnvironmentMetrics, type MetricsWindow } from '$lib/types/metrics';
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
	import TimeSeriesChart from '$lib/components/ui/TimeSeriesChart.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	// This concrete route shadows the [tab] stub for every service type, so
	// the stateful services' Metrics tabs keep their stub presentation here.
	const designed = $derived(data.service.type === 'application');

	const WINDOWS: { value: MetricsWindow; label: string }[] = [
		{ value: '1h', label: '1 hour' },
		{ value: '24h', label: '24 hours' },
		{ value: '7d', label: '7 days' }
	];

	let range = $state<MetricsWindow>('24h');
	// Seeded from the server load, then overwritten by refetches on window
	// change and every 30s (the sampler cadence); a navigation reseeds it.
	let metrics = $derived(data.metrics);

	$effect(() => {
		const envId = data.env?.id;
		if (!designed || !envId) return;
		const selected = range;
		let cancelled = false;
		const fetchSeries = async () => {
			try {
				const fresh = await api.get<EnvironmentMetrics>(
					`/v1/environments/${envId}/metrics?window=${selected}`
				);
				if (!cancelled) metrics = fresh;
			} catch {
				// Keep the last good series; the next tick retries.
			}
		};
		if (selected !== (data.metrics?.window ?? '24h')) void fetchSeries();
		const timer = setInterval(fetchSeries, 30_000);
		return () => {
			cancelled = true;
			clearInterval(timer);
		};
	});

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
	const stepLabel = $derived.by(() => {
		const step = metrics?.step_seconds ?? 0;
		if (step >= 3600) return `${step / 3600}h`;
		if (step >= 60) return `${step / 60}min`;
		return `${step}s`;
	});
</script>

<svelte:head>
	<title>Metrics · {data.service.name} — skali</title>
</svelte:head>

{#if !designed}
	<EmptyState
		icon={Gauge}
		title="Metrics isn't part of the prototype yet"
		description="this page will land in a later design pass"
	/>
{:else}
	<div class="mb-3.5 flex items-baseline gap-2.5">
		<h2 class="text-text-primary text-xl font-semibold">Usage</h2>
		<div class="text-text-muted text-md">usage and edge traffic across the app's pods</div>
		<div class="ml-auto flex items-center gap-1" role="group" aria-label="Window">
			{#each WINDOWS as option (option.value)}
				<button
					type="button"
					onclick={() => (range = option.value)}
					class="rounded-full px-3 py-1 text-sm transition-colors {range === option.value
						? 'bg-white/8 text-text-primary'
						: 'text-text-tertiary hover:text-text-secondary'}"
				>
					{option.label}
				</button>
			{/each}
		</div>
	</div>

	{#if hasData}
		<div class="grid grid-cols-1 gap-3.5 pb-6">
			<div class="border-border-subtle rounded-[15px] border p-4.5">
				<div class="text-text-muted mb-2.5 text-sm tracking-wide uppercase">CPU</div>
				<TimeSeriesChart
					series={cpuSeries}
					formatValue={formatCores}
					label="CPU usage over the selected window"
				/>
			</div>
			<div class="border-border-subtle rounded-[15px] border p-4.5">
				<div class="text-text-muted mb-2.5 text-sm tracking-wide uppercase">Memory</div>
				<TimeSeriesChart
					series={memSeries}
					formatValue={formatBytes}
					label="Memory usage over the selected window"
				/>
			</div>
			{#if app?.edge}
				<div class="border-border-subtle rounded-[15px] border p-4.5">
					<div class="text-text-muted mb-2.5 text-sm tracking-wide uppercase">
						Requests <span class="normal-case">/ {stepLabel}</span>
					</div>
					<TimeSeriesChart
						series={requestSeries}
						formatValue={formatCount}
						label="Edge requests per bucket over the selected window"
					/>
				</div>
				<div class="border-border-subtle rounded-[15px] border p-4.5">
					<div class="text-text-muted mb-2.5 text-sm tracking-wide uppercase">
						Bandwidth <span class="normal-case">/ {stepLabel}</span>
					</div>
					<TimeSeriesChart
						series={bandwidthSeries}
						formatValue={formatBytes}
						label="Edge request and response bytes per bucket over the selected window"
					/>
				</div>
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
