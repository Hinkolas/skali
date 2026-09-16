<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'Restore snapshot',
		archetype: 'danger',
		size: 'lg'
	} satisfies ModalOptions;
</script>

<script lang="ts">
	// Restore flow for one snapshot: pick the environment it goes into
	// (the origin is preselected when it still exists and may be written),
	// read what will happen, type the environment name, go. The server
	// stops the environment, replaces matching databases, buckets, and
	// volumes, and resumes the current revision; on failure the environment
	// stays down. Restore is sudo-gated: the API client replays after a
	// fresh login on its own.
	import ArrowRight from '@lucide/svelte/icons/arrow-right';
	import DatabaseBackup from '@lucide/svelte/icons/database-backup';
	import Lock from '@lucide/svelte/icons/lock';
	import { api, ApiError } from '$lib/api/client';
	import { requiredTitle, roleAtLeast } from '$lib/access';
	import { formatBytes, formatDateTime } from '$lib/format';
	import { sidepanel } from '$lib/stores/sidepanel.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import ModalHeader from '$lib/components/ui/ModalHeader.svelte';
	import Pill from '$lib/components/ui/Pill.svelte';
	import TextInput from '$lib/components/ui/TextInput.svelte';
	import RunDetailPanel from '$lib/components/run/RunDetailPanel.svelte';
	import { snapshotContents, snapshotOrigin, type BackupSnapshot } from '$lib/types/backups';
	import type { Environment, Project } from '$lib/types/project';

	let {
		snapshot,
		environments,
		project,
		close
	}: {
		snapshot: BackupSnapshot;
		/** Every environment of the project, as restore targets. */
		environments: Environment[];
		project: Project;
		close: (restored?: boolean) => void;
	} = $props();

	// The reason a target is out of reach, or null when it may be picked.
	function refusal(t: Environment): string | null {
		if (t.access === 'none') return 'locked for you';
		if (!roleAtLeast(t.access, 'maintain')) return requiredTitle('maintain', 'environment', t.name);
		return null;
	}

	// svelte-ignore state_referenced_locally
	const origin = environments.find((e) => e.name === snapshot.environment) ?? null;
	let target = $state<Environment | null>(origin && !refusal(origin) ? origin : null);
	let typed = $state('');
	let restoring = $state(false);

	const crossEnvironment = $derived(!!target && target.name !== snapshot.environment);
	const armed = $derived(!!target && typed.trim() === target.name && !restoring);

	async function restore() {
		if (!armed || !target) return;
		restoring = true;
		try {
			const res = await api.post<{ run_id: string }>(`/v1/environments/${target.id}/restore`, {
				snapshot_id: snapshot.id
			});
			toast.success(`Restoring into ${target.name}`);
			close(true);
			sidepanel.open(RunDetailPanel, { runId: res.run_id }, { label: 'Run details' });
		} catch (err) {
			toast.error(err instanceof ApiError ? err.message : 'Could not start the restore');
		} finally {
			restoring = false;
		}
	}
</script>

<ModalHeader
	title="Restore snapshot into {target?.name ?? 'an environment'}"
	icon={DatabaseBackup}
	tone="danger"
>
	Taken from <span class="font-mono text-text-secondary">{snapshot.environment}</span> on
	{formatDateTime(snapshot.created_at)} · {snapshotOrigin(snapshot)} · {snapshotContents(snapshot)} ·
	{formatBytes(snapshot.bytes)}
</ModalHeader>

<div class="flex flex-col gap-4 px-5.5 py-4">
	<div>
		<div class="text-text-tertiary mb-1.5 text-base font-medium">Restore into</div>
		<div class="border-border-subtle flex flex-col rounded-xl border p-1.5" role="radiogroup">
			{#each environments as e (e.id)}
				{@const reason = refusal(e)}
				<button
					type="button"
					role="radio"
					aria-checked={target?.id === e.id}
					disabled={!!reason}
					title={reason ?? undefined}
					onclick={() => {
						target = e;
						typed = '';
					}}
					class="flex w-full items-center gap-2.5 rounded-[9px] px-3 py-2 text-left transition-colors disabled:cursor-default {target?.id ===
					e.id
						? 'bg-status-danger/10 inset-ring inset-ring-status-danger/25'
						: reason
							? ''
							: 'cursor-pointer hover:bg-white/4'}"
				>
					{#if e.access === 'none'}
						<Lock size={12} class="text-text-ghost flex-none" />
					{/if}
					<span class="font-mono text-md {reason ? 'text-text-ghost' : 'text-text-primary'}">
						{e.name}
					</span>
					{#if e.name === snapshot.environment}
						<Pill text="origin" />
					{/if}
					{#if reason}
						<span class="text-text-faint ml-auto min-w-0 truncate text-sm">{reason}</span>
					{:else if target?.id === e.id}
						<ArrowRight size={13} class="text-status-danger ml-auto flex-none" />
					{/if}
				</button>
			{:else}
				<p class="text-text-muted px-3 py-2 text-md">
					{project.name} has no environment to restore into; deploy one first.
				</p>
			{/each}
		</div>
	</div>

	{#if target}
		<div
			class="border-status-danger/25 bg-status-danger/8 text-text-secondary rounded-[11px] border px-3.5 py-3 text-md"
		>
			<p>
				<span class="font-mono text-text-primary">{target.name}</span> stops while the snapshot's databases,
				buckets, and volumes replace its current data, then the revision running there resumes. Current
				data is overwritten. If the restore fails the environment stays down until a restore succeeds
				or it is redeployed.
			</p>
			{#if crossEnvironment}
				<p class="mt-2">
					The snapshot comes from <span class="font-mono">{snapshot.environment}</span>; services
					match by key, and components {target.name} does not declare are skipped.
				</p>
			{/if}
		</div>

		<label class="flex flex-col gap-1.5">
			<span class="text-text-tertiary text-base font-medium">
				Type <span class="font-mono text-text-primary">{target.name}</span> to confirm
			</span>
			<TextInput bind:value={typed} mono autofocus placeholder={target.name} autocomplete="off" />
		</label>
	{/if}
</div>

<div class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3">
	<Button variant="secondary" onclick={() => close(false)} disabled={restoring}>Cancel</Button>
	<Button variant="danger" onclick={restore} disabled={!armed} busy={restoring}>
		Restore{target ? ` into ${target.name}` : ''}
	</Button>
</div>
