<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import Archive from '@lucide/svelte/icons/archive';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import { api, ApiError } from '$lib/api/client';
	import { deferred } from '$lib/deferred.svelte';
	import { formatBytes, formatDateTime, relativeTime } from '$lib/format';
	import type { StatCardData } from '$lib/models/view';
	import { dialog } from '$lib/stores/dialog.svelte';
	import { modal } from '$lib/stores/modal.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import { failingNow, type BackupTarget } from '$lib/types/backups';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import BackupTargetModal, {
		modalOptions as backupTargetModalOptions
	} from '$lib/components/system/BackupTargetModal.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Card from '$lib/components/ui/Card.svelte';
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
	import KeyValueRow from '$lib/components/ui/KeyValueRow.svelte';
	import Pill from '$lib/components/ui/Pill.svelte';
	import ProgressBar from '$lib/components/ui/ProgressBar.svelte';
	import Skeleton from '$lib/components/ui/Skeleton.svelte';
	import StatCard from '$lib/components/ui/StatCard.svelte';
	import type { PageData } from './$types';

	// Backups at a glance: whether the bucket answers, what it holds, and how
	// backups have been running, with the target's settings below. The
	// target is edited in a modal; without one the page offers to set it.
	let { data }: { data: PageData } = $props();

	const target = $derived(data.target);
	const summaryState = deferred(() => data.summary);
	// A summary for an older target (a reload in flight after an edit) is
	// not shown against the new one.
	const summary = $derived(
		summaryState.value && target && summaryState.value.target.updated_at === target.updated_at
			? summaryState.value
			: null
	);
	const loadingSummary = $derived(summaryState.pending && summary === null);

	let removing = $state(false);

	const status = $derived.by(() => {
		if (!target) return null;
		if (loadingSummary) return { text: 'checking', tone: 'neutral' as const };
		if (!summary) return { text: 'configured', tone: 'neutral' as const };
		return summary.reachable
			? { text: 'connected', tone: 'success' as const }
			: { text: 'unreachable', tone: 'danger' as const };
	});

	const endpointHost = $derived.by(() => {
		if (!target) return '';
		try {
			return new URL(target.endpoint).host;
		} catch {
			return target.endpoint;
		}
	});

	const stats = $derived.by((): StatCardData[] => {
		if (!summary) return [];
		const snapshots = summary.snapshots;
		const last = summary.runs.last_succeeded;
		const projects = snapshots?.projects.length ?? 0;
		return [
			{
				label: 'STORED',
				value: snapshots ? formatBytes(snapshots.bytes) : 'n/a',
				note: snapshots ? 'in snapshots' : 'bucket did not answer'
			},
			{
				label: 'SNAPSHOTS',
				value: snapshots ? `${snapshots.count}` : 'n/a',
				note: snapshots
					? `${projects} ${projects === 1 ? 'project' : 'projects'}`
					: 'bucket did not answer'
			},
			{
				label: 'LAST BACKUP',
				value: last ? relativeTime(last.finished_at) : 'never',
				note: last ? `${last.project} · ${last.environment}` : 'no backup yet'
			}
		];
	});

	// The last failure is worth a line while backups are failing, or while it
	// falls in the counted window.
	const lastFailure = $derived.by(() => {
		const failed = summary?.runs.last_failed;
		if (!summary || !failed) return null;
		const recent = Date.parse(failed.finished_at) > Date.parse(summary.runs.since);
		return failingNow(summary) || recent ? failed : null;
	});

	const running = $derived.by(() => {
		if (!summary) return '';
		const { backing_up: backups, restoring: restores } = summary.runs;
		const parts: string[] = [];
		if (backups > 0) parts.push(backups === 1 ? '1 backup' : `${backups} backups`);
		if (restores > 0) parts.push(restores === 1 ? '1 restore' : `${restores} restores`);
		return parts.join(', ');
	});

	async function editTarget(current: BackupTarget | null) {
		const saved = await modal.open<boolean>(
			BackupTargetModal,
			{ target: current },
			backupTargetModalOptions
		).result;
		if (saved) await invalidateAll();
	}

	function remove() {
		dialog.confirm({
			title: 'Remove the backup target?',
			description:
				'The stored credentials are deleted and scheduled backups stop. Snapshots already in the ' +
				'bucket are untouched; setting the same target again makes them listable and restorable.',
			confirmLabel: 'Remove target',
			variant: 'danger',
			onConfirm: async () => {
				removing = true;
				try {
					await api.del('/v1/system/backup-target');
					toast.success('Backup target removed');
					await invalidateAll();
				} catch (err) {
					toast.error(err instanceof ApiError ? err.message : 'Could not remove the backup target');
					throw err;
				} finally {
					removing = false;
				}
			}
		});
	}
</script>

<svelte:head>
	<title>Backups — Skali</title>
</svelte:head>

<PageHeader title="Backups">
	{#snippet titleTrailing()}
		{#if status}
			<Pill text={status.text} tone={status.tone} />
		{/if}
	{/snippet}
	{#snippet subtitle()}
		{#if target}
			<span class="font-mono text-text-faint text-md">{endpointHost}/{target.bucket}</span>
		{:else}
			<span>Snapshots go to an S3 bucket you own, off the cluster</span>
		{/if}
	{/snippet}
	{#snippet actions()}
		{#if target}
			<Button variant="ghost" onclick={remove} busy={removing}>Remove target</Button>
			<Button onclick={() => editTarget(target)}>Edit target</Button>
		{/if}
	{/snippet}
</PageHeader>

{#if !target}
	<div class="pb-6">
		<EmptyState
			icon={Archive}
			title="No backup target"
			description="configure one to back up environments and restore them; until then backups and schedules idle"
		>
			{#snippet action()}
				<Button variant="primary" onclick={() => editTarget(null)}>Set backup target</Button>
			{/snippet}
		</EmptyState>
	</div>
{:else}
	{#if summary && !summary.reachable}
		<div
			class="border-status-danger/40 bg-status-danger/10 mb-6.5 flex items-start gap-3 rounded-[15px] border px-5 py-4"
		>
			<TriangleAlert size={18} class="text-status-danger mt-0.5 flex-none" />
			<div class="flex min-w-0 flex-1 flex-col gap-1">
				<span class="text-text-primary text-lg font-medium">The bucket does not answer</span>
				{#if summary.unreachable_reason}
					<span class="font-mono text-status-danger text-sm break-words">
						{summary.unreachable_reason}
					</span>
				{/if}
				<span class="text-text-muted text-md">
					Backups and restores fail until it does. Check the endpoint, the bucket and the
					credentials.
				</span>
			</div>
			<Button size="sm" onclick={() => editTarget(target)}>Edit target</Button>
		</div>
	{/if}

	{#if loadingSummary}
		<div class="mb-6.5 grid grid-cols-1 gap-3.5 @2xl:grid-cols-3">
			{#each [0, 1, 2] as i (i)}
				<Skeleton variant="card" />
			{/each}
		</div>
	{:else if stats.length > 0}
		<div class="mb-6.5 grid grid-cols-1 gap-3.5 @2xl:grid-cols-3">
			{#each stats as stat (stat.label)}
				<StatCard {stat} />
			{/each}
		</div>
	{/if}

	<div class="grid grid-cols-1 gap-3.5 pb-6 @4xl:grid-cols-2">
		{#if summary}
			<Card class="flex flex-col p-5">
				<div class="mb-3.5 flex items-baseline gap-2.5">
					<h3 class="text-text-primary text-xl font-semibold">Last 7 days</h3>
					<span class="font-mono text-text-faint text-xs">
						since {formatDateTime(summary.runs.since)}
					</span>
				</div>
				<div class="flex flex-col">
					<KeyValueRow k="Succeeded" v={`${summary.runs.succeeded}`} labelWidth="w-44" />
					<div class="border-border-subtle flex items-baseline gap-3 border-b py-2">
						<div class="text-text-faint w-44 flex-none text-md">Failed</div>
						<div
							class="font-mono text-md {summary.runs.failed > 0
								? 'text-status-danger'
								: 'text-text-secondary'}"
						>
							{summary.runs.failed}
						</div>
					</div>
					{#if running}
						<KeyValueRow k="Running now" v={running} labelWidth="w-44" />
					{/if}
					<KeyValueRow
						k="Automatic backups"
						v={`${summary.schedules.scheduled} of ${summary.schedules.environments} environments`}
						labelWidth="w-44"
					/>
				</div>
				{#if lastFailure}
					<div
						class="border-status-danger/25 bg-status-danger/5 mt-3.5 flex flex-col gap-1 rounded-[11px] border px-4 py-3"
					>
						<div class="flex items-baseline justify-between gap-3">
							<span class="text-text-primary text-md font-medium">Last failure</span>
							<span class="text-text-muted text-sm">{relativeTime(lastFailure.finished_at)}</span>
						</div>
						<span class="font-mono text-text-muted text-sm">
							{lastFailure.project} · {lastFailure.environment}
						</span>
						{#if lastFailure.error}
							<span class="font-mono text-status-danger text-xs break-words">
								{lastFailure.error}
							</span>
						{/if}
					</div>
				{/if}
				<p class="text-text-faint mt-auto pt-3.5 text-sm">
					Automatic backups are scheduled per environment in its settings: a cron schedule (UTC) and
					a retention window.
				</p>
			</Card>
		{/if}

		<Card class="flex flex-col p-5">
			<h3 class="text-text-primary mb-3.5 text-xl font-semibold">Target</h3>
			<div class="flex flex-col">
				<KeyValueRow k="Endpoint" v={target.endpoint} labelWidth="w-36" />
				<KeyValueRow k="Region" v={target.region || '—'} labelWidth="w-36" />
				<KeyValueRow k="Bucket" v={target.bucket} labelWidth="w-36" />
				<KeyValueRow k="Prefix" v={target.prefix || '—'} labelWidth="w-36" />
				<KeyValueRow k="Access key id" v={target.access_key_id} labelWidth="w-36" />
				<KeyValueRow k="Secret access key" v="stored encrypted" labelWidth="w-36" />
				<KeyValueRow k="Last changed" v={formatDateTime(target.updated_at)} labelWidth="w-36" />
			</div>
			<p class="text-text-faint mt-auto pt-3.5 text-sm">
				Every project's snapshots are written under
				<span class="font-mono">skali/v1/&lt;project&gt;/&lt;environment&gt;/</span>. The layout is
				self-describing: a fresh installation pointed at the same bucket lists and restores them.
			</p>
		</Card>

		{#if summary?.snapshots}
			{@const snapshots = summary.snapshots}
			<Card class="flex flex-col p-5">
				<h3 class="text-text-primary mb-3.5 text-xl font-semibold">By project</h3>
				{#if snapshots.projects.length === 0}
					<p class="text-text-muted text-md">No snapshots yet.</p>
				{:else}
					<div class="flex flex-col">
						{#each snapshots.projects as project (project.project)}
							<div class="border-border-subtle flex flex-col gap-1.5 border-b py-2.5 last:border-0">
								<div class="flex items-baseline justify-between gap-3">
									<span class="font-mono text-text-secondary truncate text-md">
										{project.project}
									</span>
									<span class="font-mono text-text-primary flex-none text-md">
										{formatBytes(project.bytes)}
									</span>
								</div>
								<ProgressBar
									pct={snapshots.bytes > 0 ? (project.bytes / snapshots.bytes) * 100 : 0}
									class="bg-service-storage"
								/>
								<span class="text-text-faint text-sm">
									{project.count === 1 ? '1 snapshot' : `${project.count} snapshots`} · newest
									{relativeTime(project.newest_at)}
								</span>
							</div>
						{/each}
					</div>
				{/if}
				<p class="text-text-faint mt-auto pt-3.5 text-sm">
					Snapshots are listed and restored per project on its Backups tab.
				</p>
			</Card>

			<Card class="flex flex-col p-5">
				<h3 class="text-text-primary mb-3.5 text-xl font-semibold">Snapshots</h3>
				<div class="flex flex-col">
					<KeyValueRow k="Scheduled" v={`${snapshots.scheduled}`} labelWidth="w-56" />
					<KeyValueRow k="Manual" v={`${snapshots.manual}`} labelWidth="w-56" />
					{#if snapshots.orphaned > 0}
						<KeyValueRow
							k="From deleted environments"
							v={`${snapshots.orphaned}`}
							labelWidth="w-56"
						/>
					{/if}
					<KeyValueRow
						k="Oldest"
						v={snapshots.oldest_at ? formatDateTime(snapshots.oldest_at) : '—'}
						labelWidth="w-56"
					/>
					<KeyValueRow
						k="Newest"
						v={snapshots.newest_at ? formatDateTime(snapshots.newest_at) : '—'}
						labelWidth="w-56"
					/>
				</div>
				<p class="text-text-faint mt-auto pt-3.5 text-sm">
					Counted from the snapshots' manifests {relativeTime(summary.checked_at)}. Scheduled
					snapshots expire under their environment's retention, the newest is always kept; manual
					ones stay until deleted.
				</p>
			</Card>
		{/if}
	</div>
{/if}
