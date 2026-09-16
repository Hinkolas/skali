<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import Archive from '@lucide/svelte/icons/archive';
	import CalendarClock from '@lucide/svelte/icons/calendar-clock';
	import CloudOff from '@lucide/svelte/icons/cloud-off';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import Lock from '@lucide/svelte/icons/lock';
	import { api, ApiError } from '$lib/api/client';
	import { isInstanceAdmin, requiredTitle, roleAtLeast } from '$lib/access';
	import { backupRefusal, confirmBackup } from '$lib/backups';
	import { describeCron, describeSeconds, nextCronFire } from '$lib/cron';
	import { formatBytes, formatDateTime, relativeTime } from '$lib/format';
	import { dialog } from '$lib/stores/dialog.svelte';
	import { modal } from '$lib/stores/modal.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import type { Backup } from '$lib/types/definition';
	import { snapshotContents, snapshotOrigin, type BackupSnapshot } from '$lib/types/backups';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Card from '$lib/components/ui/Card.svelte';
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
	import Menu from '$lib/components/ui/Menu.svelte';
	import MenuItem from '$lib/components/ui/MenuItem.svelte';
	import Pill from '$lib/components/ui/Pill.svelte';
	import Segmented from '$lib/components/ui/Segmented.svelte';
	import Table from '$lib/components/ui/Table.svelte';
	import RunsSection from '$lib/components/run/RunsSection.svelte';
	import RestoreModal, {
		modalOptions as restoreModalOptions
	} from '$lib/components/run/RestoreModal.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	const projectName = $derived(data.project.name);
	const envName = $derived(data.env?.name ?? null);
	const snapshots = $derived(data.snapshots ?? []);

	// Policies come from the draft definition: what the next deployment
	// runs, which is also what the active revisions run unless the manifest
	// changed since. Each row names its schedule, retention, and what it
	// includes; the listing tells when the policy last produced a snapshot.
	const policies = $derived(
		Object.entries(data.definition?.backups ?? {}).toSorted(([a], [b]) => a.localeCompare(b))
	);
	function includes(b: Backup): string {
		const parts: string[] = [];
		if (b.include.allDatabases) parts.push('all databases');
		else if (b.include.databases?.length) parts.push(`databases ${b.include.databases.join(', ')}`);
		if (b.include.allBuckets) parts.push('all buckets');
		else if (b.include.buckets?.length) parts.push(`buckets ${b.include.buckets.join(', ')}`);
		if (b.include.allVolumes) parts.push('all volumes');
		else if (b.include.volumes?.length) parts.push(`volumes ${b.include.volumes.join(', ')}`);
		return parts.join(' · ') || 'nothing';
	}
	function lastScheduled(policy: string): BackupSnapshot | undefined {
		return snapshots.find(
			(s) =>
				s.trigger === 'scheduled' && s.policy === policy && (!envName || s.environment === envName)
		);
	}

	// The listing is project-wide; the segmented control narrows it to the
	// selected environment, which is also where "Back up now" goes.
	let scope = $state<'all' | 'env'>('all');
	const shown = $derived(
		scope === 'env' && envName ? snapshots.filter((s) => s.environment === envName) : snapshots
	);
	const envCount = $derived(
		envName ? snapshots.filter((s) => s.environment === envName).length : 0
	);

	const backupTitle = $derived(backupRefusal(data.env));

	// Restore needs maintain on the target, delete maintain on the origin
	// (or project admin when the origin environment is gone). The server
	// enforces both; the menu explains what it would say.
	const envAccess = $derived(new Map(data.environments.map((e) => [e.name, e.access])));
	function deleteRefusal(s: BackupSnapshot): string | undefined {
		const access = envAccess.get(s.environment);
		if (access === undefined) {
			return roleAtLeast(data.project.access.role, 'admin') || isInstanceAdmin(data.user)
				? undefined
				: requiredTitle('admin', 'project', projectName);
		}
		return roleAtLeast(access, 'maintain')
			? undefined
			: requiredTitle('maintain', 'environment', s.environment);
	}
	const anyRestoreTarget = $derived(
		data.environments.some((e) => roleAtLeast(e.access, 'maintain'))
	);

	function restore(snapshot: BackupSnapshot) {
		modal.open(
			RestoreModal,
			{ snapshot, environments: data.environments, project: data.project },
			restoreModalOptions
		);
	}

	function remove(snapshot: BackupSnapshot) {
		dialog.confirm({
			title: 'Delete this snapshot?',
			description:
				`Taken from ${snapshot.environment} ${relativeTime(snapshot.created_at)} (${snapshotContents(snapshot)}, ` +
				`${formatBytes(snapshot.bytes)}). Its data is removed from the backup target and cannot be restored afterwards.`,
			confirmLabel: 'Delete snapshot',
			variant: 'danger',
			onConfirm: async () => {
				try {
					await api.del(`/v1/projects/${data.project.id}/backups/${snapshot.id}`);
					toast.success('Snapshot deleted');
					await invalidateAll();
				} catch (err) {
					toast.error(err instanceof ApiError ? err.message : 'Could not delete the snapshot');
					throw err;
				}
			}
		});
	}

	const short = (id: string) => id.slice(0, 8);
	const grid = 'grid-cols-[1fr_1fr_1.1fr_1.4fr_1.4fr_0.8fr_0.5fr]';

	// The listing failed for a reason the reader can act on: no target yet
	// (an admin sets one under System), or the target did not answer.
	const refusal = $derived(data.refusal);
	const unconfigured = $derived(refusal?.code === 'backup_target_unconfigured');
</script>

<svelte:head>
	<title>Backups · {data.project.display_name || projectName} — skali</title>
</svelte:head>

<PageHeader title="Backups">
	{#snippet subtitle()}
		{projectName} · snapshots on the backup target
	{/snippet}
	{#snippet actions()}
		<Button
			variant="primary"
			disabled={!!backupTitle || !!refusal}
			title={backupTitle ?? (refusal ? refusal.message : undefined)}
			onclick={() => data.env && confirmBackup(data.env)}
		>
			Back up {envName ?? 'now'}
		</Button>
	{/snippet}
</PageHeader>

<div class="flex flex-col gap-3.5 pb-6">
	<Card class="p-5">
		<div class="mb-3 flex flex-wrap items-baseline gap-x-2.5 gap-y-0.5">
			<h3 class="text-text-primary text-xl font-semibold">Policies</h3>
			<span class="text-text-muted text-md">
				from skali.yaml · every active environment is snapshotted on schedule (UTC)
			</span>
		</div>
		{#if policies.length === 0}
			<p class="text-text-muted text-md">
				No backup policy is declared. Add a <span class="font-mono">backups:</span> block with a
				cron <span class="font-mono">schedule</span>, a <span class="font-mono">retention</span>
				window, and what to <span class="font-mono">include</span>; skali takes and expires the
				snapshots for you. Manual snapshots work without one.
			</p>
		{:else}
			<div class="flex flex-col">
				{#each policies as [name, policy] (name)}
					{@const next = nextCronFire(policy.schedule)}
					{@const last = lastScheduled(name)}
					<div
						class="border-border-subtle grid grid-cols-1 gap-x-4 gap-y-1.5 border-b py-3 last:border-0 @3xl:grid-cols-[minmax(0,1fr)_minmax(0,1.6fr)_minmax(0,1fr)] @3xl:items-center"
					>
						<div class="flex min-w-0 items-center gap-2">
							<CalendarClock size={15} class="text-text-ghost flex-none" />
							<span class="font-mono text-text-primary truncate text-md">{name}</span>
							<Pill text={policy.strategy ?? 'complete'} />
						</div>
						<div class="text-text-secondary min-w-0 text-md">
							{describeCron(policy.schedule)} UTC · keeps {describeSeconds(policy.retentionSeconds)}
							<div class="text-text-muted truncate text-sm" title={includes(policy)}>
								{includes(policy)}
							</div>
						</div>
						<div class="font-mono text-text-muted text-sm">
							{#if next}
								<div title="{next.toISOString()} (your local time is shown)">
									next {formatDateTime(next.toISOString())} local
								</div>
							{/if}
							<div>
								{#if last}
									last {relativeTime(last.created_at)}{envName ? ` in ${envName}` : ''}
								{:else}
									no snapshot yet{envName ? ` in ${envName}` : ''}
								{/if}
							</div>
						</div>
					</div>
				{/each}
			</div>
		{/if}
	</Card>

	<div class="mt-2 flex flex-wrap items-center gap-x-2.5 gap-y-2">
		<h3 class="text-text-primary text-xl font-semibold">Snapshots</h3>
		<span class="text-text-muted text-md">
			newest first · manual snapshots stay until deleted, scheduled ones expire under their policy
		</span>
		{#if envName && !refusal}
			<div class="ml-auto">
				<Segmented
					label="Snapshot scope"
					value={scope}
					onchange={(id) => (scope = id as 'all' | 'env')}
					segments={[
						{ id: 'all', label: 'All environments', count: snapshots.length },
						{ id: 'env', label: envName, count: envCount }
					]}
				/>
			</div>
		{/if}
	</div>

	{#if refusal}
		{#if unconfigured}
			<EmptyState
				icon={CloudOff}
				title="No backup target"
				description="snapshots need an external S3 location; an admin sets it under System"
			>
				{#snippet action()}
					{#if isInstanceAdmin(data.user)}
						<Button variant="primary" href={resolve('/(app)/system/backups')}
							>Set backup target</Button
						>
					{/if}
				{/snippet}
			</EmptyState>
		{:else if refusal.code === 'backup_target_unreachable'}
			<EmptyState
				icon={CloudOff}
				title="The backup target did not answer"
				description={refusal.message}
			/>
		{:else if refusal.status === 403}
			<EmptyState
				icon={Lock}
				title="Snapshots are not visible to you"
				description="read on the project is required to list its snapshots"
			/>
		{:else}
			<EmptyState icon={CloudOff} title="Could not list snapshots" description={refusal.message} />
		{/if}
	{:else if shown.length === 0}
		<EmptyState
			icon={Archive}
			title="No snapshots yet"
			description={scope === 'env' && envName
				? `nothing has been backed up from ${envName}`
				: 'back up an environment now, or declare a backups policy in skali.yaml'}
		/>
	{:else}
		<Table
			columns={['Snapshot', 'Environment', 'Origin', 'Created', 'Contents', 'Size', '']}
			{grid}
		>
			{#each shown as snapshot (snapshot.id)}
				{@const origin = snapshotOrigin(snapshot)}
				{@const removeTitle = deleteRefusal(snapshot)}
				<div
					class="border-border-subtle border-b px-4.5 py-3 transition-colors last:border-0 hover:bg-white/2 @max-2xl:flex @max-2xl:flex-wrap @max-2xl:items-center @max-2xl:gap-x-3 @max-2xl:gap-y-1.5 @2xl:grid @2xl:items-center {grid}"
				>
					<div class="font-mono text-text-primary text-md" title={snapshot.id}>
						{short(snapshot.id)}
					</div>
					<div class="@max-2xl:order-first">
						<Pill
							text={snapshot.environment}
							tone={snapshot.environment === 'production' ? 'success' : 'neutral'}
						/>
					</div>
					<div class="text-md">
						{#if snapshot.trigger === 'scheduled'}
							<span class="text-text-secondary inline-flex items-center gap-1.5">
								<CalendarClock size={13} class="text-text-ghost" />
								{origin}
							</span>
						{:else}
							<span class="text-text-muted">manual</span>
						{/if}
					</div>
					<div
						class="text-text-muted text-md @max-2xl:basis-full"
						title={formatDateTime(snapshot.created_at)}
					>
						{formatDateTime(snapshot.created_at)}
						<span class="text-text-faint">· {relativeTime(snapshot.created_at)}</span>
					</div>
					<div class="text-text-muted text-md">
						{snapshotContents(snapshot)}
						<span class="font-mono text-text-faint text-xs">
							· rev {snapshot.revision_checksum.replace(/^sha256:/, '').slice(0, 8)}
						</span>
					</div>
					<div class="font-mono text-text-muted text-sm">{formatBytes(snapshot.bytes)}</div>
					<div class="flex justify-end @max-2xl:ml-auto">
						<Menu
							label="Actions on snapshot {short(snapshot.id)}"
							align="end"
							triggerClass="flex size-7 cursor-pointer items-center justify-center rounded-[8px] text-text-tertiary transition-colors hover:bg-white/5 hover:text-text-primary"
						>
							{#snippet trigger()}
								<Ellipsis size={15} />
							{/snippet}
							<MenuItem
								disabled={!anyRestoreTarget}
								title={anyRestoreTarget
									? 'Replay this snapshot into an environment; it stops while data is written.'
									: 'maintain on an environment is required to restore'}
								onselect={() => restore(snapshot)}
							>
								Restore…
							</MenuItem>
							<MenuItem
								danger
								disabled={!!removeTitle}
								title={removeTitle ?? 'Remove the snapshot from the backup target for good.'}
								onselect={() => remove(snapshot)}
							>
								Delete
							</MenuItem>
						</Menu>
					</div>
				</div>
			{/each}
		</Table>
	{/if}

	{#if data.env && data.env.access !== 'none'}
		<div class="mt-2 flex flex-wrap items-baseline gap-x-2.5 gap-y-0.5">
			<h3 class="text-text-primary text-xl font-semibold">Activity</h3>
			<span class="text-text-muted text-md">backup and restore runs of {data.env.name}</span>
		</div>
		<RunsSection
			envId={data.env.id}
			seed={data.runs}
			kinds={['backup', 'restore']}
			emptyTitle="No backup runs yet"
			emptyDescription="snapshots and restores of {data.env.name} appear here"
		/>
	{/if}
</div>
