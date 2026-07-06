<script lang="ts">
	import X from '@lucide/svelte/icons/x';
	import Pencil from '@lucide/svelte/icons/pencil';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import LayoutDashboard from '@lucide/svelte/icons/layout-dashboard';
	import ChartLine from '@lucide/svelte/icons/chart-line';
	import { api } from '$lib/api/client';
	import { decimate, type ChartPoint, type ChartSeries } from '$lib/charts';
	import { formatBytes, formatPct, formatRate, relativeTime } from '$lib/format';
	import { NODE_ROLE_META, NODE_STATE_META } from '$lib/service-types';
	import type { Node, NodeMetricsHistory, NodeMetricsSample } from '$lib/types/nodes';
	import Button from '$lib/components/ui/Button.svelte';
	import KeyValueRow from '$lib/components/ui/KeyValueRow.svelte';
	import ProgressBar from '$lib/components/ui/ProgressBar.svelte';
	import Tabs, { type TabDef } from '$lib/components/ui/Tabs.svelte';
	import TimeSeriesChart from '$lib/components/ui/TimeSeriesChart.svelte';

	let {
		node,
		onedit,
		onremove,
		close
	}: {
		/** Fresh object on every page poll; same id ⇒ history is kept. */
		node: Node;
		/** Page-owned: opens the edit modal (invalidateAll/toasts live there). */
		onedit: () => void;
		/** Page-owned: the confirm-remove flow. */
		onremove: () => void;
		/** Injected by the SidePanel host. */
		close: () => void;
	} = $props();

	const isMaster = $derived(node.roles.includes('master'));
	// Not named `state`: a local of that name turns `$state` runes into
	// legacy store subscriptions.
	const stateMeta = $derived(NODE_STATE_META[node.status]);

	// --- tabs -------------------------------------------------------------------
	// Deliberately NOT reset when the selected node changes: staying on
	// Metrics while clicking through nodes is how you compare them.
	const TABS: TabDef[] = [
		{ id: 'overview', label: 'Overview', icon: LayoutDashboard },
		{ id: 'metrics', label: 'Metrics', icon: ChartLine }
	];
	let tab = $state('overview');

	// --- history fetch + poll -------------------------------------------------
	// Keyed on the MEMOIZED id: the page re-opens the panel with a fresh node
	// object every 10s, but an unchanged primitive id does not re-run this
	// effect — history survives poll churn and only resets when the selected
	// node actually changes. Not gated on the active tab: the fetch is cheap
	// and keeping it warm makes switching to Metrics instant.
	const nodeId = $derived(node.id);

	let history = $state<NodeMetricsSample[] | null>(null);
	let historyFailed = $state(false);

	$effect(() => {
		const id = nodeId; // sole tracked dependency
		history = null;
		historyFailed = false;
		let alive = true;
		const load = async () => {
			try {
				const res = await api.get<NodeMetricsHistory>(`/v1/nodes/${id}/metrics`);
				if (alive) {
					history = res.samples;
					historyFailed = false;
				}
			} catch {
				// A failed refetch keeps the stale frame; only flag when there
				// is nothing to show at all.
				if (alive && history === null) historyFailed = true;
			}
		};
		load();
		const t = setInterval(load, 15_000);
		return () => {
			alive = false;
			clearInterval(t);
		};
	});

	// --- chart series ----------------------------------------------------------
	const c1 = 'var(--color-chart-1)';
	const c2 = 'var(--color-chart-2)';

	const times = $derived((history ?? []).map((s) => Date.parse(s.sampled_at)));

	function seriesOf(
		label: string,
		color: string,
		f: (s: NodeMetricsSample) => number
	): ChartSeries {
		const points: ChartPoint[] = decimate(
			(history ?? []).map((s, i) => ({ t: times[i], v: f(s) }))
		);
		return { label, color, points };
	}

	const charts = $derived.by(() => {
		if (!history?.length) return null;
		const memTotal = node.metrics?.mem_total ?? history.at(-1)?.mem_total ?? 0;
		const cpuMax = Math.max(100, ...history.map((s) => s.cpu_pct));
		return [
			{
				title: 'CPU',
				latest: node.metrics ? formatPct(node.metrics.cpu_pct) : null,
				series: [seriesOf('CPU', c1, (s) => s.cpu_pct)],
				yDomain: [0, cpuMax] as [number, number],
				format: formatPct
			},
			{
				title: 'Memory',
				latest: node.metrics ? formatBytes(node.metrics.mem_used) : null,
				series: [seriesOf('Used', c1, (s) => s.mem_used)],
				yDomain: memTotal > 0 ? ([0, memTotal] as [number, number]) : undefined,
				format: formatBytes
			},
			{
				title: 'Network',
				latest: node.metrics
					? `↓ ${formatRate(node.metrics.net_rx_rate)} ↑ ${formatRate(node.metrics.net_tx_rate)}`
					: null,
				series: [
					seriesOf('Down', c1, (s) => s.net_rx_rate),
					seriesOf('Up', c2, (s) => s.net_tx_rate)
				],
				yDomain: undefined,
				format: formatRate
			},
			{
				title: 'Disk I/O',
				latest: node.metrics
					? `R ${formatRate(node.metrics.disk_read_rate)} W ${formatRate(node.metrics.disk_write_rate)}`
					: null,
				series: [
					seriesOf('Read', c1, (s) => s.disk_read_rate),
					seriesOf('Write', c2, (s) => s.disk_write_rate)
				],
				yDomain: undefined,
				format: formatRate
			},
			{
				title: 'Load',
				latest: node.metrics ? node.metrics.load1.toFixed(2) : null,
				series: [seriesOf('Load', c1, (s) => s.load1)],
				yDomain: undefined,
				format: (v: number) => v.toFixed(2)
			}
		];
	});

	// --- current tiles ----------------------------------------------------------
	function pct(used: number, total: number): number {
		return total > 0 ? Math.min(100, (used / total) * 100) : 0;
	}
	function barClass(p: number): string {
		return p > 90 ? 'bg-status-danger' : p > 75 ? 'bg-status-warning' : 'bg-accent';
	}
</script>

<!-- header -->
<div
	class="border-border-subtle flex items-start justify-between gap-3 border-b px-4.5 pt-4 pb-3.5"
>
	<div class="min-w-0">
		<div class="flex items-center gap-2">
			<h2 class="text-text-primary truncate text-[15px] font-semibold tracking-tight">
				{node.name}
			</h2>
			<span class="flex flex-none items-center gap-1.5 text-[11px] {stateMeta.text}">
				<span class="size-1.5 rounded-full {stateMeta.dot}"></span>
				{stateMeta.label}
			</span>
		</div>
		<div class="mt-1.5 flex flex-wrap items-center gap-1">
			{#each node.roles as role (role)}
				{@const meta = NODE_ROLE_META[role]}
				<span class="font-mono rounded-full px-2 py-0.5 text-[9.5px] {meta.text} {meta.bg}">
					{role}
				</span>
			{/each}
		</div>
	</div>
	<button
		type="button"
		onclick={() => close()}
		class="text-text-faint hover:text-text-primary flex-none cursor-pointer rounded-md p-1 transition-colors hover:bg-white/5"
		aria-label="Close details"
	>
		<X class="size-3.5" />
	</button>
</div>

<!-- tabs: fixed strip, only the pane below scrolls -->
<div class="px-4.5 pt-3">
	<Tabs tabs={TABS} active={tab} onchange={(id) => (tab = id)} label="{node.name} sections" />
</div>

<!-- body -->
<div class="flex-1 overflow-y-auto px-4.5 pb-4">
	{#if tab === 'overview'}
		<!-- facts -->
		<h3 class="text-text-ghost mt-4 mb-1.5 text-[10px] font-semibold tracking-[0.12em] uppercase">
			Node
		</h3>
		<div class="flex flex-col">
			<KeyValueRow k="ID" v={node.id} labelWidth="w-24" />
			<KeyValueRow k="Private addr" v={node.advertise_addr || '—'} labelWidth="w-24" />
			{#if node.public_addr}
				<KeyValueRow k="Public addr" v={node.public_addr} labelWidth="w-24" />
			{/if}
			<KeyValueRow
				k="System"
				v={[node.os, node.arch].filter(Boolean).join('/') || '—'}
				labelWidth="w-24"
			/>
			<KeyValueRow k="Version" v={node.skalid_version ?? '—'} labelWidth="w-24" />
			<KeyValueRow k="Last seen" v={relativeTime(node.last_seen)} labelWidth="w-24" />
			<KeyValueRow
				k="Added"
				v={new Date(node.created_at).toLocaleDateString(undefined, {
					year: 'numeric',
					month: 'short',
					day: 'numeric'
				})}
				labelWidth="w-24"
			/>
		</div>

		<!-- current -->
		<h3 class="text-text-ghost mt-5 mb-1.5 text-[10px] font-semibold tracking-[0.12em] uppercase">
			Current
		</h3>
		{#if node.metrics}
			{@const m = node.metrics}
			{@const cpuP = Math.min(100, m.cpu_pct)}
			{@const memP = pct(m.mem_used, m.mem_total)}
			{@const diskP = pct(m.disk_used, m.disk_total)}
			<div class="grid grid-cols-2 gap-2">
				<div class="rounded-[10px] bg-white/3 px-3 py-2.5">
					<div class="text-text-ghost text-[9.5px] font-semibold tracking-[0.1em] uppercase">
						CPU
					</div>
					<div class="font-mono text-text-primary mt-1 text-[13px]">{formatPct(m.cpu_pct)}</div>
					<div class="mt-1.5"><ProgressBar pct={cpuP} class={barClass(cpuP)} /></div>
				</div>
				<div class="rounded-[10px] bg-white/3 px-3 py-2.5">
					<div class="text-text-ghost text-[9.5px] font-semibold tracking-[0.1em] uppercase">
						Memory
					</div>
					<div class="font-mono text-text-primary mt-1 text-[13px]">
						{formatBytes(m.mem_used)}
						<span class="text-text-faint text-[11px]">/ {formatBytes(m.mem_total)}</span>
					</div>
					<div class="mt-1.5"><ProgressBar pct={memP} class={barClass(memP)} /></div>
				</div>
				<div class="rounded-[10px] bg-white/3 px-3 py-2.5">
					<div class="text-text-ghost text-[9.5px] font-semibold tracking-[0.1em] uppercase">
						Disk
					</div>
					<div class="font-mono text-text-primary mt-1 text-[13px]">
						{formatBytes(m.disk_used)}
						<span class="text-text-faint text-[11px]">/ {formatBytes(m.disk_total)}</span>
					</div>
					<div class="mt-1.5"><ProgressBar pct={diskP} class={barClass(diskP)} /></div>
				</div>
				<div class="rounded-[10px] bg-white/3 px-3 py-2.5">
					<div class="text-text-ghost text-[9.5px] font-semibold tracking-[0.1em] uppercase">
						Network
					</div>
					<div class="font-mono text-text-primary mt-1 text-[12px]">
						↓ {formatRate(m.net_rx_rate)}
					</div>
					<div class="font-mono text-text-secondary text-[12px]">↑ {formatRate(m.net_tx_rate)}</div>
				</div>
			</div>
		{:else}
			<p class="text-text-ghost text-[12px]">No metrics reported yet.</p>
		{/if}
	{:else if tab === 'metrics'}
		<!-- history -->
		<h3 class="text-text-ghost mt-4 mb-1.5 text-[10px] font-semibold tracking-[0.12em] uppercase">
			History · 24h
		</h3>
		{#if node.status === 'offline' && history?.length}
			<p class="text-status-warning mb-2 text-[11px]">Offline — showing last received data.</p>
		{/if}
		{#if charts}
			<div class="flex flex-col gap-4">
				{#each charts as chart (chart.title)}
					<div>
						<div class="mb-0.5 flex items-baseline justify-between">
							<span class="text-text-tertiary text-[12px] font-medium">{chart.title}</span>
							{#if chart.latest}
								<span class="font-mono text-text-muted text-[11px]">{chart.latest}</span>
							{/if}
						</div>
						<TimeSeriesChart
							series={chart.series}
							yDomain={chart.yDomain}
							formatValue={chart.format}
							label="{chart.title} over the last 24 hours"
						/>
					</div>
				{/each}
			</div>
		{:else if historyFailed}
			<p class="text-text-muted text-[12px]">Could not load metrics history.</p>
		{:else if history !== null}
			<p class="text-text-ghost text-[12px]">No metrics recorded yet.</p>
		{:else}
			<!-- loading: fixed-height skeletons so the panel doesn't jump -->
			<div class="flex flex-col gap-4">
				{#each ['cpu', 'mem', 'net'] as key (key)}
					<div class="h-[110px] animate-pulse rounded-lg bg-white/3"></div>
				{/each}
			</div>
		{/if}
	{/if}
</div>

<!-- footer -->
<div class="border-border-subtle bg-surface-raised/50 flex gap-2 border-t px-4.5 py-3">
	<Button size="sm" variant="secondary" class="flex-1" onclick={onedit}>
		<Pencil size={13} />
		Edit
	</Button>
	<span title={isMaster ? 'The master node cannot be removed' : undefined}>
		<Button
			size="sm"
			variant="ghost"
			class={isMaster ? 'text-text-ghost/40' : 'text-status-danger hover:text-status-danger'}
			disabled={isMaster}
			onclick={onremove}
		>
			<Trash2 size={13} />
			Remove
		</Button>
	</span>
</div>
