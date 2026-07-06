// Data helpers for TimeSeriesChart. Pure functions, no DOM.

/** One chart point; v null = gap (no data for that window). */
export type ChartPoint = { t: number; v: number | null };

/** One TimeSeriesChart series. All series in a chart share timestamps. */
export type ChartSeries = { label: string; color: string; points: ChartPoint[] };

/**
 * Fixed-width time-bucket decimation with averaging. Empty buckets yield
 * v:null, which the chart renders as a gap — offline windows appear
 * automatically because the backend omits samples it never received.
 */
export function decimate(points: ChartPoint[], target = 180): ChartPoint[] {
	if (points.length <= target) return points;
	const t0 = points[0].t;
	const t1 = points[points.length - 1].t;
	const width = (t1 - t0) / target || 1;
	const sums = new Float64Array(target);
	const counts = new Uint32Array(target);
	for (const p of points) {
		if (p.v == null) continue;
		const i = Math.min(target - 1, Math.floor((p.t - t0) / width));
		sums[i] += p.v;
		counts[i]++;
	}
	return Array.from({ length: target }, (_, i) => ({
		t: t0 + (i + 0.5) * width,
		v: counts[i] ? sums[i] / counts[i] : null
	}));
}

/** Round up to 1/2/5 × 10^k for a clean auto y-max. */
export function niceMax(v: number): number {
	if (v <= 0) return 1;
	const pow = 10 ** Math.floor(Math.log10(v));
	const m = v / pow;
	return (m <= 1 ? 1 : m <= 2 ? 2 : m <= 5 ? 5 : 10) * pow;
}

/** Index of the point nearest to timestamp t (points sorted ascending). */
export function nearestIndex(pts: ChartPoint[], t: number): number {
	let lo = 0;
	let hi = pts.length - 1;
	while (hi - lo > 1) {
		const mid = (lo + hi) >> 1;
		if (pts[mid].t < t) lo = mid;
		else hi = mid;
	}
	return t - pts[lo].t <= pts[hi].t - t ? lo : hi;
}
