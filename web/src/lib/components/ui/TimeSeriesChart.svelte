<script lang="ts">
	import {
		localMidnights,
		nearestIndex,
		niceMax,
		type ChartPoint,
		type ChartSeries
	} from '$lib/charts';
	import { formatClock, formatDay, formatTimestamp } from '$lib/format';

	// Line/area time-series chart. Contract: all series share identical
	// timestamps (they come from the same sample array), so one hover index
	// addresses every series. Null values render as gaps in line and area.
	let {
		series,
		yDomain,
		height = 121,
		formatValue = (v: number) => v.toFixed(1),
		label,
		class: className = ''
	}: {
		series: ChartSeries[];
		/** Fixed y range; omit for [0, niceMax(data)]. */
		yDomain?: [number, number];
		height?: number;
		/** Formats tick + tooltip values, unit included ("1.2 GiB"). */
		formatValue?: (v: number) => string;
		/** Accessible description, e.g. "CPU usage over the last 24 hours". */
		label?: string;
		class?: string;
	} = $props();

	let w = $state(0);
	let hover = $state<number | null>(null);

	// SVG-internal geometry, so it does not follow --spacing; these are the
	// theme's 110% values hand-applied. Left gutter is sized for the widest
	// realistic tick ("512 MiB", "1000 B/s") at text-2xs mono — labels are
	// end-anchored at l-7 and must not clip.
	const pad = { t: 7, r: 7, b: 20, l: 64 };

	const pts0 = $derived(series[0]?.points ?? []);
	const t0 = $derived(pts0[0]?.t ?? 0);
	const t1 = $derived(pts0[pts0.length - 1]?.t ?? 1);
	const dom = $derived.by((): [number, number] => {
		if (yDomain) return yDomain;
		let max = 0;
		for (const s of series) for (const p of s.points) if (p.v != null && p.v > max) max = p.v;
		return [0, niceMax(max)];
	});

	const px = (t: number) => pad.l + ((t - t0) / Math.max(1, t1 - t0)) * (w - pad.l - pad.r);
	const py = (v: number) =>
		pad.t + (1 - (v - dom[0]) / (dom[1] - dom[0] || 1)) * (height - pad.t - pad.b);

	// Pen-up on nulls → visible gaps.
	function linePath(pts: ChartPoint[]): string {
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
	}

	// Area = one closed subpath per contiguous non-null run, dropped to the baseline.
	function areaPath(pts: ChartPoint[]): string {
		const base = py(dom[0]).toFixed(1);
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
	}

	const paths = $derived(
		series.map((s) => ({ line: linePath(s.points), area: areaPath(s.points) }))
	);
	const yTicks = $derived([dom[0], (dom[0] + dom[1]) / 2, dom[1]]);

	// Within a couple of days the x axis reads as clock times at even
	// positions, up to four of them and fewer on a narrow chart so the labels
	// stay apart (a clock label is ~50px at text-2xs mono). Over longer spans
	// clock times say nothing, so the ticks move to local midnights labeled
	// by date, thinned to what the width fits (at most seven) and kept clear
	// of both edges where a centered label would clip.
	const DAY = 86_400_000;
	const daily = $derived(t1 - t0 > 2 * DAY);
	const inner = $derived(Math.max(0, w - pad.l - pad.r));
	const xTicks = $derived.by((): { t: number; anchor: string }[] => {
		if (!daily) {
			const n = Math.max(2, Math.min(4, Math.floor(inner / 110) + 1));
			return Array.from({ length: n }, (_, i) => ({
				t: t0 + (i / (n - 1)) * (t1 - t0),
				anchor: i === 0 ? 'start' : i === n - 1 ? 'end' : 'middle'
			}));
		}
		const midnights = localMidnights(t0, t1).filter((t) => {
			const frac = (t - t0) / (t1 - t0);
			return frac > 0.03 && frac < 0.97;
		});
		const every = Math.ceil(midnights.length / Math.max(2, Math.min(7, Math.floor(inner / 90))));
		return midnights.filter((_, i) => i % every === 0).map((t) => ({ t, anchor: 'middle' }));
	});

	function onmove(e: PointerEvent & { currentTarget: SVGRectElement }) {
		const rect = e.currentTarget.getBoundingClientRect();
		const frac = Math.min(1, Math.max(0, (e.clientX - rect.left) / rect.width));
		hover = nearestIndex(pts0, t0 + frac * (t1 - t0));
	}
</script>

<div class="relative {className}" bind:clientWidth={w}>
	{#if w > 60 && pts0.length > 1}
		<svg width={w} {height} class="block" role="img" aria-label={label}>
			<!-- recessive grid + y ticks (text tokens, never series color) -->
			{#each yTicks as tick (tick)}
				<line x1={pad.l} x2={w - pad.r} y1={py(tick)} y2={py(tick)} class="stroke-border-subtle" />
				<text
					x={pad.l - 7}
					y={py(tick) + 3}
					text-anchor="end"
					class="fill-text-ghost font-mono text-2xs"
				>
					{formatValue(tick)}
				</text>
			{/each}
			{#each xTicks as tick (tick.t)}
				<text
					x={px(tick.t)}
					y={height - 4}
					text-anchor={tick.anchor}
					class="fill-text-ghost font-mono text-2xs"
				>
					{daily ? formatDay(tick.t) : formatClock(tick.t)}
				</text>
			{/each}

			<!-- marks: 12%-opacity area under a 2px line -->
			{#each series as s, i (s.label)}
				<path d={paths[i].area} fill={s.color} fill-opacity="0.12" />
				<path
					d={paths[i].line}
					fill="none"
					stroke={s.color}
					stroke-width="2"
					stroke-linejoin="round"
					stroke-linecap="round"
				/>
			{/each}

			<!-- crosshair + hovered points (2px surface ring) -->
			{#if hover != null && pts0[hover]}
				<line
					x1={px(pts0[hover].t)}
					x2={px(pts0[hover].t)}
					y1={pad.t}
					y2={height - pad.b}
					class="stroke-white/15"
				/>
				{#each series as s (s.label)}
					{@const p = s.points[hover]}
					{#if p?.v != null}
						<circle
							cx={px(p.t)}
							cy={py(p.v)}
							r="3"
							fill={s.color}
							stroke="var(--color-surface-raised)"
							stroke-width="2"
						/>
					{/if}
				{/each}
			{/if}

			<!-- transparent overlay: the whole plot is the hover hit target.
			     Hover is a progressive enhancement (values also live in the
			     Current tiles), so presentation is the honest role. -->
			<rect
				role="presentation"
				x={pad.l}
				y={pad.t}
				width={Math.max(0, w - pad.l - pad.r)}
				height={height - pad.t - pad.b}
				fill="transparent"
				onpointermove={onmove}
				onpointerleave={() => (hover = null)}
			/>
		</svg>

		{#if hover != null && pts0[hover]}
			{@const hx = px(pts0[hover].t)}
			{@const flip = hx > w * 0.55}
			<div
				class="border-border-raised bg-surface-overlay pointer-events-none absolute top-1.5 z-10 rounded-lg border px-2.5 py-2 shadow-xl"
				style:left={flip ? undefined : `${hx + 10}px`}
				style:right={flip ? `${w - hx + 10}px` : undefined}
			>
				<div class="font-mono text-text-ghost text-xs whitespace-nowrap">
					{formatTimestamp(pts0[hover].t)}
				</div>
				{#each series as s (s.label)}
					{@const v = s.points[hover]?.v}
					<div class="mt-1 flex items-center gap-1.5 whitespace-nowrap">
						<span class="h-0.5 w-3 flex-none rounded-full" style:background={s.color}></span>
						<span class="font-mono text-text-primary text-sm">
							{v == null ? '—' : formatValue(v)}
						</span>
						{#if series.length > 1}
							<span class="text-text-faint text-xs">{s.label}</span>
						{/if}
					</div>
				{/each}
			</div>
		{/if}

		<!-- legend only for multi-series (a single series is named by its title) -->
		{#if series.length > 1}
			<div class="mt-1 flex items-center gap-3 pl-11">
				{#each series as s (s.label)}
					<span class="flex items-center gap-1.5">
						<span class="h-0.5 w-3 rounded-full" style:background={s.color}></span>
						<span class="text-text-faint text-xs">{s.label}</span>
					</span>
				{/each}
			</div>
		{/if}
	{/if}
</div>
