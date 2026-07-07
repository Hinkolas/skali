<script lang="ts">
	import X from '@lucide/svelte/icons/x';
	import Pencil from '@lucide/svelte/icons/pencil';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import LayoutDashboard from '@lucide/svelte/icons/layout-dashboard';
	import ChartLine from '@lucide/svelte/icons/chart-line';
	import Container from '@lucide/svelte/icons/container';
	import Layers from '@lucide/svelte/icons/layers';
	import HardDrive from '@lucide/svelte/icons/hard-drive';
	import Play from '@lucide/svelte/icons/play';
	import Square from '@lucide/svelte/icons/square';
	import Plus from '@lucide/svelte/icons/plus';
	import { api, ApiError } from '$lib/api/client';
	import { decimate, type ChartPoint, type ChartSeries } from '$lib/charts';
	import { formatBytes, formatPct, formatRate, relativeTime } from '$lib/format';
	import { modal } from '$lib/stores/modal.svelte';
	import { dialog } from '$lib/stores/dialog.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import {
		CONTAINER_HEALTH_META,
		CONTAINER_KIND_META,
		CONTAINER_STATE_META,
		NODE_ROLE_META,
		NODE_STATE_META
	} from '$lib/service-types';
	import type {
		ContainerCreateRequest,
		Node,
		NodeContainer,
		NodeContainerList,
		NodeImage,
		NodeImageList,
		NodeMetricsHistory,
		NodeMetricsSample,
		NodeVolume,
		NodeVolumeList
	} from '$lib/types/nodes';
	import Button from '$lib/components/ui/Button.svelte';
	import KeyValueRow from '$lib/components/ui/KeyValueRow.svelte';
	import ProgressBar from '$lib/components/ui/ProgressBar.svelte';
	import Tabs, { type TabDef } from '$lib/components/ui/Tabs.svelte';
	import TimeSeriesChart from '$lib/components/ui/TimeSeriesChart.svelte';
	import CreateContainerModal, {
		modalOptions as createContainerOptions
	} from '$lib/components/nodes/CreateContainerModal.svelte';

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
		{ id: 'metrics', label: 'Metrics', icon: ChartLine },
		{ id: 'containers', label: 'Containers', icon: Container },
		{ id: 'images', label: 'Images', icon: Layers },
		{ id: 'volumes', label: 'Volumes', icon: HardDrive }
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

	// --- containers: fetch + poll ------------------------------------------------
	// Same shape as the history effect: keyed on the memoized node id, kept
	// warm regardless of the active tab, stale frame on refetch errors.
	let containers = $state<NodeContainer[] | null>(null);
	let containersFailed = $state(false);

	$effect(() => {
		const id = nodeId; // sole tracked dependency
		containers = null;
		containersFailed = false;
		let alive = true;
		const load = async () => {
			try {
				const res = await api.get<NodeContainerList>(`/v1/nodes/${id}/containers`);
				if (alive) {
					containers = res.containers;
					containersFailed = false;
				}
			} catch {
				if (alive && containers === null) containersFailed = true;
			}
		};
		load();
		const t = setInterval(load, 10_000);
		return () => {
			alive = false;
			clearInterval(t);
		};
	});

	// --- inventory: fetch + poll ---------------------------------------------
	// Same shape again, on a slower poll: the node samples its inventory every
	// 60s, so 30s here just keeps the pane fresh without hammering the API.
	let images = $state<NodeImage[] | null>(null);
	let imagesFailed = $state(false);
	let volumes = $state<NodeVolume[] | null>(null);
	let volumesFailed = $state(false);

	$effect(() => {
		const id = nodeId; // sole tracked dependency
		images = null;
		imagesFailed = false;
		volumes = null;
		volumesFailed = false;
		let alive = true;
		const load = async () => {
			try {
				const res = await api.get<NodeImageList>(`/v1/nodes/${id}/images`);
				if (alive) {
					images = res.images;
					imagesFailed = false;
				}
			} catch {
				if (alive && images === null) imagesFailed = true;
			}
			try {
				const res = await api.get<NodeVolumeList>(`/v1/nodes/${id}/volumes`);
				if (alive) {
					volumes = res.volumes;
					volumesFailed = false;
				}
			} catch {
				if (alive && volumes === null) volumesFailed = true;
			}
		};
		load();
		const t = setInterval(load, 30_000);
		return () => {
			alive = false;
			clearInterval(t);
		};
	});

	/** Primary display name: first tag, or the shortened digest for dangling images. */
	function imageName(img: NodeImage): string {
		return img.repo_tags[0] ?? img.id.replace('sha256:', '').slice(0, 12);
	}

	// --- container actions -------------------------------------------------------
	// Mutations patch the local list from the response; the 10s poll (and the
	// heartbeat behind it) reconciles everything else.
	let creating = $state(false);
	let busyIds = $state<string[]>([]);

	function patchContainer(next: NodeContainer) {
		containers = (containers ?? []).map((c) => (c.id === next.id ? next : c));
	}

	// The sudo-gated create call runs BETWEEN modal close and any toast —
	// never while the modal is open — so the reauth modal slot stays free.
	async function addContainer() {
		const req = await modal.open<ContainerCreateRequest>(
			CreateContainerModal,
			{},
			createContainerOptions
		).result;
		if (!req) return;
		creating = true;
		try {
			const res = await api.post<{ container: NodeContainer }>(
				`/v1/nodes/${nodeId}/containers`,
				req
			);
			containers = [...(containers ?? []), res.container].sort((a, b) =>
				a.name.localeCompare(b.name)
			);
			toast.success(`Created ${res.container.name}`);
		} catch (err) {
			toast.error(err instanceof ApiError ? err.message : 'Could not create the container');
		} finally {
			creating = false;
		}
	}

	async function startStop(c: NodeContainer, action: 'start' | 'stop') {
		if (busyIds.includes(c.id)) return;
		busyIds = [...busyIds, c.id];
		try {
			const res = await api.post<{ container: NodeContainer }>(
				`/v1/nodes/${nodeId}/containers/${c.id}/${action}`
			);
			patchContainer(res.container);
		} catch (err) {
			toast.error(err instanceof ApiError ? err.message : `Could not ${action} ${c.name}`);
		} finally {
			busyIds = busyIds.filter((id) => id !== c.id);
		}
	}

	function removeContainer(c: NodeContainer) {
		dialog.confirm({
			title: `Remove ${c.name}?`,
			description:
				c.state === 'running'
					? 'The container is running; it will be killed and removed from the node.'
					: 'The container is removed from the node.',
			confirmLabel: 'Remove container',
			variant: 'danger',
			onConfirm: async () => {
				try {
					await api.del(`/v1/nodes/${nodeId}/containers/${c.id}?force=true`);
					containers = (containers ?? []).filter((x) => x.id !== c.id);
					toast.success(`Removed ${c.name}`);
				} catch (err) {
					toast.error(err instanceof ApiError ? err.message : 'Could not remove the container');
					throw err; // keep the dialog open
				}
			}
		});
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
	{:else if tab === 'containers'}
		<div class="mt-4 mb-1.5 flex items-center justify-between">
			<h3 class="text-text-ghost text-[10px] font-semibold tracking-[0.12em] uppercase">
				Containers
			</h3>
			<Button size="sm" variant="secondary" busy={creating} onclick={addContainer}>
				<Plus size={13} strokeWidth={2.5} />
				Add
			</Button>
		</div>
		{#if containers?.length}
			<div class="flex flex-col gap-2">
				{#each containers as c (c.id)}
					{@const cState = CONTAINER_STATE_META[c.state] ?? CONTAINER_STATE_META.gone}
					{@const kindMeta = CONTAINER_KIND_META[c.kind]}
					{@const busy = busyIds.includes(c.id)}
					{@const gone = c.state === 'gone'}
					<div class="rounded-[10px] bg-white/3 px-3 py-2.5 {gone ? 'opacity-50' : ''}">
						<div class="flex items-center justify-between gap-2">
							<div class="flex min-w-0 items-center gap-2">
								<span class="size-1.5 flex-none rounded-full {cState.dot}"></span>
								<span class="text-text-primary truncate text-[12.5px] font-medium">{c.name}</span>
								<span
									class="font-mono flex-none rounded-full px-2 py-0.5 text-[9.5px] {kindMeta.text} {kindMeta.bg}"
								>
									{c.kind}
								</span>
							</div>
							<div class="flex flex-none items-center gap-0.5">
								{#if !gone && c.state !== 'running'}
									<button
										type="button"
										disabled={busy}
										onclick={() => startStop(c, 'start')}
										class="text-text-ghost hover:text-status-success cursor-pointer rounded-md p-1 transition-colors hover:bg-white/5 disabled:opacity-40"
										aria-label="Start {c.name}"
										title="Start"
									>
										<Play size={13} />
									</button>
								{:else if c.state === 'running'}
									<button
										type="button"
										disabled={busy}
										onclick={() => startStop(c, 'stop')}
										class="text-text-ghost hover:text-status-warning cursor-pointer rounded-md p-1 transition-colors hover:bg-white/5 disabled:opacity-40"
										aria-label="Stop {c.name}"
										title="Stop"
									>
										<Square size={13} />
									</button>
								{/if}
								<button
									type="button"
									disabled={busy}
									onclick={() => removeContainer(c)}
									class="text-text-ghost hover:text-status-danger cursor-pointer rounded-md p-1 transition-colors hover:bg-white/5 disabled:opacity-40"
									aria-label="Remove {c.name}"
									title="Remove"
								>
									<Trash2 size={13} />
								</button>
							</div>
						</div>
						<div class="mt-1 flex items-center justify-between gap-2">
							<span class="font-mono text-text-faint truncate text-[11px]">{c.image}</span>
							<span class="flex flex-none items-center gap-1.5 text-[11px] {cState.text}">
								{cState.label}{#if c.state === 'exited' && c.exit_code !== null}&nbsp;({c.exit_code}){/if}
								{#if c.health}
									<span class={CONTAINER_HEALTH_META[c.health]}>· {c.health}</span>
								{/if}
							</span>
						</div>
						{#if c.stats}
							<div class="font-mono text-text-muted mt-1.5 flex gap-3 text-[11px]">
								<span>CPU {formatPct(c.stats.cpu_pct)}</span>
								<span>Mem {formatBytes(c.stats.mem_used)}</span>
								<span>↓ {formatRate(c.stats.net_rx_rate)} ↑ {formatRate(c.stats.net_tx_rate)}</span>
							</div>
						{/if}
					</div>
				{/each}
			</div>
			<p class="text-text-ghost mt-3 text-[11px] leading-relaxed">
				Raw skali-managed containers on this node, as last observed. This is the low-level admin
				surface — applications and databases will manage their own containers.
			</p>
		{:else if containersFailed}
			<p class="text-text-muted text-[12px]">Could not load containers.</p>
		{:else if containers !== null}
			<p class="text-text-ghost text-[12px]">No skali-managed containers on this node.</p>
		{:else}
			<div class="flex flex-col gap-2">
				{#each ['a', 'b', 'c'] as key (key)}
					<div class="h-[64px] animate-pulse rounded-[10px] bg-white/3"></div>
				{/each}
			</div>
		{/if}
	{:else if tab === 'images'}
		<h3 class="text-text-ghost mt-4 mb-1.5 text-[10px] font-semibold tracking-[0.12em] uppercase">
			Images
		</h3>
		{#if images?.length}
			<div class="flex flex-col gap-2">
				{#each images as img (img.id)}
					{@const unused = img.containers === 0}
					<div class="rounded-[10px] bg-white/3 px-3 py-2.5 {unused ? 'opacity-60' : ''}">
						<div class="flex items-center justify-between gap-2">
							<span class="font-mono text-text-primary truncate text-[12px] font-medium">
								{imageName(img)}
							</span>
							<span class="font-mono text-text-muted flex-none text-[11px]">
								{formatBytes(img.size_bytes)}
							</span>
						</div>
						<div class="mt-1 flex items-center justify-between gap-2 text-[11px]">
							<span class="text-text-faint truncate">
								{#if img.repo_tags.length > 1}
									+{img.repo_tags.length - 1} more tag{img.repo_tags.length > 2 ? 's' : ''}
								{:else}
									{img.id.replace('sha256:', '').slice(0, 12)}
								{/if}
							</span>
							<span class="text-text-muted flex flex-none items-center gap-1.5">
								{#if img.dangling}
									<span class="text-status-warning">dangling</span>
									·
								{/if}
								{img.containers === 0 ? 'unused' : `in use by ${img.containers}`}
							</span>
						</div>
					</div>
				{/each}
			</div>
			<p class="text-text-ghost mt-3 text-[11px] leading-relaxed">
				Every image on this node, largest first — skali-managed or not. Cleanup lands with the
				image lifecycle milestone.
			</p>
		{:else if imagesFailed}
			<p class="text-text-muted text-[12px]">Could not load images.</p>
		{:else if images !== null}
			<p class="text-text-ghost text-[12px]">No images observed on this node yet.</p>
		{:else}
			<div class="flex flex-col gap-2">
				{#each ['a', 'b', 'c'] as key (key)}
					<div class="h-[52px] animate-pulse rounded-[10px] bg-white/3"></div>
				{/each}
			</div>
		{/if}
	{:else if tab === 'volumes'}
		<h3 class="text-text-ghost mt-4 mb-1.5 text-[10px] font-semibold tracking-[0.12em] uppercase">
			Volumes
		</h3>
		{#if volumes?.length}
			<div class="flex flex-col gap-2">
				{#each volumes as vol (vol.name)}
					{@const unused = vol.containers === 0}
					<div class="rounded-[10px] bg-white/3 px-3 py-2.5 {unused ? 'opacity-60' : ''}">
						<div class="flex items-center justify-between gap-2">
							<span class="font-mono text-text-primary truncate text-[12px] font-medium">
								{vol.name}
							</span>
							<span class="font-mono text-text-muted flex-none text-[11px]">{vol.driver}</span>
						</div>
						<div class="mt-1 flex items-center justify-between gap-2 text-[11px]">
							<span class="text-text-faint truncate">
								{vol.created_at ? `created ${relativeTime(vol.created_at)}` : '—'}
							</span>
							<span class="text-text-muted flex-none">
								{vol.containers === 0 ? 'unused' : `in use by ${vol.containers}`}
							</span>
						</div>
					</div>
				{/each}
			</div>
			<p class="text-text-ghost mt-3 text-[11px] leading-relaxed">
				Every named volume on this node, anonymous ones included. Sizes are not sampled — they
				require walking each volume's filesystem.
			</p>
		{:else if volumesFailed}
			<p class="text-text-muted text-[12px]">Could not load volumes.</p>
		{:else if volumes !== null}
			<p class="text-text-ghost text-[12px]">No volumes on this node.</p>
		{:else}
			<div class="flex flex-col gap-2">
				{#each ['a', 'b', 'c'] as key (key)}
					<div class="h-[52px] animate-pulse rounded-[10px] bg-white/3"></div>
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
