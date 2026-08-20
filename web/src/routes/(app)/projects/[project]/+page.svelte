<script lang="ts">
	import Plus from '@lucide/svelte/icons/plus';
	import Container from '@lucide/svelte/icons/container';
	import type { StatCardData } from '$lib/models/view';
	import { envStatus } from '$lib/stores/envstatus.svelte';
	import { HEALTH_META } from '$lib/service-types';
	import { formatBytes, formatCount } from '$lib/format';
	import { currentTotal, windowTotal } from '$lib/types/metrics';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
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

	// All four tiles come from stored samples for the selected environment
	// (24h window): usage from the newest bucket, traffic from the edge
	// counters Traefik reports.
	const stats = $derived.by((): StatCardData[] => {
		const m = data.metrics;
		const apps = m?.applications ?? [];
		const noData: Pick<StatCardData, 'value' | 'note'> = { value: 'n/a', note: 'no data yet' };

		const requests = currentTotal(apps, (a) => a.edge?.requests ?? []);
		const requestsStat: StatCardData =
			requests != null && m
				? {
						label: 'REQUESTS',
						value: formatCount((requests / m.step_seconds) * 60),
						unit: '/min'
					}
				: { label: 'REQUESTS', ...noData };

		const cpu = currentTotal(apps, (a) => a.cpu_millicores);
		let cpuStat: StatCardData = { label: 'CPU', ...noData };
		if (cpu != null) {
			// The limit picks the unit so numerator and denominator match.
			if (limits.cpu != null) {
				cpuStat =
					limits.cpu >= 1000
						? {
								label: 'CPU',
								value: (cpu / 1000).toFixed(cpu < 100 ? 2 : 1),
								unit: `/ ${+(limits.cpu / 1000).toFixed(1)} cores`,
								progress: { pct: Math.min(100, (cpu / limits.cpu) * 100), class: 'bg-accent' }
							}
						: {
								label: 'CPU',
								value: `${Math.round(cpu)}`,
								unit: `/ ${Math.round(limits.cpu)} mCPU`,
								progress: { pct: Math.min(100, (cpu / limits.cpu) * 100), class: 'bg-accent' }
							};
			} else if (cpu >= 1000) {
				cpuStat = { label: 'CPU', value: (cpu / 1000).toFixed(1), unit: 'cores' };
			} else {
				cpuStat = { label: 'CPU', value: `${Math.round(cpu)}`, unit: 'mCPU' };
			}
		}

		const mem = currentTotal(apps, (a) => a.memory_bytes);
		let memStat: StatCardData = { label: 'MEMORY', ...noData };
		if (mem != null) {
			const memParts = formatBytes(mem).split(' ');
			memStat =
				limits.mem != null
					? {
							label: 'MEMORY',
							value: memParts[0],
							unit: `${memParts[1]} / ${formatBytes(limits.mem)}`,
							progress: { pct: Math.min(100, (mem / limits.mem) * 100), class: 'bg-accent' }
						}
					: { label: 'MEMORY', value: memParts[0], unit: memParts[1] };
		}

		// Response bytes served over the loaded 24h window: edge egress/day.
		const egress = windowTotal(apps, (a) => a.edge?.response_bytes);
		const egressParts = egress != null ? formatBytes(egress).split(' ') : null;
		const egressStat: StatCardData = egressParts
			? { label: 'EGRESS', value: egressParts[0], unit: `${egressParts[1]}/d` }
			: { label: 'EGRESS', ...noData };

		return [requestsStat, cpuStat, memStat, egressStat];
	});
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
