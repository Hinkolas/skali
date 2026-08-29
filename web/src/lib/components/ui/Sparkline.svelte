<script lang="ts">
	import { decimate, type ChartPoint } from '$lib/charts';

	// Axis-free trend preview for stat tiles: a 1.5px line over an area that
	// fades to the card surface. Gaps (null) break the line, same as the
	// full TimeSeriesChart. Sized by its container width. The y range hugs the
	// data (a quarter of the spread below the minimum, floored at zero) so a
	// steady series still shows its drift instead of a flat line.
	let {
		points,
		color = 'var(--color-chart-1)',
		height = 28,
		class: className = ''
	}: {
		points: ChartPoint[];
		color?: string;
		height?: number;
		class?: string;
	} = $props();

	let w = $state(0);
	const gradientId = `spark-${Math.random().toString(36).slice(2, 9)}`;

	const pts = $derived(decimate(points, 60));
	const t0 = $derived(pts[0]?.t ?? 0);
	const t1 = $derived(pts[pts.length - 1]?.t ?? 1);
	const range = $derived.by((): [number, number] => {
		let lo = Infinity;
		let hi = -Infinity;
		for (const p of pts) {
			if (p.v == null) continue;
			if (p.v < lo) lo = p.v;
			if (p.v > hi) hi = p.v;
		}
		if (lo === Infinity) return [0, 1];
		const spread = hi - lo || hi || 1;
		return [Math.max(0, lo - spread * 0.25), hi];
	});

	// 1px inset keeps the round line caps and the top of the peak inside the box.
	const px = (t: number) => 1 + ((t - t0) / Math.max(1, t1 - t0)) * (w - 2);
	const py = (v: number) => 1 + (1 - (v - range[0]) / (range[1] - range[0] || 1)) * (height - 2);

	const line = $derived.by(() => {
		let d = '';
		let pen = false;
		for (const p of pts) {
			if (p.v == null) {
				pen = false;
				continue;
			}
			d += `${pen ? 'L' : 'M'}${px(p.t).toFixed(1)} ${py(p.v).toFixed(1)}`;
			pen = true;
		}
		return d;
	});

	const area = $derived.by(() => {
		const base = (height - 1).toFixed(1);
		let d = '';
		let run: ChartPoint[] = [];
		const flush = () => {
			if (run.length >= 2) {
				d += `M${px(run[0].t).toFixed(1)} ${base}`;
				for (const p of run) d += `L${px(p.t).toFixed(1)} ${py(p.v!).toFixed(1)}`;
				d += `L${px(run[run.length - 1].t).toFixed(1)} ${base}Z`;
			}
			run = [];
		};
		for (const p of pts) {
			if (p.v == null) flush();
			else run.push(p);
		}
		flush();
		return d;
	});
</script>

<div class="relative {className}" bind:clientWidth={w} style:height="{height}px">
	{#if w > 20 && pts.length > 1}
		<svg width={w} {height} class="block" aria-hidden="true">
			<defs>
				<linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
					<stop offset="0%" stop-color={color} stop-opacity="0.35" />
					<stop offset="100%" stop-color={color} stop-opacity="0.02" />
				</linearGradient>
			</defs>
			<path d={area} fill="url(#{gradientId})" />
			<path
				d={line}
				fill="none"
				stroke={color}
				stroke-width="1.5"
				stroke-linejoin="round"
				stroke-linecap="round"
			/>
		</svg>
	{/if}
</div>
