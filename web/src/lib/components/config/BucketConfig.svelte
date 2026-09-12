<script lang="ts">
	import type { BucketView } from '$lib/models/service';
	import { formatBytes, formatCount } from '$lib/format';
	import { describeSeconds } from '$lib/cron';
	import Choice from './Choice.svelte';
	import ConfigCard from './ConfigCard.svelte';

	// The bucket claim: who may read it, whether versions are kept, the
	// quotas that cap it, and the lifecycle rules that clean up behind
	// clients.
	let { service }: { service: BucketView } = $props();

	const c = $derived(service.config);
	const versioned = $derived(c.versioning === 'enabled');

	const quotas = $derived([
		{
			label: 'Storage',
			value: c.storageQuotaBytes ? formatBytes(c.storageQuotaBytes) : 'unlimited',
			set: !!c.storageQuotaBytes
		},
		{
			label: 'Objects',
			value: c.objectQuota ? formatCount(c.objectQuota) : 'unlimited',
			set: !!c.objectQuota
		},
		{
			label: 'Max object size',
			value: c.maxObjectSizeBytes ? formatBytes(c.maxObjectSizeBytes) : 'no limit',
			set: !!c.maxObjectSizeBytes
		}
	]);
</script>

<div class="grid grid-cols-2 gap-3.5 pb-6">
	<ConfigCard title="Visibility" hint="who may read objects">
		<Choice
			label="Visibility"
			value={c.visibility}
			options={[
				{ value: 'private', description: 'every request needs the keypair' },
				{ value: 'public-read', description: 'anyone may read, writes need the keypair' }
			]}
		/>
	</ConfigCard>

	<ConfigCard title="Versioning" hint="what happens to overwritten objects">
		<Choice
			label="Versioning"
			value={c.versioning}
			options={[
				{ value: 'enabled', description: 'old versions are kept and restorable' },
				{ value: 'disabled', description: 'an overwrite replaces the object' }
			]}
		/>
		<div class="mt-3 flex items-baseline gap-3">
			<span class="text-text-faint text-md">Expire old versions</span>
			<span class="text-text-primary ml-auto font-mono text-md">
				{#if !versioned}
					n/a
				{:else if c.expireNoncurrentVersionsAfterSeconds}
					after {describeSeconds(c.expireNoncurrentVersionsAfterSeconds)}
				{:else}
					kept forever
				{/if}
			</span>
		</div>
	</ConfigCard>

	<ConfigCard title="Quotas" hint="caps enforced by the object store">
		<div class="grid grid-cols-3 gap-3">
			{#each quotas as quota (quota.label)}
				<div
					class="flex flex-col gap-1 rounded-[11px] border px-3.5 py-3 {quota.set
						? 'border-border-default'
						: 'border-border-default border-dashed'}"
				>
					<span class="text-text-ghost text-xs font-semibold tracking-[0.12em] uppercase">
						{quota.label}
					</span>
					<span class="font-mono text-lg {quota.set ? 'text-text-primary' : 'text-text-faint'}">
						{quota.value}
					</span>
				</div>
			{/each}
		</div>
	</ConfigCard>

	<ConfigCard title="Lifecycle" hint="automatic cleanup">
		<div class="flex items-baseline gap-3 py-1">
			<span class="text-text-faint text-md">Abort incomplete uploads</span>
			<span class="text-text-primary ml-auto font-mono text-md">
				{c.abortIncompleteUploadsAfterSeconds
					? `after ${describeSeconds(c.abortIncompleteUploadsAfterSeconds)}`
					: 'never'}
			</span>
		</div>
	</ConfigCard>
</div>
