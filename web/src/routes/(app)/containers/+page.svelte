<script lang="ts">
	import Plus from '@lucide/svelte/icons/plus';
	import Play from '@lucide/svelte/icons/play';
	import Square from '@lucide/svelte/icons/square';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import Container from '@lucide/svelte/icons/container';
	import Layers from '@lucide/svelte/icons/layers';
	import HardDrive from '@lucide/svelte/icons/hard-drive';
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import Search from '@lucide/svelte/icons/search';
	import { invalidateAll, replaceState } from '$app/navigation';
	import { page } from '$app/state';
	import { api, ApiError } from '$lib/api/client';
	import { modal } from '$lib/stores/modal.svelte';
	import { dialog } from '$lib/stores/dialog.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import { formatBytes, formatPct, relativeTime } from '$lib/format';
	import {
		CONTAINER_HEALTH_META,
		CONTAINER_KIND_META,
		CONTAINER_STATE_META
	} from '$lib/service-types';
	import type { NodeContainer, NodeImage } from '$lib/types/nodes';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Table from '$lib/components/ui/Table.svelte';
	import Tabs, { type TabDef } from '$lib/components/ui/Tabs.svelte';
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
	import CreateContainerModal, {
		modalOptions as createContainerOptions,
		type CreateContainerResult
	} from '$lib/components/containers/CreateContainerModal.svelte';
	import PullImageModal, {
		modalOptions as pullImageOptions,
		type PullImageResult
	} from '$lib/components/containers/PullImageModal.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	const TABS: TabDef[] = [
		{ id: 'containers', label: 'Containers', icon: Container },
		{ id: 'images', label: 'Images', icon: Layers },
		{ id: 'volumes', label: 'Volumes', icon: HardDrive }
	];
	const TAB_IDS = ['containers', 'images', 'volumes'];

	const ctrGrid = 'grid-cols-[1.1fr_2.4fr_0.9fr_1.2fr_1.5fr_96px]';
	const imgGrid = 'grid-cols-[1.1fr_2.6fr_0.8fr_1.3fr_0.9fr_56px]';
	const volGrid = 'grid-cols-[1.1fr_2.6fr_0.8fr_1fr_0.9fr]';

	// Live-ish state: re-run the server load every 10s while the page is open.
	$effect(() => {
		const t = setInterval(() => invalidateAll(), 10_000);
		return () => clearInterval(t);
	});

	// --- URL state: ?tab= and ?node=, local state mirrored via replaceState --
	// Same pattern as the nodes page's ?node= selection: page.url cannot be
	// the reactive source (shallow replaceState never reassigns it), so local
	// state drives the UI and the URL only mirrors it for deep links.
	const initTab = page.url.searchParams.get('tab');
	let tab = $state(initTab && TAB_IDS.includes(initTab) ? initTab : 'containers');
	let nodeFilter = $state(page.url.searchParams.get('node'));
	let query = $state('');

	function mirrorUrl() {
		// location, not page.url: page.url goes stale after the first shallow
		// replaceState and would resurrect old params.
		const url = new URL(location.href);
		if (tab !== 'containers') url.searchParams.set('tab', tab);
		else url.searchParams.delete('tab');
		if (nodeFilter) url.searchParams.set('node', nodeFilter);
		else url.searchParams.delete('node');
		replaceState(url, {});
	}
	function setTab(id: string) {
		tab = id;
		mirrorUrl();
	}
	function setNodeFilter(id: string | null) {
		nodeFilter = id;
		filterOpen = false;
		mirrorUrl();
	}

	// A filter pointing at a deleted node (or a bogus deep link) resets to
	// "All nodes" instead of silently showing nothing.
	$effect(() => {
		if (nodeFilter && !data.nodes.some((n) => n.id === nodeFilter)) setNodeFilter(null);
	});

	// --- node filter dropdown (no select primitive exists yet) ---------------
	let filterOpen = $state(false);
	let filterRoot = $state<HTMLDivElement | null>(null);
	const filterLabel = $derived(
		nodeFilter ? (data.nodes.find((n) => n.id === nodeFilter)?.name ?? '…') : 'All nodes'
	);

	function onWindowClick(e: MouseEvent) {
		if (filterOpen && filterRoot && !filterRoot.contains(e.target as Node)) filterOpen = false;
	}
	function onWindowKeydown(e: KeyboardEvent) {
		if (e.key === 'Escape') filterOpen = false;
	}

	// --- client-side filtering over the full dataset -------------------------
	const q = $derived(query.trim().toLowerCase());
	const containers = $derived(
		data.containers.filter(
			(c) =>
				(!nodeFilter || c.node_id === nodeFilter) &&
				(!q || c.name.toLowerCase().includes(q) || c.image.toLowerCase().includes(q))
		)
	);
	const images = $derived(
		data.images.filter(
			(i) =>
				(!nodeFilter || i.node_id === nodeFilter) &&
				(!q || i.repo_tags.some((t) => t.toLowerCase().includes(q)) || i.id.includes(q))
		)
	);
	const volumes = $derived(
		data.volumes.filter(
			(v) => (!nodeFilter || v.node_id === nodeFilter) && (!q || v.name.toLowerCase().includes(q))
		)
	);
	const filtered = $derived(Boolean(nodeFilter || q));

	/** Primary display name: first tag, or the shortened digest for dangling images. */
	function imageName(img: NodeImage): string {
		return img.repo_tags[0] ?? img.id.replace('sha256:', '').slice(0, 12);
	}

	// --- actions --------------------------------------------------------------
	// Mutations re-run the server load; the 10s poll (and the heartbeat behind
	// it) reconciles everything else.
	let creating = $state(false);
	let busyIds = $state<string[]>([]);

	// The sudo-gated create call runs BETWEEN modal close and any toast —
	// never while the modal is open — so the reauth modal slot stays free.
	async function addContainer() {
		const result = await modal.open<CreateContainerResult>(
			CreateContainerModal,
			{
				nodes: data.nodes.map((n) => ({ id: n.id, name: n.name })),
				initialNodeId: nodeFilter
			},
			createContainerOptions
		).result;
		if (!result) return;
		creating = true;
		try {
			const res = await api.post<{ container: NodeContainer }>(
				`/v1/nodes/${result.nodeId}/containers`,
				result.request
			);
			toast.success(`Created ${res.container.name} on ${res.container.node_name}`);
			await invalidateAll();
		} catch (err) {
			toast.error(err instanceof ApiError ? err.message : 'Could not create the container');
		} finally {
			creating = false;
		}
	}

	let pulling = $state(false);

	async function pullImage() {
		const result = await modal.open<PullImageResult>(
			PullImageModal,
			{
				nodes: data.nodes.map((n) => ({ id: n.id, name: n.name })),
				initialNodeId: nodeFilter
			},
			pullImageOptions
		).result;
		if (!result) return;
		pulling = true;
		try {
			const res = await api.post<{ image: NodeImage }>(
				`/v1/nodes/${result.nodeId}/images/pull`,
				{ reference: result.reference }
			);
			toast.success(`Pulled ${result.reference} onto ${res.image.node_name}`);
			await invalidateAll();
		} catch (err) {
			toast.error(err instanceof ApiError ? err.message : `Could not pull ${result.reference}`);
		} finally {
			pulling = false;
		}
	}

	function removeImage(img: NodeImage) {
		const ref = img.repo_tags[0] ?? img.id;
		dialog.confirm({
			title: `Remove ${imageName(img)}?`,
			description:
				img.containers > 0
					? `The image is in use by ${img.containers} container${img.containers === 1 ? '' : 's'} on ${img.node_name}; removal will fail until they are gone.`
					: `The image is removed from ${img.node_name}. It can always be pulled again.`,
			confirmLabel: 'Remove image',
			variant: 'danger',
			onConfirm: async () => {
				try {
					await api.del(`/v1/nodes/${img.node_id}/images?ref=${encodeURIComponent(ref)}`);
					toast.success(`Removed ${imageName(img)}`);
					await invalidateAll();
				} catch (err) {
					toast.error(err instanceof ApiError ? err.message : 'Could not remove the image');
					throw err; // keep the dialog open
				}
			}
		});
	}

	async function startStop(c: NodeContainer, action: 'start' | 'stop') {
		if (busyIds.includes(c.id)) return;
		busyIds = [...busyIds, c.id];
		try {
			await api.post(`/v1/nodes/${c.node_id}/containers/${c.id}/${action}`);
			await invalidateAll();
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
					? `The container is running on ${c.node_name}; it will be killed and removed.`
					: `The container is removed from ${c.node_name}.`,
			confirmLabel: 'Remove container',
			variant: 'danger',
			onConfirm: async () => {
				try {
					await api.del(`/v1/nodes/${c.node_id}/containers/${c.id}?force=true`);
					toast.success(`Removed ${c.name}`);
					await invalidateAll();
				} catch (err) {
					toast.error(err instanceof ApiError ? err.message : 'Could not remove the container');
					throw err; // keep the dialog open
				}
			}
		});
	}
</script>

<svelte:window onclick={onWindowClick} onkeydown={onWindowKeydown} />

<svelte:head>
	<title>Containers — skali</title>
</svelte:head>

<PageHeader title="Containers">
	{#snippet subtitle()}
		{data.containers.length} container{data.containers.length === 1 ? '' : 's'} ·
		{data.images.length} image{data.images.length === 1 ? '' : 's'} ·
		{data.volumes.length} volume{data.volumes.length === 1 ? '' : 's'} across
		{data.nodes.length} node{data.nodes.length === 1 ? '' : 's'}
	{/snippet}
	{#snippet actions()}
		{#if tab === 'images'}
			<Button variant="primary" busy={pulling} onclick={pullImage}>
				<Plus size={15} strokeWidth={2.5} />
				Pull image
			</Button>
		{:else}
			<Button variant="primary" busy={creating} onclick={addContainer}>
				<Plus size={15} strokeWidth={2.5} />
				Add container
			</Button>
		{/if}
	{/snippet}
</PageHeader>

<!-- toolbar: tabs + search + node filter -->
<div class="flex flex-wrap items-center gap-2.5 pb-4">
	<Tabs tabs={TABS} active={tab} onchange={setTab} label="Engine surface sections" />
	<div class="ml-auto flex items-center gap-2">
		<label class="relative">
			<Search size={13} class="text-text-ghost absolute top-1/2 left-3 -translate-y-1/2" />
			<input
				bind:value={query}
				type="text"
				placeholder="Filter…"
				class="border-border-strong bg-surface-input text-text-primary focus:border-accent/50 w-44 rounded-[10px] border py-2 pr-3 pl-8.5 text-[12.5px] transition-colors focus:outline-none"
			/>
		</label>
		<div bind:this={filterRoot} class="relative">
			<button
				type="button"
				onclick={() => (filterOpen = !filterOpen)}
				aria-expanded={filterOpen}
				aria-haspopup="menu"
				class="border-border-strong text-text-secondary flex cursor-pointer items-center gap-2 rounded-[10px] border px-3 py-2 text-[12.5px] font-medium transition-colors hover:bg-white/4 {nodeFilter
					? 'border-accent/40 text-accent-nav'
					: ''}"
			>
				{filterLabel}
				<ChevronDown
					size={13}
					class="text-text-ghost flex-none transition-transform {filterOpen ? 'rotate-180' : ''}"
				/>
			</button>
			{#if filterOpen}
				<div
					class="bg-surface-overlay border-border-default absolute top-full right-0 z-10 mt-1.5 flex w-52 flex-col gap-0.5 rounded-xl border p-1.5 shadow-lg"
					role="menu"
					aria-label="Filter by node"
				>
					<button
						type="button"
						role="menuitem"
						onclick={() => setNodeFilter(null)}
						class="cursor-pointer rounded-lg px-2.5 py-2 text-left text-[12.5px] font-medium transition-colors {nodeFilter ===
						null
							? 'text-accent-nav bg-accent/10'
							: 'text-text-secondary hover:text-text-primary hover:bg-white/5'}"
					>
						All nodes
					</button>
					{#each data.nodes as n (n.id)}
						<button
							type="button"
							role="menuitem"
							onclick={() => setNodeFilter(n.id)}
							class="cursor-pointer truncate rounded-lg px-2.5 py-2 text-left text-[12.5px] font-medium transition-colors {nodeFilter ===
							n.id
								? 'text-accent-nav bg-accent/10'
								: 'text-text-secondary hover:text-text-primary hover:bg-white/5'}"
						>
							{n.name}
						</button>
					{/each}
				</div>
			{/if}
		</div>
	</div>
</div>

<div class="pb-6">
	{#if tab === 'containers'}
		{#if containers.length === 0}
			<EmptyState
				title={filtered ? 'No matching containers' : 'No containers yet'}
				description={filtered
					? 'Nothing matches the current node filter or search.'
					: 'Add a raw container to any node — the low-level admin surface; applications and databases will manage their own.'}
			/>
		{:else}
			<Table columns={['Node', 'Container', 'Kind', 'State', 'CPU / Mem', '']} grid={ctrGrid}>
				{#each containers as c (c.node_id + c.id)}
					{@const cState = CONTAINER_STATE_META[c.state] ?? CONTAINER_STATE_META.gone}
					{@const kindMeta = CONTAINER_KIND_META[c.kind]}
					{@const busy = busyIds.includes(c.id)}
					{@const gone = c.state === 'gone'}
					<div
						class="border-border-subtle grid items-center border-b px-4.5 py-3 transition-colors last:border-0 hover:bg-white/2 {ctrGrid} {gone
							? 'opacity-50'
							: ''}"
					>
						<div class="text-text-muted truncate text-[12px]">{c.node_name}</div>
						<div class="flex min-w-0 flex-col gap-px">
							<span class="text-text-primary truncate text-[13px] font-medium">{c.name}</span>
							<span class="font-mono text-text-faint truncate text-[11px]">{c.image}</span>
						</div>
						<div>
							<span class="font-mono rounded-full px-2 py-0.5 text-[9.5px] {kindMeta.text} {kindMeta.bg}">
								{c.kind}
							</span>
						</div>
						<div>
							<span class="flex items-center gap-1.5 text-[11.5px] {cState.text}">
								<span class="size-1.5 flex-none rounded-full {cState.dot}"></span>
								{cState.label}{#if c.state === 'exited' && c.exit_code !== null}&nbsp;({c.exit_code}){/if}
								{#if c.health}
									<span class={CONTAINER_HEALTH_META[c.health]}>· {c.health}</span>
								{/if}
							</span>
						</div>
						<div class="font-mono text-text-muted text-[11px]">
							{#if c.stats}
								{formatPct(c.stats.cpu_pct)} · {formatBytes(c.stats.mem_used)}
							{:else}
								—
							{/if}
						</div>
						<div class="flex items-center justify-end gap-1">
							{#if !gone && c.state !== 'running'}
								<button
									type="button"
									disabled={busy}
									onclick={() => startStop(c, 'start')}
									class="text-text-ghost hover:text-status-success cursor-pointer rounded-lg p-1.5 transition-colors hover:bg-white/5 disabled:opacity-40"
									aria-label="Start {c.name}"
									title="Start"
								>
									<Play size={14} />
								</button>
							{:else if c.state === 'running'}
								<button
									type="button"
									disabled={busy}
									onclick={() => startStop(c, 'stop')}
									class="text-text-ghost hover:text-status-warning cursor-pointer rounded-lg p-1.5 transition-colors hover:bg-white/5 disabled:opacity-40"
									aria-label="Stop {c.name}"
									title="Stop"
								>
									<Square size={14} />
								</button>
							{/if}
							<button
								type="button"
								disabled={busy}
								onclick={() => removeContainer(c)}
								class="text-text-ghost hover:text-status-danger cursor-pointer rounded-lg p-1.5 transition-colors hover:bg-white/5 disabled:opacity-40"
								aria-label="Remove {c.name}"
								title="Remove"
							>
								<Trash2 size={14} />
							</button>
						</div>
					</div>
				{/each}
			</Table>
		{/if}
	{:else if tab === 'images'}
		{#if images.length === 0}
			<EmptyState
				title={filtered ? 'No matching images' : 'No images observed'}
				description={filtered
					? 'Nothing matches the current node filter or search.'
					: 'Every image on every node shows up here — skali-managed or not — as heartbeats report them.'}
			/>
		{:else}
			<Table columns={['Node', 'Image', 'Size', 'Status', 'Created', '']} grid={imgGrid}>
				{#each images as img (img.node_id + img.id)}
					{@const unused = img.containers === 0}
					<div
						class="border-border-subtle grid items-center border-b px-4.5 py-3 transition-colors last:border-0 hover:bg-white/2 {imgGrid} {unused
							? 'opacity-60'
							: ''}"
					>
						<div class="text-text-muted truncate text-[12px]">{img.node_name}</div>
						<div class="flex min-w-0 flex-col gap-px">
							<span class="font-mono text-text-primary truncate text-[12.5px] font-medium">
								{imageName(img)}
							</span>
							<span class="font-mono text-text-faint truncate text-[11px]">
								{#if img.repo_tags.length > 1}
									+{img.repo_tags.length - 1} more tag{img.repo_tags.length > 2 ? 's' : ''}
								{:else}
									{img.id.replace('sha256:', '').slice(0, 12)}
								{/if}
							</span>
						</div>
						<div class="font-mono text-text-muted text-[11.5px]">{formatBytes(img.size_bytes)}</div>
						<div class="text-[11.5px]">
							<span class="text-text-muted">
								{#if img.dangling}
									<span class="text-status-warning">dangling</span> ·
								{/if}
								{img.containers === 0 ? 'unused' : `in use by ${img.containers}`}
							</span>
						</div>
						<div class="font-mono text-text-muted text-[11px]">
							{img.created_at ? relativeTime(img.created_at) : '—'}
						</div>
						<div class="flex items-center justify-end">
							<button
								type="button"
								onclick={() => removeImage(img)}
								class="text-text-ghost hover:text-status-danger cursor-pointer rounded-lg p-1.5 transition-colors hover:bg-white/5"
								aria-label="Remove {imageName(img)}"
								title="Remove"
							>
								<Trash2 size={14} />
							</button>
						</div>
					</div>
				{/each}
			</Table>
			<p class="text-text-ghost mt-3 text-[11px] leading-relaxed">
				Largest first, unfiltered — whole-cluster disk visibility. Automated cleanup of unneeded
				images lands with the application layer.
			</p>
		{/if}
	{:else if tab === 'volumes'}
		{#if volumes.length === 0}
			<EmptyState
				title={filtered ? 'No matching volumes' : 'No volumes'}
				description={filtered
					? 'Nothing matches the current node filter or search.'
					: 'Named volumes on any node show up here as heartbeats report them (anonymous ones included).'}
			/>
		{:else}
			<Table columns={['Node', 'Volume', 'Driver', 'In use', 'Created']} grid={volGrid}>
				{#each volumes as vol (vol.node_id + vol.name)}
					{@const unused = vol.containers === 0}
					<div
						class="border-border-subtle grid items-center border-b px-4.5 py-3 transition-colors last:border-0 hover:bg-white/2 {volGrid} {unused
							? 'opacity-60'
							: ''}"
					>
						<div class="text-text-muted truncate text-[12px]">{vol.node_name}</div>
						<div class="font-mono text-text-primary truncate text-[12.5px] font-medium">
							{vol.name}
						</div>
						<div class="font-mono text-text-muted text-[11.5px]">{vol.driver}</div>
						<div class="text-text-muted text-[11.5px]">
							{vol.containers === 0 ? 'unused' : `by ${vol.containers} container${vol.containers === 1 ? '' : 's'}`}
						</div>
						<div class="font-mono text-text-muted text-[11px]">
							{vol.created_at ? relativeTime(vol.created_at) : '—'}
						</div>
					</div>
				{/each}
			</Table>
			<p class="text-text-ghost mt-3 text-[11px] leading-relaxed">
				Sizes are not sampled — they require walking each volume's filesystem.
			</p>
		{/if}
	{/if}
</div>
