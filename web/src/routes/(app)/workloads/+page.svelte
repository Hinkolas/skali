<script lang="ts">
	import Plus from '@lucide/svelte/icons/plus';
	import Play from '@lucide/svelte/icons/play';
	import Square from '@lucide/svelte/icons/square';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import Pencil from '@lucide/svelte/icons/pencil';
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import Search from '@lucide/svelte/icons/search';
	import { invalidateAll } from '$app/navigation';
	import { api, ApiError } from '$lib/api/client';
	import { modal } from '$lib/stores/modal.svelte';
	import { dialog } from '$lib/stores/dialog.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import { relativeTime } from '$lib/format';
	import { ASSIGNMENT_PHASE_META, WORKLOAD_STATUS_META } from '$lib/service-types';
	import type { Workload } from '$lib/types/workloads';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Table from '$lib/components/ui/Table.svelte';
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
	import WorkloadModal, {
		modalOptions as workloadModalOptions,
		type WorkloadModalResult
	} from '$lib/components/workloads/WorkloadModal.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	const grid = 'grid-cols-[24px_1.6fr_2.2fr_0.7fr_0.9fr_1.1fr_0.9fr_120px]';

	// Anything mid-convergence tightens the poll so phases stream in.
	const settled = (w: Workload) => w.status === 'running' || w.status === 'stopped';
	$effect(() => {
		const busy = data.workloads.some((w) => !settled(w));
		const t = setInterval(() => invalidateAll(), busy ? 3_000 : 10_000);
		return () => clearInterval(t);
	});

	let query = $state('');
	const q = $derived(query.trim().toLowerCase());
	const workloads = $derived(
		data.workloads.filter(
			(w) => !q || w.name.toLowerCase().includes(q) || w.image.toLowerCase().includes(q)
		)
	);

	let expanded = $state<string | null>(null);
	function toggleExpand(id: string) {
		expanded = expanded === id ? null : id;
	}

	function readyCount(w: Workload): string {
		const target = w.desired_state === 'stopped' ? 'stopped' : 'ready';
		const done = w.assignments.filter(
			(a) => a.phase === target && a.generation === w.generation
		).length;
		return `${done}/${w.replicas}`;
	}
	function shortDigest(w: Workload): string {
		return w.image_digest ? w.image_digest.replace('sha256:', '').slice(0, 12) : '';
	}
	/** "retrying in 12s" from an assignment's backoff gate. */
	function retryIn(iso?: string): string {
		if (!iso) return '';
		const secs = Math.round((new Date(iso).getTime() - Date.now()) / 1000);
		return secs > 0 ? `retrying in ${secs}s` : 'retrying now';
	}

	let busyIds = $state<string[]>([]);
	const isBusy = (id: string) => busyIds.includes(id);
	async function mutate(id: string, fn: () => Promise<void>, fallback: string) {
		if (isBusy(id)) return;
		busyIds = [...busyIds, id];
		try {
			await fn();
			await invalidateAll();
		} catch (err) {
			toast.error(err instanceof ApiError ? err.message : fallback);
		} finally {
			busyIds = busyIds.filter((b) => b !== id);
		}
	}

	// Modal flows collect input only; the sudo-gated call runs after close so
	// the reauth modal slot stays free.
	async function createWorkload() {
		const result = await modal.open<WorkloadModalResult>(
			WorkloadModal,
			{ nodes: data.nodes.map((n) => ({ id: n.id, name: n.name })) },
			workloadModalOptions
		).result;
		if (!result) return;
		const name = result.body.name as string;
		await mutate(
			'create',
			async () => {
				await api.post('/v1/workloads', result.body);
				toast.success(`Declared ${name} — converging in the background`);
			},
			`Could not create ${name}`
		);
	}

	async function editWorkload(w: Workload) {
		const result = await modal.open<WorkloadModalResult>(
			WorkloadModal,
			{ nodes: data.nodes.map((n) => ({ id: n.id, name: n.name })), workload: w },
			workloadModalOptions
		).result;
		if (!result) return;
		await mutate(
			w.id,
			async () => {
				await api.patch(`/v1/workloads/${w.id}`, result.body);
				toast.success(`Updated ${w.name} — converging to the new spec`);
			},
			`Could not update ${w.name}`
		);
	}

	function startStop(w: Workload) {
		const desired = w.desired_state === 'stopped' ? 'running' : 'stopped';
		void mutate(
			w.id,
			async () => {
				await api.patch(`/v1/workloads/${w.id}`, { desired_state: desired });
				toast.success(`${w.name} converging to ${desired}`);
			},
			`Could not update ${w.name}`
		);
	}

	function removeWorkload(w: Workload) {
		dialog.confirm({
			title: `Delete ${w.name}?`,
			description: `All ${w.replicas} replica${w.replicas === 1 ? '' : 's'} are removed from their nodes, then the workload disappears. An offline node holds this up until it returns (or is removed).`,
			confirmLabel: 'Delete workload',
			variant: 'danger',
			onConfirm: async () => {
				try {
					await api.del(`/v1/workloads/${w.id}`);
					toast.success(`Deleting ${w.name} — converging to absence`);
					await invalidateAll();
				} catch (err) {
					toast.error(err instanceof ApiError ? err.message : `Could not delete ${w.name}`);
					throw err; // keep the dialog open
				}
			}
		});
	}
</script>

<svelte:head>
	<title>Workloads — skali</title>
</svelte:head>

<PageHeader title="Workloads">
	{#snippet subtitle()}
		{#if data.disabled}
			Workloads are disabled — they need the cluster image mirror
		{:else}
			{data.workloads.length} workload{data.workloads.length === 1 ? '' : 's'} declared; the
			reconciler keeps nodes converged
		{/if}
	{/snippet}
	{#snippet actions()}
		{#if !data.disabled}
			<Button variant="primary" busy={isBusy('create')} onclick={createWorkload}>
				<Plus size={15} strokeWidth={2.5} />
				New workload
			</Button>
		{/if}
	{/snippet}
</PageHeader>

{#if data.disabled}
	<EmptyState
		title="No mirror on this master"
		description="Workloads pull every image through the cluster registry: set CLUSTER_ADDR on the master and restart skalid to enable both."
	/>
{:else}
	<div class="flex flex-wrap items-center gap-2.5 pb-4">
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
		</div>
	</div>

	{#if workloads.length === 0}
		<EmptyState
			title={q ? 'No matching workloads' : 'Nothing declared yet'}
			description={q
				? 'Nothing matches the current search.'
				: 'Declare a workload — an image, a replica count, optional node pins — and the reconciler imports, places, and runs it.'}
		/>
	{:else}
		<Table columns={['', 'Workload', 'Image', 'Kind', 'Ready', 'Status', 'Updated', '']} {grid}>
			{#each workloads as w (w.id)}
				{@const meta = WORKLOAD_STATUS_META[w.status]}
				{@const busy = isBusy(w.id)}
				{@const open = expanded === w.id}
				{@const deleting = w.desired_state === 'deleting'}
				<div class="border-border-subtle border-b last:border-0">
					<div
						class="grid cursor-pointer items-center px-4.5 py-3 transition-colors hover:bg-white/2 {grid} {deleting
							? 'opacity-60'
							: ''}"
						onclick={() => toggleExpand(w.id)}
						onkeydown={(e) => e.key === 'Enter' && toggleExpand(w.id)}
						role="button"
						tabindex="0"
						aria-expanded={open}
					>
						<ChevronDown
							size={14}
							class="text-text-ghost transition-transform {open ? 'rotate-180' : ''}"
						/>
						<div class="text-text-primary truncate text-[13px] font-medium">{w.name}</div>
						<div class="flex min-w-0 flex-col gap-px">
							<span class="font-mono text-text-muted truncate text-[12px]">{w.image}</span>
							{#if w.image_digest}
								<span class="font-mono text-text-faint truncate text-[10.5px]"
									>@{shortDigest(w)}</span
								>
							{/if}
						</div>
						<div class="font-mono text-text-muted text-[11.5px]">{w.kind}</div>
						<div class="font-mono text-text-muted text-[12px]">{readyCount(w)}</div>
						<div>
							<span class="flex items-center gap-1.5 text-[11.5px] {meta.text}">
								<span class="size-1.5 flex-none rounded-full {meta.dot}"></span>
								{meta.label}
							</span>
						</div>
						<div class="font-mono text-text-muted text-[11px]" title={w.updated_at}>
							{relativeTime(w.updated_at)}
						</div>
						<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
						<div class="flex items-center justify-end gap-1" onclick={(e) => e.stopPropagation()}>
							{#if !deleting}
								<button
									type="button"
									disabled={busy}
									onclick={() => startStop(w)}
									class="text-text-ghost cursor-pointer rounded-lg p-1.5 transition-colors hover:bg-white/5 disabled:opacity-40 {w.desired_state ===
									'stopped'
										? 'hover:text-status-success'
										: 'hover:text-status-warning'}"
									aria-label={w.desired_state === 'stopped' ? `Start ${w.name}` : `Stop ${w.name}`}
									title={w.desired_state === 'stopped' ? 'Start' : 'Stop'}
								>
									{#if w.desired_state === 'stopped'}
										<Play size={14} />
									{:else}
										<Square size={14} />
									{/if}
								</button>
								<button
									type="button"
									disabled={busy}
									onclick={() => editWorkload(w)}
									class="text-text-ghost hover:text-text-primary cursor-pointer rounded-lg p-1.5 transition-colors hover:bg-white/5 disabled:opacity-40"
									aria-label="Edit {w.name}"
									title="Edit"
								>
									<Pencil size={14} />
								</button>
								<button
									type="button"
									disabled={busy}
									onclick={() => removeWorkload(w)}
									class="text-text-ghost hover:text-status-danger cursor-pointer rounded-lg p-1.5 transition-colors hover:bg-white/5 disabled:opacity-40"
									aria-label="Delete {w.name}"
									title="Delete"
								>
									<Trash2 size={14} />
								</button>
							{/if}
						</div>
					</div>

					{#if open}
						<div class="bg-surface-raised/40 flex flex-col gap-0.5 px-4.5 pt-1 pb-3">
							{#if w.last_error}
								<p class="text-status-danger px-8 py-1 text-[11.5px]">
									{w.last_error}
									{#if w.next_attempt_at}
										<span class="text-text-ghost">· {retryIn(w.next_attempt_at)}</span>
									{/if}
								</p>
							{/if}
							{#each w.assignments as a (a.ordinal)}
								{@const phase = ASSIGNMENT_PHASE_META[a.phase]}
								<div
									class="grid grid-cols-[24px_1.2fr_1.2fr_1fr_2.4fr_0.9fr] items-center px-8 py-1.5 text-[11.5px]"
								>
									<span class="font-mono text-text-ghost">#{a.ordinal}</span>
									<span class="font-mono text-text-muted truncate">{a.container_name}</span>
									<span class="text-text-muted truncate">{a.node_name || 'unplaced'}</span>
									<span class="flex items-center gap-1.5 {phase.text}">
										<span class="size-1.5 flex-none rounded-full {phase.dot}"></span>
										{phase.label}
									</span>
									<span class="text-status-danger truncate" title={a.last_error}>
										{#if a.last_error}
											{a.last_error}
											{#if a.next_attempt_at}
												<span class="text-text-ghost">· {retryIn(a.next_attempt_at)}</span>
											{/if}
										{/if}
									</span>
									<span class="font-mono text-text-ghost text-right" title={a.updated_at}>
										{relativeTime(a.updated_at)}
									</span>
								</div>
							{:else}
								<p class="text-text-ghost px-8 py-1.5 text-[11.5px]">No replicas planned yet.</p>
							{/each}
						</div>
					{/if}
				</div>
			{/each}
		</Table>
		<p class="text-text-ghost mt-3 text-[11px] leading-relaxed">
			Level-triggered: the reconciler compares desired state against what nodes report and fixes
			the difference — a killed container comes back, a drifted spec is replaced, an offline node
			is surfaced and healed on return.
		</p>
	{/if}
{/if}
