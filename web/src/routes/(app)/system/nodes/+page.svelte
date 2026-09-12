<script lang="ts">
	import Server from '@lucide/svelte/icons/server';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
	import StackedBar from '$lib/components/ui/StackedBar.svelte';
	import Table from '$lib/components/ui/Table.svelte';
	import { NODE_ROLE_META, NODE_STATE_META, STORAGE_CATEGORY_META } from '$lib/service-types';
	import { formatBytes, formatCores, relativeTime } from '$lib/format';
	import { lastValue, type NodeStorage } from '$lib/types/metrics';
	import type { PageData } from './$types';

	// Node rows come from the (app) shell layout load; this page adds usage and
	// storage from its own load and presents them.
	let { data }: { data: PageData } = $props();

	const grid = 'grid-cols-[1.1fr_1.3fr_1.1fr_1fr_0.9fr_1.3fr_1.5fr_0.8fr]';

	const storage = $derived.by(() => {
		const byNode: Record<string, NodeStorage> = {};
		for (const node of data.nodeStorage?.nodes ?? []) byNode[node.name] = node;
		return byNode;
	});

	function storageSegments(node: NodeStorage) {
		const categories = node.categories as unknown as Record<string, number>;
		return STORAGE_CATEGORY_META.map((meta) => ({
			label: `${meta.label} ${formatBytes(categories[meta.key] ?? 0)}`,
			value: categories[meta.key] ?? 0,
			class: meta.class
		}));
	}

	// Current usage from the newest sampled bucket, keyed by node name.
	const usage = $derived.by(() => {
		const byNode: Record<
			string,
			{ cpu: number | null; cpuAlloc: number; mem: number | null; memAlloc: number }
		> = {};
		for (const node of data.nodeMetrics?.nodes ?? []) {
			byNode[node.name] = {
				cpu: lastValue(node.cpu_millicores),
				cpuAlloc: node.cpu_allocatable_millicores,
				mem: lastValue(node.memory_bytes),
				memAlloc: node.memory_allocatable_bytes
			};
		}
		return byNode;
	});

	const online = $derived(data.nodes.filter((n) => n.ready).length);
	const subtitleText = $derived.by(() => {
		if (data.nodes.length === 0) {
			return data.nodesObservation?.state === 'fresh'
				? 'no nodes in the cluster'
				: 'cluster not observed';
		}
		return online === data.nodes.length
			? `all ${data.nodes.length} online`
			: `${online}/${data.nodes.length} online`;
	});
</script>

<svelte:head>
	<title>Nodes — skali</title>
</svelte:head>

<PageHeader title="Nodes">
	{#snippet subtitle()}
		{subtitleText}
	{/snippet}
</PageHeader>

<div class="pb-6">
	{#if data.nodes.length > 0}
		<Table
			columns={['Node', 'Addresses', 'Roles', 'OS', 'Kubelet', 'Usage', 'Storage', 'State']}
			{grid}
		>
			{#each data.nodes as node (node.name)}
				{@const state = NODE_STATE_META[node.ready ? 'online' : 'offline']}
				{@const use = usage[node.name]}
				{@const disk = storage[node.name]}
				<div
					class="border-border-subtle border-b px-4.5 py-3 transition-colors last:border-0 hover:bg-white/2 @max-2xl:flex @max-2xl:flex-wrap @max-2xl:items-center @max-2xl:gap-x-3 @max-2xl:gap-y-2 @2xl:grid @2xl:items-center {grid}"
				>
					<div class="font-mono text-text-primary text-md @max-2xl:flex-1">{node.name}</div>
					<!-- Everything between name and state: grid cells on a wide pane, a
					     wrapping meta block under the name on a narrow one. -->
					<div
						class="@2xl:contents @max-2xl:order-1 @max-2xl:flex @max-2xl:basis-full @max-2xl:flex-wrap @max-2xl:items-center @max-2xl:gap-x-4 @max-2xl:gap-y-1.5"
					>
						<div class="flex flex-col">
							<span class="font-mono text-text-faint text-sm">
								{node.internal_ip ?? 'unknown'}
							</span>
							{#if node.external_ip}
								<span class="font-mono text-text-faint text-xs">{node.external_ip} public</span>
							{/if}
						</div>
						<div class="flex flex-wrap items-center gap-1">
							{#each [node.role, ...node.capabilities] as role (role)}
								{@const meta = NODE_ROLE_META[role] ?? NODE_ROLE_META.agent}
								<span class="font-mono rounded-full px-2 py-0.5 text-2xs {meta.text} {meta.bg}">
									{role}
								</span>
							{/each}
						</div>
						<div class="font-mono text-text-muted truncate pr-3 text-sm" title={node.os}>
							{node.os ?? 'unknown'}
						</div>
						<div class="font-mono text-text-muted text-sm">{node.kubelet_version ?? 'unknown'}</div>
						<div class="flex flex-col pr-3">
							{#if use && (use.cpu != null || use.mem != null)}
								<span class="font-mono text-text-muted text-sm">
									cpu {use.cpu != null ? formatCores(use.cpu) : 'n/a'} / {formatCores(use.cpuAlloc)}
								</span>
								<span class="font-mono text-text-faint text-xs">
									mem {use.mem != null ? formatBytes(use.mem) : 'n/a'} / {formatBytes(use.memAlloc)}
								</span>
							{:else}
								<span class="font-mono text-text-faint text-sm">no data</span>
							{/if}
						</div>
						<div class="flex flex-col justify-center gap-1 pr-4 @max-2xl:basis-full">
							{#if disk}
								<StackedBar segments={storageSegments(disk)} total={disk.capacity_bytes} />
								<span class="font-mono text-text-faint text-xs">
									{formatBytes(disk.used_bytes)} / {formatBytes(disk.capacity_bytes)}
								</span>
							{:else}
								<span class="font-mono text-text-faint text-sm">no data</span>
							{/if}
						</div>
					</div>
					<div
						class="flex items-center gap-1.5 text-md {state.text} @max-2xl:ml-auto"
						title={node.last_heartbeat
							? `heartbeat ${relativeTime(node.last_heartbeat)}`
							: undefined}
					>
						<span class="size-[8px] rounded-full {state.dot}"></span>
						{state.label}
					</div>
				</div>
			{/each}
		</Table>
		{#if data.nodeStorage?.nodes.length}
			<div class="mt-3 flex flex-wrap items-center gap-x-4 gap-y-1 px-4.5">
				{#each STORAGE_CATEGORY_META as meta (meta.key)}
					<span class="flex items-center gap-1.5 font-mono text-text-faint text-xs">
						<span class="size-[8px] rounded-full {meta.class}"></span>
						{meta.label}
					</span>
				{/each}
				<span class="font-mono text-text-faint text-xs">unfilled = free</span>
			</div>
		{/if}
	{:else}
		<EmptyState
			icon={Server}
			title="No nodes observed"
			description="the daemon runs api-only or the cluster has not synced yet"
		/>
	{/if}
</div>
