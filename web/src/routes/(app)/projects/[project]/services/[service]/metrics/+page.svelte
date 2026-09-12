<script lang="ts">
	import { untrack } from 'svelte';
	import Gauge from '@lucide/svelte/icons/gauge';
	import { api } from '$lib/api/client';
	import { formatBytes, formatCores, formatCount } from '$lib/format';
	import { toChartPoints, type EnvironmentMetrics, type MetricsWindow } from '$lib/types/metrics';
	import Card from '$lib/components/ui/Card.svelte';
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
	// The route load seeds the 24h window; every other window, and the
	// seeded one once another has been shown, is fetched here and refreshed
	// every 30s (the sampler cadence). While a fetch is in flight the seed
	// serves its own window at once and any other window keeps the last
	// series on screen instead of flashing empty.
	let fetched = $state<EnvironmentMetrics | null>(null);
	const metrics = $derived.by(() => {
		if (fetched?.window === range) return fetched;
		if (data.metrics?.window === range) return data.metrics;
		return fetched ?? data.metrics;
	});

	$effect(() => {
		const envId = data.env?.id;
		if (!designed || !envId) return;
		const selected = range;
		// `fetched` is read untracked: a completed fetch must not re-run
		// this effect, or it would fetch again in a loop.
		const seeded = selected === (data.metrics?.window ?? '24h') && untrack(() => fetched) === null;
		let cancelled = false;
		const fetchSeries = async () => {
			try {
				const fresh = await api.get<EnvironmentMetrics>(
					`/v1/environments/${envId}/metrics?window=${selected}`
				);
				if (!cancelled) fetched = fresh;
			} catch {
				// Keep the last good series; the next tick retries.
			}
		};
		if (!seeded) void fetchSeries();
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
		title="Metrics isn't available yet"
		description="this page will arrive in a future version"
	/>
{:else}
	<div class="mb-3.5 flex flex-wrap items-center gap-x-3 gap-y-2">
		<div class="flex min-w-56 flex-1 flex-wrap items-baseline gap-x-2.5 gap-y-0.5">
			<h2 class="text-text-primary text-xl font-semibold">Usage</h2>
			<div class="text-text-muted text-md">usage and edge traffic across the app's pods</div>
		</div>
		<div class="ml-auto flex flex-none items-center gap-1" role="group" aria-label="Window">
			{#each WINDOWS as option (option.value)}
				<button
					type="button"
					onclick={() => (range = option.value)}
					class="cursor-pointer rounded-full px-3 py-1 text-sm whitespace-nowrap transition-colors {range ===
					option.value
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
						Requests <span class="normal-case">/ {stepLabel}</span>
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
						Bandwidth <span class="normal-case">/ {stepLabel}</span>
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
