// Shapes mirror the skali API (api/openapi.yaml), snake_case included.

import type { ChartPoint } from '$lib/charts';

export type MetricsWindow = '1h' | '24h' | '7d';

/** One application's aligned series; null marks a bucket without samples. */
export interface ApplicationSeries {
	key: string;
	cpu_millicores: (number | null)[];
	memory_bytes: (number | null)[];
	/** Edge traffic (through Traefik); absent until edge samples exist. */
	edge?: EdgeSeries;
}

/** Per-bucket edge traffic: counts and bytes that arrived within the bucket. */
export interface EdgeSeries {
	requests: (number | null)[];
	request_bytes: (number | null)[];
	response_bytes: (number | null)[];
}

/** GET /v1/environments/{id}/metrics */
export interface EnvironmentMetrics {
	window: MetricsWindow;
	step_seconds: number;
	timestamps: string[];
	applications: ApplicationSeries[];
}

export interface NodeSeries {
	name: string;
	cpu_allocatable_millicores: number;
	memory_allocatable_bytes: number;
	cpu_millicores: (number | null)[];
	memory_bytes: (number | null)[];
}

/** GET /v1/nodes/metrics */
export interface NodeMetrics {
	window: MetricsWindow;
	step_seconds: number;
	timestamps: string[];
	nodes: NodeSeries[];
}

/** Zip one value array with the shared timestamps into chart points. */
export function toChartPoints(timestamps: string[], values: (number | null)[]): ChartPoint[] {
	return timestamps.map((ts, i) => ({ t: new Date(ts).getTime(), v: values[i] ?? null }));
}

/** The newest non-null value, i.e. current usage; null when all gaps. */
export function lastValue(values: (number | null)[]): number | null {
	for (let i = values.length - 1; i >= 0; i--) {
		const v = values[i];
		if (v != null) return v;
	}
	return null;
}

/** Sum every non-null value of a per-application accessor over the window. */
export function windowTotal(
	apps: ApplicationSeries[],
	pick: (app: ApplicationSeries) => (number | null)[] | undefined
): number | null {
	let total: number | null = null;
	for (const app of apps) {
		for (const v of pick(app) ?? []) {
			if (v != null) total = (total ?? 0) + v;
		}
	}
	return total;
}

/** Sum a per-application accessor at the newest bucket that has any data. */
export function currentTotal(
	apps: ApplicationSeries[],
	pick: (app: ApplicationSeries) => (number | null)[]
): number | null {
	let best: number | null = null;
	let bestIndex = -1;
	for (const app of apps) {
		const values = pick(app);
		for (let i = values.length - 1; i > bestIndex; i--) {
			if (values[i] != null) {
				bestIndex = i;
				break;
			}
		}
	}
	if (bestIndex < 0) return best;
	best = 0;
	for (const app of apps) best += pick(app)[bestIndex] ?? 0;
	return best;
}
