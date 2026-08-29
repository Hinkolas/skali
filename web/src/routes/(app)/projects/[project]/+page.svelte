<script lang="ts">
	import Plus from '@lucide/svelte/icons/plus';
	import Container from '@lucide/svelte/icons/container';
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
		toChartPoints
	} from '$lib/types/metrics';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
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
	// whole window summed across apps, and the storage tile reads the
	// footprint API instead (no series there).
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
			requestsStat = {
				label: 'REQUESTS',
				value: formatCount(requests * perMinute),
				unit: '/min',
				sparkline: series(requestSeries.map((v) => (v == null ? null : v * perMinute)))
			};
			// Compare the newest bucket with the one an hour earlier.
			const now = requestSeries.length - 1;
			const before = now - Math.round(3600 / m.step_seconds);
			const prev = before >= 0 ? requestSeries[before] : null;
			if (prev != null && prev > 0 && requestSeries[now] != null) {
				const delta = Math.round(((requestSeries[now]! - prev) / prev) * 100);
				requestsStat.chip = {
					text: `${delta >= 0 ? '+' : ''}${delta}%`,
					tone: delta >= 0 ? 'success' : 'neutral'
				};
				requestsStat.note = 'vs last hour';
			}
		}

		const cpuSeries = sumSeries(apps, (a) => a.cpu_millicores);
		const cpu = currentTotal(apps, (a) => a.cpu_millicores);
		let cpuStat: StatCardData = { label: 'CPU', ...noData };
		if (cpu != null) {
			// The limit picks the unit so numerator and denominator match.
			const inCores = limits.cpu != null ? limits.cpu >= 1000 : cpu >= 1000;
			cpuStat = {
				label: 'CPU',
				value: inCores ? (cpu / 1000).toFixed(cpu < 100 ? 2 : 1) : `${Math.round(cpu)}`,
				unit:
					limits.cpu != null
						? inCores
							? `/ ${+(limits.cpu / 1000).toFixed(1)} cores`
							: `/ ${Math.round(limits.cpu)} mCPU`
						: inCores
							? 'cores'
							: 'mCPU',
				sparkline: series(cpuSeries)
			};
		}

		const memSeries = sumSeries(apps, (a) => a.memory_bytes);
		const mem = currentTotal(apps, (a) => a.memory_bytes);
		let memStat: StatCardData = { label: 'MEMORY', ...noData };
		if (mem != null) {
			const memParts = formatBytes(mem).split(' ');
			memStat = {
				label: 'MEMORY',
				value: memParts[0],
				unit: limits.mem != null ? `${memParts[1]} / ${formatBytes(limits.mem)}` : memParts[1],
				sparkline: series(memSeries)
			};
		}

		// Best-known storage footprint of the selected environment: measured
		// where the sampler has real numbers, reserved sizes elsewhere.
		let storageStat: StatCardData = { label: 'STORAGE', ...noData };
		if (envStorage.length > 0) {
			const used = envStorage.reduce((acc, s) => acc + storageFootprint(s), 0);
			const declared = envStorage.reduce((acc, s) => acc + s.capacity_bytes, 0);
			const parts = formatBytes(used).split(' ');
			const kinds = Object.entries(storageKinds).filter(([, v]) => v > 0).length;
			storageStat =
				declared > 0
					? {
							label: 'STORAGE',
							value: parts[0],
							unit: `${parts[1]} / ${formatBytes(declared)}`,
							progress: { pct: Math.min(100, (used / declared) * 100), class: 'bg-accent' },
							note: `${envStorage.length} service${envStorage.length === 1 ? '' : 's'}, ${kinds} kind${kinds === 1 ? '' : 's'}`
						}
					: { label: 'STORAGE', value: parts[0], unit: parts[1] };
		}

		return [requestsStat, cpuStat, memStat, storageStat];
	});

	// Storage rows of the selected environment, largest footprint first.
	const envStorage = $derived(
		storageForEnvironment(data.storage, data.env?.id ?? null).toSorted(
			(a, b) => storageFootprint(b) - storageFootprint(a)
		)
	);
	const storageKinds = $derived(storageByKind(envStorage));
	const storageTotal = $derived(envStorage.reduce((acc, s) => acc + storageFootprint(s), 0));
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
	<div class="border-border-subtle mb-6.5 rounded-[15px] border px-4.5 py-4">
		<StackedBar
			segments={Object.entries(STORAGE_KIND_META).map(([kind, meta]) => ({
				label: `${meta.label} ${formatBytes(storageKinds[kind as keyof typeof storageKinds] ?? 0)}`,
				value: storageKinds[kind as keyof typeof storageKinds] ?? 0,
				class: meta.class
			}))}
			total={storageTotal}
		/>
		<div class="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1">
			{#each Object.entries(STORAGE_KIND_META) as [kind, meta] (kind)}
				{#if (storageKinds[kind as keyof typeof storageKinds] ?? 0) > 0}
					<span class="flex items-center gap-1.5 font-mono text-text-faint text-xs">
						<span class="size-[8px] rounded-full {meta.class}"></span>
						{meta.label}
						{formatBytes(storageKinds[kind as keyof typeof storageKinds])}
					</span>
				{/if}
			{/each}
		</div>
		<div class="border-border-subtle mt-3.5 border-t">
			{#each envStorage as entry (`${entry.kind}:${entry.service_key}`)}
				{@const meta = STORAGE_KIND_META[entry.kind]}
				<div
					class="border-border-subtle grid grid-cols-[1.6fr_1fr_1.4fr] items-center gap-3 border-b py-2.5 last:border-0"
				>
					<div class="flex items-center gap-2">
						<span class="size-[8px] flex-none rounded-full {meta.class}"></span>
						<span class="font-mono text-text-primary truncate text-sm">{entry.service_key}</span>
					</div>
					<div class="font-mono text-text-muted text-sm">
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
							<span class="font-mono text-text-faint flex-none text-xs">
								of {formatBytes(entry.capacity_bytes)}
							</span>
						{/if}
					</div>
				</div>
			{/each}
		</div>
	</div>
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
