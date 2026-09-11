<script lang="ts">
	import Plus from '@lucide/svelte/icons/plus';
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import Container from '@lucide/svelte/icons/container';
	import { slide } from 'svelte/transition';
	import type { StatCardData } from '$lib/models/view';
	import { envStatus } from '$lib/stores/envstatus.svelte';
	import { HEALTH_META, STORAGE_KIND_META } from '$lib/service-types';
	import { formatBytes, formatCount } from '$lib/format';
	import {
		currentTotal,
		storageByKind,
		storageFootprint,
		storageForEnvironment,
		sumSeries,
		toChartPoints,
		seriesAverage,
		seriesPeak,
		windowTotal
	} from '$lib/types/metrics';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Card from '$lib/components/ui/Card.svelte';
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
	import ProgressBar from '$lib/components/ui/ProgressBar.svelte';
	import StackedBar from '$lib/components/ui/StackedBar.svelte';
	import StatCard from '$lib/components/ui/StatCard.svelte';
	import ServiceCard from '$lib/components/service/ServiceCard.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	const status = $derived(envStatus.doc ?? data.status);

	const title = $derived(data.project.display_name || data.project.name);

	const subtitleText = $derived.by(() => {
		if (!data.env) return 'no environments yet';
		const parts = [`${data.services.length} service${data.services.length === 1 ? '' : 's'}`];
		if (status) {
			const healthy = status.services.filter((s) => s.health === 'healthy').length;
			if (status.services.length > 0) parts.push(`${healthy}/${status.services.length} healthy`);
			parts.push(status.state);
		}
		return parts.join(' · ');
	});
	const subtitleDot = $derived.by(() => {
		if (!status || status.services.length === 0) return HEALTH_META.unknown.dot;
		if (status.services.every((s) => s.health === 'healthy')) return HEALTH_META.healthy.dot;
		if (status.services.some((s) => s.health === 'unhealthy')) return HEALTH_META.unhealthy.dot;
		return HEALTH_META.degraded.dot;
	});

	// Declared per-replica limits from the draft definition, scaled by the
	// live pod count (falling back to minReplicas), summed across the apps
	// that declare them: the tiles' at-a-glance denominator. An approximation
	// when the running revision trails the draft, but honest enough for a
	// headline number.
	const limits = $derived.by(() => {
		let cpu = 0;
		let mem = 0;
		for (const [key, app] of Object.entries(data.definition?.applications ?? {})) {
			const declared = app.resources?.limits;
			if (!declared) continue;
			const live = status?.services.find((s) => s.type === 'application' && s.key === key);
			const replicas = live?.pods?.length || app.scaling?.minReplicas || 1;
			cpu += (declared.milliCpu ?? 0) * replicas;
			mem += (declared.memoryBytes ?? 0) * replicas;
		}
		return { cpu: cpu || null, mem: mem || null };
	});

	// The four tiles come from stored samples for the selected environment
	// (24h window): the headline is the newest bucket, the sparkline is the
	// whole window summed across apps. Traffic is the exception: bytes are
	// per-bucket deltas, so its headline is the window total. Storage has
	// its own section below and no tile.
	const stats = $derived.by((): StatCardData[] => {
		const m = data.metrics;
		const apps = m?.applications ?? [];
		const noData: Pick<StatCardData, 'value' | 'note'> = { value: 'n/a', note: 'no data yet' };
		const series = (values: (number | null)[]) =>
			m ? toChartPoints(m.timestamps, values) : undefined;

		const requestSeries = sumSeries(apps, (a) => a.edge?.requests);
		const requests = currentTotal(apps, (a) => a.edge?.requests ?? []);
		let requestsStat: StatCardData = { label: 'REQUESTS', ...noData };
		if (requests != null && m) {
			const perMinute = 60 / m.step_seconds;
			const rateSeries = requestSeries.map((v) => (v == null ? null : v * perMinute));
			const total = windowTotal(apps, (a) => a.edge?.requests);
			const peak = seriesPeak(rateSeries);
			requestsStat = {
				label: 'REQUESTS',
				value: formatCount(requests * perMinute),
				unit: '/min',
				sparkline: series(rateSeries),
				split: [
					{ label: 'total', value: `${formatCount(total ?? 0)} / 24h` },
					{ label: 'peak', value: `${formatCount(peak ?? 0)} /min` }
				]
			};
		}

		const cpuSeries = sumSeries(apps, (a) => a.cpu_millicores);
		const cpu = currentTotal(apps, (a) => a.cpu_millicores);
		let cpuStat: StatCardData = { label: 'CPU', ...noData };
		if (cpu != null) {
			// The limit picks the unit so numerator, denominator, and the sub
			// stats all match.
			const inCores = limits.cpu != null ? limits.cpu >= 1000 : cpu >= 1000;
			const fmt = (v: number) =>
				inCores ? (v / 1000).toFixed(v < 100 ? 2 : 1) : `${Math.round(v)}`;
			const avg = seriesAverage(cpuSeries);
			const peak = seriesPeak(cpuSeries);
			cpuStat = {
				label: 'CPU',
				value: fmt(cpu),
				unit:
					limits.cpu != null
						? inCores
							? `/ ${+(limits.cpu / 1000).toFixed(1)} cores`
							: `/ ${Math.round(limits.cpu)} mCPU`
						: inCores
							? 'cores'
							: 'mCPU',
				sparkline: series(cpuSeries),
				split: [
					{ label: 'avg', value: fmt(avg ?? cpu) },
					{ label: 'peak', value: fmt(peak ?? cpu) }
				]
			};
		}

		const memSeries = sumSeries(apps, (a) => a.memory_bytes);
		const mem = currentTotal(apps, (a) => a.memory_bytes);
		let memStat: StatCardData = { label: 'MEMORY', ...noData };
		if (mem != null) {
			const memParts = formatBytes(mem).split(' ');
			const avg = seriesAverage(memSeries);
			const peak = seriesPeak(memSeries);
			memStat = {
				label: 'MEMORY',
				value: memParts[0],
				unit: limits.mem != null ? `${memParts[1]} / ${formatBytes(limits.mem)}` : memParts[1],
				sparkline: series(memSeries),
				split: [
					{ label: 'avg', value: formatBytes(avg ?? mem) },
					{ label: 'peak', value: formatBytes(peak ?? mem) }
				]
			};
		}

		// Edge traffic in both directions over the window: request bytes are
		// what clients sent in, response bytes what the apps sent out. Only
		// traffic through the edge proxy counts (public routes), not internal
		// service-to-service, database, or bucket transfers.
		const inBytes = windowTotal(apps, (a) => a.edge?.request_bytes);
		const outBytes = windowTotal(apps, (a) => a.edge?.response_bytes);
		let trafficStat: StatCardData = { label: 'TRAFFIC', ...noData };
		if (inBytes != null || outBytes != null) {
			const inSeries = sumSeries(apps, (a) => a.edge?.request_bytes);
			const outSeries = sumSeries(apps, (a) => a.edge?.response_bytes);
			const totalSeries = inSeries.map((v, i) => {
				const o = outSeries[i];
				return v == null && o == null ? null : (v ?? 0) + (o ?? 0);
			});
			const totalParts = formatBytes((inBytes ?? 0) + (outBytes ?? 0)).split(' ');
			trafficStat = {
				label: 'TRAFFIC',
				value: totalParts[0],
				unit: `${totalParts[1]} / 24h`,
				sparkline: series(totalSeries),
				split: [
					{ label: 'in', value: formatBytes(inBytes ?? 0), class: 'bg-chart-1' },
					{ label: 'out', value: formatBytes(outBytes ?? 0), class: 'bg-chart-2' }
				]
			};
		}

		return [requestsStat, cpuStat, memStat, trafficStat];
	});

	// Storage rows of the selected environment, largest footprint first.
	const envStorage = $derived(
		storageForEnvironment(data.storage, data.env?.id ?? null).toSorted(
			(a, b) => storageFootprint(b) - storageFootprint(a)
		)
	);
	const storageKinds = $derived(storageByKind(envStorage));
	const storageTotal = $derived(envStorage.reduce((acc, s) => acc + storageFootprint(s), 0));
	// Declared capacity across the environment's services: the summary bar's
	// full width, so the unfilled track is what is still free. Falls back to
	// the footprint (a full bar) where nothing declares a size.
	const storageCapacity = $derived(envStorage.reduce((acc, s) => acc + s.capacity_bytes, 0));

	// The per-service breakdown is folded away by default: the summary bar
	// answers the common question, and with many services the row list
	// would otherwise push the service cards below the fold.
	let storageOpen = $state(false);
</script>

<svelte:head>
	<title>{title} — skali</title>
</svelte:head>

<!-- Promote and Redeploy are environment-wide and live in the topbar next
     to the environment breadcrumb. -->
<PageHeader {title}>
	{#snippet subtitle()}
		<span class="size-[8px] flex-none rounded-full {subtitleDot}"></span>
		{subtitleText}
	{/snippet}
</PageHeader>

<div class="mb-6.5 grid grid-cols-4 gap-3.5">
	{#each stats as stat (stat.label)}
		<StatCard {stat} />
	{/each}
</div>

{#if envStorage.length > 0}
	<div class="mb-3.5 flex items-baseline gap-2.5">
		<h2 class="text-text-primary text-xl font-semibold">Storage</h2>
		<div class="text-text-muted text-md">env {data.env?.name ?? 'none'}</div>
	</div>
	<Card class="mb-6.5">
		<button
			type="button"
			onclick={() => (storageOpen = !storageOpen)}
			aria-expanded={storageOpen}
			aria-controls="storage-breakdown"
			class="flex w-full cursor-pointer items-center gap-4 px-4.5 py-4 text-left"
		>
			<div class="min-w-0 flex-1">
				<StackedBar
					segments={Object.entries(STORAGE_KIND_META).map(([kind, meta]) => ({
						label: `${meta.label} ${formatBytes(storageKinds[kind as keyof typeof storageKinds] ?? 0)}`,
						value: storageKinds[kind as keyof typeof storageKinds] ?? 0,
						class: meta.class
					}))}
					total={storageCapacity > 0 ? storageCapacity : storageTotal}
					class="h-2.5"
				/>
				<div class="mt-2.5 flex flex-wrap items-center gap-x-4 gap-y-1">
					{#each Object.entries(STORAGE_KIND_META) as [kind, meta] (kind)}
						{#if (storageKinds[kind as keyof typeof storageKinds] ?? 0) > 0}
							<span class="text-text-faint flex items-center gap-1.5 font-mono text-xs">
								<span class="size-[8px] rounded-full {meta.class}"></span>
								{meta.label}
								{formatBytes(storageKinds[kind as keyof typeof storageKinds])}
							</span>
						{/if}
					{/each}
					{#if storageCapacity > 0}
						<span class="text-text-muted ml-auto font-mono text-xs">
							{formatBytes(storageTotal)} of {formatBytes(storageCapacity)}
						</span>
					{/if}
				</div>
			</div>
			<span class="text-text-muted flex flex-none items-center gap-1.5 text-md">
				{envStorage.length} service{envStorage.length === 1 ? '' : 's'}
				<ChevronDown
					size={16}
					class="text-text-faint transition-transform duration-200 {storageOpen
						? 'rotate-180'
						: ''}"
				/>
			</span>
		</button>
		{#if storageOpen}
			<div
				id="storage-breakdown"
				transition:slide={{ duration: 180 }}
				class="border-border-subtle border-t px-4.5 pt-1 pb-1.5"
			>
				{#each envStorage as entry (`${entry.kind}:${entry.service_key}`)}
					{@const meta = STORAGE_KIND_META[entry.kind]}
					<div
						class="border-border-subtle grid grid-cols-[1.6fr_1fr_1.4fr] items-center gap-3 border-b py-2.5 last:border-0"
					>
						<div class="flex items-center gap-2">
							<span class="size-[8px] flex-none rounded-full {meta.class}"></span>
							<span class="text-text-primary truncate font-mono text-sm">{entry.service_key}</span>
						</div>
						<div class="text-text-muted font-mono text-sm">
							{#if entry.used_bytes != null}
								{formatBytes(entry.used_bytes)}
							{:else}
								reserved {formatBytes(entry.capacity_bytes)}
							{/if}
						</div>
						<div class="flex items-center gap-2.5">
							{#if entry.used_bytes != null && entry.capacity_bytes > 0}
								<div class="min-w-0 flex-1">
									<ProgressBar
										pct={Math.min(100, (entry.used_bytes / entry.capacity_bytes) * 100)}
										class={meta.class}
									/>
								</div>
								<span class="text-text-faint flex-none font-mono text-xs">
									of {formatBytes(entry.capacity_bytes)}
								</span>
							{/if}
						</div>
					</div>
				{/each}
			</div>
		{/if}
	</Card>
{/if}

<div class="mb-3.5 flex items-baseline gap-2.5">
	<h2 class="text-text-primary text-xl font-semibold">Services</h2>
	<div class="text-text-muted text-md">
		{data.services.length === 0 ? 'defined in skali.yaml' : `env ${data.env?.name ?? 'none'}`}
	</div>
</div>

{#if data.services.length > 0}
	<div class="grid grid-cols-3 gap-3.5 pb-6">
		{#each data.services as service (`${service.type}:${service.key}`)}
			<ServiceCard project={data.project} {service} />
		{/each}
		<button
			type="button"
			disabled
			title="Services are defined in skali.yaml; adding them here is coming soon"
			class="border-border-strong text-text-faint grid min-h-[132px] cursor-default place-items-center rounded-[15px] border border-dashed text-base opacity-60"
		>
			<span class="flex items-center gap-1.5"><Plus size={15} /> Add a service</span>
		</button>
	</div>
{:else}
	<div class="pb-6">
		<EmptyState
			icon={Container}
			title="No services defined"
			description="Add services to skali.yaml and run `skali deploy`."
		/>
	</div>
{/if}
