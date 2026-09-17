// The pool overview's stat tiles and chart series, built from one pool's
// usage series. CPU and memory sum the instances; connections, the
// transaction and block counters and the database size are the primary's
// exporter view. The headline is the newest sample, the sparkline the
// whole window.

import type { ChartSeries } from '$lib/charts';
import type { StatCardData } from '$lib/models/view';
import { footprintStat } from '$lib/models/claims';
import { formatBytes, formatCount } from '$lib/format';
import { seriesAverage, seriesPeak, toChartPoints } from '$lib/types/metrics';
import type { DatabasePool, PoolMetrics } from '$lib/types/pools';

/** Per-bucket transactions: commits plus rollbacks; null where either is a gap. */
export function transactionSeries(m: PoolMetrics): (number | null)[] {
	return m.commits.map((commits, i) => {
		const rollbacks = m.rollbacks[i];
		return commits == null && rollbacks == null ? null : (commits ?? 0) + (rollbacks ?? 0);
	});
}

/** The four tiles: connections against the limit, CPU, memory against the
 * budget, and the databases' size against the volume. */
export function poolStats(m: PoolMetrics | null, pool: DatabasePool): StatCardData[] {
	const noData: Pick<StatCardData, 'value' | 'note'> = { value: 'n/a', note: 'no data yet' };
	const series = (values: (number | null)[]) =>
		m ? toChartPoints(m.timestamps, values) : undefined;
	const current = m?.current ?? null;

	let connectionsStat: StatCardData = { label: 'CONNECTIONS', ...noData };
	const connections = current?.connections ?? null;
	if (m && connections != null) {
		const effectiveMax = Number(pool.parameters.effective.max_connections);
		const max = current?.max_connections ?? (Number.isFinite(effectiveMax) ? effectiveMax : null);
		const avg = seriesAverage(m.connections);
		const peak = seriesPeak(m.connections);
		connectionsStat = {
			label: 'CONNECTIONS',
			value: formatCount(connections),
			unit: max ? `/ ${formatCount(max)}` : undefined,
			sparkline: series(m.connections),
			split: [
				{ label: 'avg', value: formatCount(avg ?? connections) },
				{ label: 'peak', value: formatCount(peak ?? connections) }
			]
		};
	}

	let cpuStat: StatCardData = { label: 'CPU', ...noData };
	if (m && current) {
		// Pools carry no CPU limit, so the value picks the unit.
		const cpu = current.cpu_millicores;
		const inCores = cpu >= 1000;
		const fmt = (v: number) => (inCores ? (v / 1000).toFixed(v < 100 ? 2 : 1) : `${Math.round(v)}`);
		const avg = seriesAverage(m.cpu_millicores);
		const peak = seriesPeak(m.cpu_millicores);
		cpuStat = {
			label: 'CPU',
			value: fmt(cpu),
			unit: inCores ? 'cores' : 'mCPU',
			sparkline: series(m.cpu_millicores),
			split: [
				{ label: 'avg', value: fmt(avg ?? cpu) },
				{ label: 'peak', value: fmt(peak ?? cpu) }
			]
		};
	}

	let memStat: StatCardData = { label: 'MEMORY', ...noData };
	if (m && current) {
		const mem = current.memory_bytes;
		const budget = current.memory_budget_bytes ?? pool.memory.bytes;
		const parts = formatBytes(mem).split(' ');
		const avg = seriesAverage(m.memory_bytes);
		const peak = seriesPeak(m.memory_bytes);
		memStat = {
			label: 'MEMORY',
			value: parts[0],
			unit: budget != null ? `${parts[1]} / ${formatBytes(budget)}` : parts[1],
			note: budget == null ? 'budget not sized yet' : undefined,
			sparkline: series(m.memory_bytes),
			split: [
				{ label: 'avg', value: formatBytes(avg ?? mem) },
				{ label: 'peak', value: formatBytes(peak ?? mem) }
			]
		};
	}

	// A full volume stops the pool, so the databases' size draws against it.
	const storageStat = footprintStat(
		'STORAGE',
		current?.database_bytes,
		pool.storage_bytes,
		'bg-service-db',
		{
			declared: 'volume size, usage not yet measured',
			undeclared: 'no volume',
			measured: 'logical size of every database'
		}
	);

	return [connectionsStat, cpuStat, memStat, storageStat];
}

export interface PoolChart {
	id: string;
	title: string;
	/** Appended after the title in normal case, e.g. "/ 15min". */
	suffix?: string;
	series: ChartSeries[];
	format: (v: number) => string;
	yDomain?: [number, number];
	label: string;
}

/** Labels a per-bucket count with its interval. */
export function stepLabel(stepSeconds: number): string {
	if (stepSeconds >= 3600) return `${stepSeconds / 3600}h`;
	if (stepSeconds >= 60) return `${stepSeconds / 60}min`;
	return `${stepSeconds}s`;
}

function formatPercent(v: number): string {
	return `${v.toFixed(v >= 99.95 ? 0 : 1)}%`;
}

/** The six usage charts of the overview. */
export function poolCharts(m: PoolMetrics): PoolChart[] {
	const points = (values: (number | null)[]) => toChartPoints(m.timestamps, values);
	const one = (label: string, values: (number | null)[], color = 'var(--color-chart-1)') => [
		{ label, color, points: points(values) }
	];
	const step = stepLabel(m.step_seconds);
	return [
		{
			id: 'connections',
			title: 'Connections',
			series: one('connections', m.connections),
			format: formatCount,
			label: 'Client connections over the selected window'
		},
		{
			id: 'transactions',
			title: 'Transactions',
			suffix: `/ ${step}`,
			series: [
				{ label: 'commits', color: 'var(--color-chart-1)', points: points(m.commits) },
				{ label: 'rollbacks', color: 'var(--color-chart-2)', points: points(m.rollbacks) }
			],
			format: formatCount,
			label: 'Committed and rolled back transactions per bucket over the selected window'
		},
		{
			id: 'cpu',
			title: 'CPU',
			series: one('cpu', m.cpu_millicores),
			format: (v) => (v >= 1000 ? `${(v / 1000).toFixed(2)} cores` : `${Math.round(v)} mCPU`),
			label: 'CPU usage of the instances over the selected window'
		},
		{
			id: 'memory',
			title: 'Memory',
			series: one('memory', m.memory_bytes, 'var(--color-chart-2)'),
			format: formatBytes,
			label: 'Memory usage of the instances over the selected window'
		},
		{
			id: 'cache',
			title: 'Cache hit ratio',
			series: one(
				'hit ratio',
				m.cache_hit_ratio.map((v) => (v == null ? null : v * 100))
			),
			format: formatPercent,
			yDomain: [0, 100],
			label: 'Share of block reads served from the buffer cache over the selected window'
		},
		{
			id: 'storage',
			title: 'Storage',
			series: one('databases', m.database_bytes, 'var(--color-chart-2)'),
			format: formatBytes,
			label: 'Logical size of every database over the selected window'
		}
	];
}
