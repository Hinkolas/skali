// One node's page: its state, the stat tiles and the usage charts, built
// from the observed node, its metrics series and its newest storage
// sample. CPU and memory read as shares of what the node can allocate, the
// way the Apple app's node screen shows them; the charts keep absolute
// units with the allocatable amount as the ceiling.

import type { ChartSeries } from '$lib/charts';
import type { StatCardData } from '$lib/models/view';
import { formatBytes, formatCores, formatPct } from '$lib/format';
import {
	lastValue,
	seriesAverage,
	seriesPeak,
	toChartPoints,
	type NodeMetrics,
	type NodeSeries,
	type NodeStorage
} from '$lib/types/metrics';
import type { ClusterNode } from '$lib/types/nodes';

export interface NodeStatus {
	text: string;
	tone: 'success' | 'warning' | 'neutral';
}

/** Online, cordoned or offline: a cordoned node runs but takes no new pods. */
export function nodeStatus(node: ClusterNode): NodeStatus {
	if (!node.ready) return { text: 'offline', tone: 'neutral' };
	if (!node.schedulable) return { text: 'cordoned', tone: 'warning' };
	return { text: 'online', tone: 'success' };
}

/** "v1.36.3+k3s1" as a reader says it: "k3s v1.36.3". */
export function kubernetesLabel(kubelet: string): string {
	const plus = kubelet.indexOf('+');
	if (plus < 0) return kubelet;
	// "k3s1" is the distribution and its build number: keep "k3s".
	const distribution = kubelet.slice(plus + 1).replace(/\d+$/, '');
	const version = kubelet.slice(0, plus);
	return distribution ? `${distribution} ${version}` : version;
}

/** "of 4 cores", "of 1 core", "of 500m". */
export function cpuCapacity(millicores: number): string {
	if (millicores < 1000) return `of ${formatCores(millicores)}`;
	if (millicores === 1000) return 'of 1 core';
	// A whole count drops formatCores' ".0": "of 4 cores".
	return `of ${+(millicores / 1000).toFixed(1)} cores`;
}

/** The node's series in a metrics document, or null before it was sampled. */
export function nodeSeries(metrics: NodeMetrics | null, name: string): NodeSeries | null {
	return metrics?.nodes.find((n) => n.name === name) ?? null;
}

function percent(used: number, total: number): number {
	return total > 0 ? Math.min(100, Math.max(0, (used / total) * 100)) : 0;
}

function share(values: (number | null)[], total: number): (number | null)[] {
	return values.map((v) => (v == null || total <= 0 ? null : percent(v, total)));
}

/** A usage tile: the newest share, its sparkline, and the window's avg and peak. */
function usageStat(
	label: string,
	values: (number | null)[] | undefined,
	total: number | undefined,
	capacity: (total: number) => string,
	timestamps: string[]
): StatCardData {
	const current = values ? lastValue(values) : null;
	if (!values || current == null || !total) {
		return { label, value: 'n/a', note: 'no data yet' };
	}
	const shares = share(values, total);
	return {
		label,
		value: `${Math.round(percent(current, total))}`,
		unit: `% ${capacity(total)}`,
		sparkline: toChartPoints(timestamps, shares),
		split: [
			{ label: 'avg', value: formatPct(seriesAverage(shares) ?? 0) },
			{ label: 'peak', value: formatPct(seriesPeak(shares) ?? 0) }
		]
	};
}

/** The three tiles: CPU and memory against what the node can allocate, and
 * the disk against its capacity. */
export function nodeStats(
	metrics: NodeMetrics | null,
	series: NodeSeries | null,
	storage: NodeStorage | null
): StatCardData[] {
	const timestamps = metrics?.timestamps ?? [];
	const cpu = usageStat(
		'CPU',
		series?.cpu_millicores,
		series?.cpu_allocatable_millicores,
		cpuCapacity,
		timestamps
	);
	const memory = usageStat(
		'MEMORY',
		series?.memory_bytes,
		series?.memory_allocatable_bytes,
		(total) => `of ${formatBytes(total)}`,
		timestamps
	);

	let disk: StatCardData = { label: 'DISK', value: 'n/a', note: 'no data yet' };
	if (storage && storage.capacity_bytes > 0) {
		const used = percent(storage.used_bytes, storage.capacity_bytes);
		disk = {
			label: 'DISK',
			value: `${Math.round(used)}`,
			unit: `% of ${formatBytes(storage.capacity_bytes)}`,
			progress: { pct: used, class: 'bg-service-storage' },
			split: [
				{ label: 'used', value: formatBytes(storage.used_bytes) },
				{ label: 'free', value: formatBytes(storage.available_bytes) }
			]
		};
	}
	return [cpu, memory, disk];
}

export interface NodeChart {
	id: string;
	title: string;
	series: ChartSeries[];
	format: (v: number) => string;
	yDomain?: [number, number];
	label: string;
}

/** CPU and memory over the window, each drawn up to what the node can allocate. */
export function nodeCharts(metrics: NodeMetrics, series: NodeSeries): NodeChart[] {
	const points = (values: (number | null)[]) => toChartPoints(metrics.timestamps, values);
	const ceiling = (total: number): [number, number] | undefined =>
		total > 0 ? [0, total] : undefined;
	return [
		{
			id: 'cpu',
			title: 'CPU',
			series: [
				{ label: 'cpu', color: 'var(--color-chart-1)', points: points(series.cpu_millicores) }
			],
			format: (v) => (v >= 1000 ? `${(v / 1000).toFixed(1)} cores` : `${Math.round(v)} mCPU`),
			yDomain: ceiling(series.cpu_allocatable_millicores),
			label: 'CPU usage of the node over the selected window'
		},
		{
			id: 'memory',
			title: 'Memory',
			series: [
				{ label: 'memory', color: 'var(--color-chart-2)', points: points(series.memory_bytes) }
			],
			format: formatBytes,
			yDomain: ceiling(series.memory_allocatable_bytes),
			label: 'Memory usage of the node over the selected window'
		}
	];
}
