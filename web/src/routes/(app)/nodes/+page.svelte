<script lang="ts">
	import Plus from '@lucide/svelte/icons/plus';
	import Pencil from '@lucide/svelte/icons/pencil';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import { invalidateAll } from '$app/navigation';
	import { api, ApiError } from '$lib/api/client';
	import { modal } from '$lib/stores/modal.svelte';
	import { dialog } from '$lib/stores/dialog.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import { NODE_ROLE_META, NODE_STATE_META } from '$lib/service-types';
	import type { JoinTokenCreated, Node, NodeRole } from '$lib/types/nodes';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Table from '$lib/components/ui/Table.svelte';
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
	import AddNodeModal, {
		modalOptions as addNodeOptions
	} from '$lib/components/nodes/AddNodeModal.svelte';
	import EnrollCommandModal, {
		modalOptions as enrollCommandOptions
	} from '$lib/components/nodes/EnrollCommandModal.svelte';
	import EditNodeModal, {
		modalOptions as editNodeOptions
	} from '$lib/components/nodes/EditNodeModal.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	const onlineCount = $derived(data.nodes.filter((n) => n.status === 'online').length);

	const nodeGrid = 'grid-cols-[2.2fr_1.4fr_0.9fr_1.1fr_1fr_84px]';

	// Live-ish status: re-run the server load every 10s while the page is open.
	$effect(() => {
		const t = setInterval(() => invalidateAll(), 10_000);
		return () => clearInterval(t);
	});

	function relativeTime(iso: string | null): string {
		if (!iso) return 'never';
		const secs = Math.max(0, Math.floor((Date.now() - new Date(iso).getTime()) / 1000));
		if (secs < 60) return `${secs}s ago`;
		if (secs < 3600) return `${Math.floor(secs / 60)}m ago`;
		if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`;
		return `${Math.floor(secs / 86400)}d ago`;
	}

	// The sudo-gated mint call runs BETWEEN the two modals — never while one
	// is open — so the reauth modal (single modal slot) is not displaced.
	async function addNode() {
		const roles = await modal.open<NodeRole[]>(AddNodeModal, {}, addNodeOptions).result;
		if (!roles) return;
		try {
			const result = await api.post<JoinTokenCreated>('/v1/nodes/tokens', { roles });
			modal.open(EnrollCommandModal, { result }, enrollCommandOptions);
		} catch (err) {
			toast.error(err instanceof ApiError ? err.message : 'Could not create the join token');
		}
	}

	async function editNode(node: Node) {
		if (await modal.open<boolean>(EditNodeModal, { node }, editNodeOptions).result) {
			await invalidateAll();
		}
	}

	function deleteNode(node: Node) {
		dialog.confirm({
			title: `Remove ${node.name}?`,
			description:
				'The node is removed from the cluster and its certificate is revoked. The machine keeps running but is orphaned; re-adding it requires a fresh join token.',
			confirmLabel: 'Remove node',
			variant: 'danger',
			onConfirm: async () => {
				try {
					await api.del(`/v1/nodes/${node.id}`);
					toast.success(`Removed ${node.name}`);
					await invalidateAll();
				} catch (err) {
					toast.error(err instanceof ApiError ? err.message : 'Could not remove the node');
					throw err; // keep the dialog open
				}
			}
		});
	}
</script>

<svelte:head>
	<title>Nodes — skali</title>
</svelte:head>

<PageHeader title="Nodes">
	{#snippet subtitle()}
		{data.nodes.length} node{data.nodes.length === 1 ? '' : 's'} · {onlineCount} online
	{/snippet}
	{#snippet actions()}
		<Button variant="primary" onclick={addNode}>
			<Plus size={15} strokeWidth={2.5} />
			Add node
		</Button>
	{/snippet}
</PageHeader>

<div class="pb-6">
	{#if data.nodes.length === 0}
		<EmptyState
			title="No nodes yet"
			description="Add a node to start building your cluster: mint a join token and run the enroll command on the new machine."
		/>
	{:else}
		<Table columns={['Node', 'Roles', 'Status', 'Version', 'Last seen', '']} grid={nodeGrid}>
			{#each data.nodes as node (node.id)}
				{@const isMaster = node.roles.includes('master')}
				{@const state = NODE_STATE_META[node.status]}
				<div
					class="border-border-subtle grid items-center border-b px-4.5 py-3 transition-colors last:border-0 hover:bg-white/2 {nodeGrid}"
				>
					<div class="flex min-w-0 flex-col gap-px">
						<span class="text-text-primary truncate text-[13px] font-medium">{node.name}</span>
						<span class="font-mono text-text-faint truncate text-[11px]">
							{node.advertise_addr || '—'}
							{#if node.public_addr}
								&nbsp;· public {node.public_addr}
							{/if}
						</span>
					</div>
					<div class="flex flex-wrap items-center gap-1">
						{#each node.roles as role (role)}
							{@const meta = NODE_ROLE_META[role]}
							<span class="font-mono rounded-full px-2 py-0.5 text-[9.5px] {meta.text} {meta.bg}">
								{role}
							</span>
						{/each}
					</div>
					<div>
						<span class="flex items-center gap-1.5 text-[11.5px] {state.text}">
							<span class="size-1.5 rounded-full {state.dot}"></span>
							{state.label}
						</span>
					</div>
					<div class="flex min-w-0 flex-col gap-px">
						<span class="font-mono text-text-muted truncate text-[11px]">
							{node.skalid_version ?? '—'}
						</span>
						{#if node.os || node.arch}
							<span class="font-mono text-text-ghost truncate text-[10.5px]">
								{[node.os, node.arch].filter(Boolean).join('/')}
							</span>
						{/if}
					</div>
					<div class="font-mono text-text-muted text-[11px]">{relativeTime(node.last_seen)}</div>
					<div class="flex items-center justify-end gap-1">
						<button
							type="button"
							onclick={() => editNode(node)}
							class="text-text-ghost hover:text-text-secondary cursor-pointer rounded-lg p-1.5 transition-colors hover:bg-white/5"
							aria-label="Edit {node.name}"
							title="Edit"
						>
							<Pencil size={14} />
						</button>
						<button
							type="button"
							disabled={isMaster}
							onclick={() => deleteNode(node)}
							class="rounded-lg p-1.5 transition-colors {isMaster
								? 'text-text-ghost/40 cursor-default'
								: 'text-text-ghost hover:text-status-danger cursor-pointer hover:bg-white/5'}"
							aria-label="Remove {node.name}"
							title={isMaster ? 'The master node cannot be removed' : 'Remove'}
						>
							<Trash2 size={14} />
						</button>
					</div>
				</div>
			{/each}
		</Table>
	{/if}
</div>
