<script lang="ts">
	import Server from '@lucide/svelte/icons/server';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
	import Table from '$lib/components/ui/Table.svelte';
	import { NODE_ROLE_META, NODE_STATE_META } from '$lib/service-types';
	import { formatBytes, formatCores, relativeTime } from '$lib/format';
	import { lastValue } from '$lib/types/metrics';
	import type { PageData } from './$types';

	// Data comes from the (app) shell layout load; this page only presents it.
	let { data }: { data: PageData } = $props();

	const grid = 'grid-cols-[1.2fr_1.4fr_1.2fr_1.2fr_1fr_1.4fr_0.9fr]';

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
		const health =
			online === data.nodes.length
				? `all ${data.nodes.length} online`
				: `${online}/${data.nodes.length} online`;
		return data.org.version ? `${health} · skalid ${data.org.version}` : health;
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
		<Table columns={['Node', 'Addresses', 'Roles', 'OS', 'Kubelet', 'Usage', 'State']} {grid}>
			{#each data.nodes as node (node.name)}
				{@const state = NODE_STATE_META[node.ready ? 'online' : 'offline']}
				{@const use = usage[node.name]}
				<div
					class="border-border-subtle grid items-center border-b px-4.5 py-3 transition-colors last:border-0 hover:bg-white/2 {grid}"
				>
					<div class="font-mono text-text-primary text-md">{node.name}</div>
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
					<div
						class="flex items-center gap-1.5 text-md {state.text}"
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
	{:else}
		<EmptyState
			icon={Server}
			title="No nodes observed"
			description="The daemon is running api-only or the cluster has not synced yet."
		/>
	{/if}
</div>
