<script lang="ts">
	import Plus from '@lucide/svelte/icons/plus';
	import Pencil from '@lucide/svelte/icons/pencil';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import { invalidateAll, replaceState } from '$app/navigation';
	import { page } from '$app/state';
	import { api, ApiError } from '$lib/api/client';
	import { modal } from '$lib/stores/modal.svelte';
	import { dialog } from '$lib/stores/dialog.svelte';
	import { sidepanel } from '$lib/stores/sidepanel.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import { relativeTime } from '$lib/format';
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
	import NodeDetailPanel from '$lib/components/nodes/NodeDetailPanel.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	const onlineCount = $derived(data.nodes.filter((n) => n.status === 'online').length);

	const nodeGrid = 'grid-cols-[2.2fr_1.4fr_0.9fr_1.1fr_1fr_84px]';

	// Live-ish status: re-run the server load every 10s while the page is open.
	$effect(() => {
		const t = setInterval(() => invalidateAll(), 10_000);
		return () => clearInterval(t);
	});

	// --- selection: local state, mirrored into ?node= for deep links ---------
	// page.url CANNOT be the reactive source of truth here: shallow
	// replaceState updates the address bar but never reassigns page.url (kit
	// clones the old page object), so a $derived on its searchParams only sees
	// the param after a full load. Local state drives the panel; replaceState
	// mirrors it into the URL without re-running loads, adding history
	// entries, or firing afterNavigate (so the SidePanel host's route-close
	// can't misfire). Deep links restore the panel via the init value.
	let selected = $state(page.url.searchParams.get('node'));

	function select(id: string | null) {
		if (selected === id) return; // no-op (e.g. onClose after close)
		selected = id;
		// location, not page.url: page.url goes stale after the first shallow
		// replaceState and would resurrect the old param state.
		const url = new URL(location.href);
		if (id) url.searchParams.set('node', id);
		else url.searchParams.delete('node');
		replaceState(url, {});
	}

	// Panel lifecycle: re-runs when the selection OR the polled node list
	// changes. Same component + fresh props = in-place update (no remount, no
	// transition replay); the panel's history fetch keys on the node id, so
	// poll churn doesn't reset charts.
	$effect(() => {
		if (!selected) {
			sidepanel.close();
			return;
		}
		const node = data.nodes.find((n) => n.id === selected);
		if (!node) {
			select(null); // deleted underneath us, or a bogus deep link
			return;
		}
		sidepanel.open(
			NodeDetailPanel,
			{ node, onedit: () => editNode(node), onremove: () => deleteNode(node) },
			{ label: `${node.name} details`, onClose: () => select(null) }
		);
	});

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
				{@const isSelected = selected === node.id}
				<!-- Not a <button>: the row hosts the edit/delete buttons, and
				     interactive elements can't nest. -->
				<div
					role="button"
					tabindex="0"
					onclick={() => select(isSelected ? null : node.id)}
					onkeydown={(e) => {
						if (e.target !== e.currentTarget) return; // Enter on inner buttons must not toggle
						if (e.key === 'Enter' || e.key === ' ') {
							e.preventDefault();
							select(isSelected ? null : node.id);
						}
					}}
					class="border-border-subtle grid cursor-pointer items-center border-b px-4.5 py-3 transition-colors last:border-0 {isSelected
						? 'bg-accent/8 inset-ring inset-ring-accent/20'
						: 'hover:bg-white/2'} {nodeGrid}"
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
							onclick={(e) => {
								e.stopPropagation();
								editNode(node);
							}}
							class="text-text-ghost hover:text-text-secondary cursor-pointer rounded-lg p-1.5 transition-colors hover:bg-white/5"
							aria-label="Edit {node.name}"
							title="Edit"
						>
							<Pencil size={14} />
						</button>
						<button
							type="button"
							disabled={isMaster}
							onclick={(e) => {
								e.stopPropagation();
								deleteNode(node);
							}}
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
