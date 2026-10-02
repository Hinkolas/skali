// Usage stat tiles shared by the project overview (every application of the
// environment summed) and the application overview (one application). The
// tiles come from stored samples over the loaded window: the headline is the
// newest bucket, the sparkline the whole window. Traffic is the exception,
// its bytes are per-bucket deltas, so its headline is the window total.

import type { StatCardData } from '$lib/models/view';
import type { Application } from '$lib/types/definition';
import type { ServiceStatus } from '$lib/types/status';
import type { ApplicationSeries, EnvironmentMetrics } from '$lib/types/metrics';
import {
	currentTotal,
	seriesAverage,
	seriesPeak,
	sumSeries,
	toChartPoints,
	windowTotal
} from '$lib/types/metrics';
import { formatBytes, formatCount } from '$lib/format';

export interface UsageLimits {
	/** Summed CPU limit in millicores, null when no application declares one. */
	cpu: number | null;
	/** Summed memory limit in bytes, null when no application declares one. */
	mem: number | null;
}

/**
 * Declared per-replica limits from the draft definition, scaled by the live
 * pod count (falling back to minReplicas), summed across the applications
 * that declare them: the tiles' at-a-glance denominator. An approximation
 * when the running revision trails the draft, but honest enough for a
 * headline number.
 */
export function usageLimits(
	applications: Record<string, Application>,
	live: (key: string) => ServiceStatus | undefined
): UsageLimits {
	let cpu = 0;
	let mem = 0;
	for (const [key, app] of Object.entries(applications)) {
		const declared = app.resources?.limits;
		if (!declared) continue;
		const replicas = live(key)?.pods?.length || app.scaling?.minReplicas || 1;
		cpu += (declared.milliCpu ?? 0) * replicas;
		mem += (declared.memoryBytes ?? 0) * replicas;
	}
	return { cpu: cpu || null, mem: mem || null };
}

/** The four usage tiles for a set of application series; placeholders where no samples exist. */
export function usageStats(
	m: EnvironmentMetrics | null,
	apps: ApplicationSeries[],
	limits: UsageLimits
): StatCardData[] {
	const noData: Pick<StatCardData, 'value' | 'note'> = { value: 'n/a', note: 'no data yet' };
	const series = (values: (number | null)[]) =>
		m ? toChartPoints(m.timestamps, values) : undefined;
	const windowLabel = m?.window ?? '24h';

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
				{ label: 'total', value: `${formatCount(total ?? 0)} / ${windowLabel}` },
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
		const fmt = (v: number) => (inCores ? (v / 1000).toFixed(v < 100 ? 2 : 1) : `${Math.round(v)}`);
		const avg = seriesAverage(cpuSeries);
		const peak = seriesPeak(cpuSeries);
		cpuStat = {
			label: 'CPU',
			value: fmt(cpu),
			unit:
				limits.cpu != null
					? inCores
						? `/ ${+(limits.cpu / 1000).toFixed(1)} core${limits.cpu === 1000 ? '' : 's'}`
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
			unit: `${totalParts[1]} / ${windowLabel}`,
			sparkline: series(totalSeries),
			split: [
				{ label: 'in', value: formatBytes(inBytes ?? 0), class: 'bg-chart-1' },
				{ label: 'out', value: formatBytes(outBytes ?? 0), class: 'bg-chart-2' }
			]
		};
	}

	return [requestsStat, cpuStat, memStat, trafficStat];
}
