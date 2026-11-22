<script lang="ts">
	import { resolve } from '$app/paths';
	import HardDrive from '@lucide/svelte/icons/hard-drive';
	import PageHeader from '#lib/components/shell/PageHeader.svelte';
	import ObjectStoreVolumeSizeCard from '#lib/components/system/ObjectStoreVolumeSizeCard.svelte';
	import Card from '#lib/components/ui/Card.svelte';
	import EmptyState from '#lib/components/ui/EmptyState.svelte';
	import Pill from '#lib/components/ui/Pill.svelte';
	import ProgressBar from '#lib/components/ui/ProgressBar.svelte';
	import Segmented from '#lib/components/ui/Segmented.svelte';
	import StatCard from '#lib/components/ui/StatCard.svelte';
	import Table from '#lib/components/ui/Table.svelte';
	import TypeBadge from '#lib/components/ui/TypeBadge.svelte';
	import { formatBytes, formatCount, formatDateTime, relativeTime } from '#lib/format.ts';
	import { objectStoreStats } from '#lib/models/objectstorage.ts';
	import {
		bucketPhase,
		formatVolumeSize,
		headroomState,
		sortBuckets,
		storeDegraded,
		storeSummary,
		type ObjectStoreBucket
	} from '#lib/types/objectstorage.ts';
	import { withEnv } from '#lib/urls.ts';
	import type { PageData } from './$types';

	// The object store at a glance: how many buckets it can still take,
	// the volume size that decides it, and every bucket by what it stores.
	let { data }: { data: PageData } = $props();

	const store = $derived(data.store);
	const inventory = $derived(data.inventory);

	let sort = $state<'size' | 'slots' | 'name'>('size');
	const rows = $derived.by(() => {
		const buckets = inventory?.buckets ?? [];
		if (sort === 'name')
			return buckets.toSorted((a, b) => a.bucket_name.localeCompare(b.bucket_name));
		if (sort === 'slots') {
			return buckets.toSorted(
				(a, b) =>
					(b.usage?.slots ?? -1) - (a.usage?.slots ?? -1) ||
					a.bucket_name.localeCompare(b.bucket_name)
			);
		}
		return sortBuckets(buckets);
	});
	const totalUsed = $derived(
		(inventory?.buckets ?? []).reduce((sum, b) => sum + (b.usage?.used_bytes ?? 0), 0)
	);

	const stats = $derived(store ? objectStoreStats(store, inventory) : []);
	const summary = $derived(store ? storeSummary(store) : null);
	const headroom = $derived(store ? headroomState(store) : 'unknown');
	const facts = $derived.by(() => {
		if (!store) return '';
		const parts = [
			store.image.split(':').at(-1) ? `SeaweedFS ${store.image.split(':').at(-1)}` : 'SeaweedFS'
		];
		if (store.managed) {
			parts.push(
				`${store.masters} master${store.masters === 1 ? '' : 's'}`,
				`${store.volume_servers} volume server${store.volume_servers === 1 ? '' : 's'}`
			);
		} else {
			parts.push('local all-in-one');
		}
		parts.push(`replication ${store.replication}`);
		return parts.join(' · ');
	});

	const grid = 'grid-cols-[1.5fr_1.5fr_1.2fr_1.3fr_0.7fr_0.8fr_0.9fr]';

	function projectHref(bucket: ObjectStoreBucket): string | null {
		if (!bucket.project) return null;
		return withEnv(
			resolve('/(app)/projects/[project]', { project: bucket.project.name }),
			bucket.environment?.name
		);
	}
	function serviceHref(bucket: ObjectStoreBucket): string | null {
		if (!bucket.project || !bucket.service_key) return null;
		return withEnv(
			resolve('/(app)/projects/[project]/services/[service]', {
				project: bucket.project.name,
				service: bucket.service_key
			}),
			bucket.environment?.name
		);
	}
	function share(bucket: ObjectStoreBucket): number {
		if (!bucket.usage || totalUsed <= 0) return 0;
		return (bucket.usage.used_bytes / totalUsed) * 100;
	}
	function shareText(bucket: ObjectStoreBucket): string {
		const pct = share(bucket);
		if (pct === 0) return '';
		return pct < 1 ? '<1%' : `${Math.round(pct)}%`;
	}
</script>

<svelte:head>
	<title>Object storage — Skali</title>
</svelte:head>

<PageHeader title="Object storage">
	{#snippet titleTrailing()}
		{#if summary}
			<Pill text={summary.text} tone={summary.tone} title={summary.title} />
		{/if}
	{/snippet}
	{#snippet subtitle()}
		{#if store}
			<span class="font-mono text-text-faint text-md">{facts}</span>
		{:else}
			S3 buckets and the store behind them
		{/if}
	{/snippet}
</PageHeader>

{#if !store}
	<div class="pb-6">
		<EmptyState
			icon={HardDrive}
			title="No object store"
			description="the store comes up once a node carries the object-storage capability"
		/>
	</div>
{:else}
	{#if headroom === 'exhausted' || headroom === 'low'}
		<Card class="mb-6.5 p-5">
			<div
				class="rounded-[11px] border px-3.5 py-3 {headroom === 'exhausted'
					? 'border-status-danger/25 bg-status-danger/10'
					: 'border-status-warning/25 bg-status-warning/10'}"
			>
				<p
					class="font-medium {headroom === 'exhausted'
						? 'text-status-danger'
						: 'text-status-warning'}"
				>
					{headroom === 'exhausted' ? 'No room for another bucket' : 'Close to the bucket limit'}
				</p>
				<p class="text-text-secondary mt-1 text-md">
					{#if headroom === 'exhausted'}
						A new bucket accepts reads but refuses every write, and buckets whose volumes are full
						stop taking writes too.
					{:else}
						About {store.capacity?.bucket_headroom} more buckets fit before new ones refuse writes.
					{/if}
					Every bucket holds at least {(store.capacity?.volumes_per_bucket ?? 1) *
						(store.capacity?.copies ?? 1)} volume slots whatever it stores. Lower the volume size below
					(currently {formatVolumeSize(store.volume_size.limit_mb)}), free the slots of the leftover
					collections listed further down, or add disk to the volume servers.
				</p>
			</div>
		</Card>
	{/if}

	{#if store.observed?.state === 'fresh' && storeDegraded(store)}
		<p class="text-status-warning mb-3.5 text-md">
			The store serves below its recorded shape: {store.observed.masters_ready}/{store.masters} masters,
			{store.observed.volume_servers_ready}/{store.volume_servers} volume servers{store.observed
				.under_replicated_volumes > 0
				? `, ${store.observed.under_replicated_volumes} volumes short of a copy`
				: ''}{!store.observed.filer_ready ? ', metadata service down' : ''}.
		</p>
	{/if}

	<div class="mb-6.5 grid grid-cols-2 gap-3.5 @4xl:grid-cols-4">
		{#each stats as stat (stat.label)}
			<StatCard {stat} />
		{/each}
	</div>

	<div class="mb-6.5 grid grid-cols-1 gap-3.5 @4xl:grid-cols-2">
		{#key store.updated_at}
			<ObjectStoreVolumeSizeCard {store} />
		{/key}
		<Card class="flex flex-col p-5">
			<div class="mb-1 flex flex-wrap items-baseline gap-x-3 gap-y-1">
				<h3 class="text-text-primary text-xl font-semibold">Volume servers</h3>
				{#if store.capacity}
					<span class="text-text-muted text-md">
						{store.capacity.copies === 1
							? 'one copy of every volume'
							: `${store.capacity.copies} copies of every volume, on different servers`}
					</span>
				{/if}
			</div>
			<p class="text-text-muted mb-3 text-base">
				Slots follow each server's free disk, so they shift as data is written.
			</p>
			{#if store.capacity && store.capacity.servers.length > 0}
				<div class="flex flex-col gap-3">
					{#each store.capacity.servers as server (server.url)}
						<div class="flex flex-col gap-1.5">
							<div class="flex items-baseline justify-between gap-3">
								<span class="text-text-secondary truncate font-mono text-md" title={server.url}>
									{server.node || server.url}
								</span>
								<span class="text-text-muted flex-none font-mono text-xs">
									{server.used_slots} / {server.slots} · {server.free_slots} free
								</span>
							</div>
							<ProgressBar
								pct={server.slots > 0 ? (server.used_slots / server.slots) * 100 : 100}
								class={server.free_slots === 0 ? 'bg-status-danger' : 'bg-service-storage'}
							/>
						</div>
					{/each}
				</div>
			{:else}
				<div class="font-mono text-text-faint py-2 text-md">
					{store.capacity ? 'no volume server has announced itself' : 'the master did not answer'}
				</div>
			{/if}
		</Card>
	</div>

	<div class="mb-3.5 flex flex-wrap items-center gap-x-3 gap-y-2">
		<div class="flex min-w-56 flex-1 flex-wrap items-baseline gap-x-2.5 gap-y-0.5">
			<h2 class="text-text-primary text-xl font-semibold">Buckets</h2>
			<div class="text-text-muted text-md">
				{#if inventory?.usage_observed}
					stored bytes counted once, whatever the replication
				{:else if inventory}
					usage unknown: the master did not answer
				{:else}
					the bucket list could not be loaded
				{/if}
			</div>
		</div>
		{#if rows.length > 1}
			<div class="ml-auto">
				<Segmented
					label="Sort buckets"
					value={sort}
					onchange={(id) => (sort = id as typeof sort)}
					segments={[
						{ id: 'size', label: 'largest' },
						{ id: 'slots', label: 'most slots' },
						{ id: 'name', label: 'name' }
					]}
				/>
			</div>
		{/if}
	</div>

	<div class="mb-6.5">
		{#if rows.length > 0}
			<Table
				columns={['Bucket', 'Owner', 'Service', 'Stored', 'Slots', 'Status', 'Created']}
				{grid}
			>
				{#each rows as bucket (bucket.bucket_name)}
					{@const phase = bucketPhase(bucket.phase)}
					{@const project = projectHref(bucket)}
					{@const service = serviceHref(bucket)}
					<div
						class="border-border-subtle border-b px-4.5 py-3 transition-colors last:border-0 hover:bg-white/2 @max-2xl:flex @max-2xl:flex-wrap @max-2xl:items-center @max-2xl:gap-x-3 @max-2xl:gap-y-2 @2xl:grid @2xl:items-center {grid}"
					>
						<div class="flex min-w-0 flex-col pr-3 @max-2xl:flex-1">
							<span class="text-text-primary truncate font-mono text-md">{bucket.bucket_name}</span>
							{#if bucket.visibility === 'public-read'}
								<span class="text-text-faint font-mono text-xs">public read</span>
							{/if}
						</div>
						<div
							class="@2xl:contents @max-2xl:order-1 @max-2xl:flex @max-2xl:basis-full @max-2xl:flex-wrap @max-2xl:items-center @max-2xl:gap-x-4 @max-2xl:gap-y-1.5"
						>
							<div class="flex min-w-0 items-center gap-2 pr-3">
								{#if bucket.project && project}
									<!-- eslint-disable svelte/no-navigation-without-resolve -- resolved path, env appended by #lib/urls -->
									<a href={project} class="text-text-secondary truncate text-md hover:underline">
										{bucket.project.display_name || bucket.project.name}
									</a>
									<!-- eslint-enable svelte/no-navigation-without-resolve -->
									{#if bucket.environment}
										<Pill text={bucket.environment.name} />
									{/if}
								{:else}
									<span class="text-text-muted text-md">system</span>
								{/if}
							</div>
							<div class="flex min-w-0 items-center gap-2 pr-3">
								{#if service}
									<TypeBadge kind="bucket" />
									<!-- eslint-disable svelte/no-navigation-without-resolve -- resolved path, env appended by #lib/urls -->
									<a
										href={service}
										class="text-text-secondary truncate font-mono text-md hover:underline"
									>
										{bucket.service_key}
									</a>
									<!-- eslint-enable svelte/no-navigation-without-resolve -->
								{:else}
									<span class="text-text-muted truncate font-mono text-md">
										{bucket.system_key ?? '–'}
									</span>
								{/if}
							</div>
							<div class="flex min-w-0 flex-col gap-1 pr-3 @max-2xl:min-w-40">
								{#if bucket.usage}
									<div class="flex items-baseline justify-between gap-2">
										<span class="text-text-secondary font-mono text-md">
											{formatBytes(bucket.usage.used_bytes)}
										</span>
										<span class="text-text-faint font-mono text-xs">{shareText(bucket)}</span>
									</div>
									<ProgressBar pct={share(bucket)} class="bg-service-storage" />
									<span
										class="text-text-faint truncate font-mono text-xs"
										title={`${formatCount(bucket.usage.entry_count)} entries; ${formatBytes(bucket.usage.disk_bytes)} on disk until deleted objects are vacuumed`}
									>
										{bucket.quota_bytes > 0
											? `of ${formatBytes(bucket.quota_bytes)} quota`
											: 'no quota'}
									</span>
								{:else}
									<span class="text-text-faint font-mono text-md">unknown</span>
								{/if}
							</div>
							<div
								class="text-text-secondary pr-3 font-mono text-md"
								title={bucket.usage
									? `${bucket.usage.volumes} volume${bucket.usage.volumes === 1 ? '' : 's'}, ${bucket.usage.slots} copies`
									: undefined}
							>
								{#if bucket.usage}
									{bucket.usage.slots === 0 ? 'none yet' : bucket.usage.slots}
								{:else}
									–
								{/if}
							</div>
							<div><Pill text={phase.text} tone={phase.tone} /></div>
						</div>
						<div
							class="text-text-faint font-mono text-xs @max-2xl:ml-auto"
							title={formatDateTime(bucket.created_at)}
						>
							{relativeTime(bucket.created_at)}
						</div>
					</div>
				{/each}
			</Table>
		{:else}
			<EmptyState
				icon={HardDrive}
				title="No buckets yet"
				description="the first declared bucket lands here"
			/>
		{/if}
	</div>

	{#if inventory && inventory.unclaimed.length > 0}
		<Card class="mb-6.5 p-5">
			<div class="mb-1 flex flex-wrap items-baseline gap-x-3 gap-y-1">
				<h3 class="text-text-primary text-xl font-semibold">Leftover collections</h3>
				<span class="text-text-muted text-md">
					{inventory.unclaimed.reduce((sum, c) => sum + c.usage.slots, 0)} slots held
				</span>
			</div>
			<p class="text-text-muted mb-3 text-base">
				Collections on the volume servers that no live bucket claims: left behind by a bucket whose
				deletion did not finish, or created outside Skali. They hold their slots until they are
				removed in the SeaweedFS shell; removing one deletes its data for good.
			</p>
			<div class="flex flex-col">
				{#each inventory.unclaimed as collection (collection.collection)}
					<div class="border-border-subtle flex items-center gap-3 border-b py-2 last:border-0">
						<span class="text-text-secondary min-w-0 flex-1 truncate font-mono text-md">
							{collection.collection}
						</span>
						<span class="text-text-muted font-mono text-xs">
							{formatBytes(collection.usage.used_bytes)}
						</span>
						<span class="text-text-secondary w-20 text-right font-mono text-md">
							{collection.usage.slots} slot{collection.usage.slots === 1 ? '' : 's'}
						</span>
					</div>
				{/each}
			</div>
		</Card>
	{/if}
{/if}
