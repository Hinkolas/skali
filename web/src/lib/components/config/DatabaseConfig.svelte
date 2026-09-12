<script lang="ts">
	import type { DatabaseView } from '$lib/models/service';
	import { formatBytes } from '$lib/format';
	import { describeSeconds } from '$lib/cron';
	import Pill from '$lib/components/ui/Pill.svelte';
	import Choice from './Choice.svelte';
	import ConfigCard from './ConfigCard.svelte';

	// The database claim: what engine, how isolated and how available it
	// runs, and what it asked for in storage, recovery, and extensions.
	let { service }: { service: DatabaseView } = $props();

	const c = $derived(service.config);
</script>

<div class="grid grid-cols-1 gap-3.5 @4xl:grid-cols-2 pb-6">
	<ConfigCard title="Engine" hint="major version pinned by skali.yaml">
		<div class="flex items-baseline gap-2">
			<span class="text-text-primary text-4xl font-semibold tracking-[-0.02em]">{c.engine}</span>
			<span class="text-text-muted text-xl">{c.version}</span>
		</div>
		<div class="mt-4">
			<div class="mb-1.5 flex items-baseline">
				<span class="text-text-tertiary text-md font-medium">Extensions</span>
				<span class="text-text-faint ml-auto font-mono text-xs">
					{c.extensions?.length ? 'enabled on the database' : 'none requested'}
				</span>
			</div>
			<div class="flex flex-wrap gap-1.5">
				{#each c.extensions ?? [] as extension (extension)}
					<Pill text={extension} />
				{/each}
			</div>
		</div>
	</ConfigCard>

	<ConfigCard title="Storage and recovery" hint="what the claim reserves">
		<div class="flex flex-col">
			<div class="border-border-subtle flex items-baseline gap-3 border-b py-2">
				<span class="text-text-faint text-md">Storage requested</span>
				<span class="text-text-primary ml-auto font-mono text-md">
					{c.storageBytes ? formatBytes(c.storageBytes) : 'platform default'}
				</span>
			</div>
			<div class="flex items-baseline gap-3 py-2">
				<span class="text-text-faint text-md">Point-in-time recovery</span>
				<span class="text-text-primary ml-auto font-mono text-md">
					{c.pointInTimeRecoverySeconds
						? `${describeSeconds(c.pointInTimeRecoverySeconds)} window`
						: 'off · snapshots only'}
				</span>
			</div>
		</div>
	</ConfigCard>

	<ConfigCard title="Isolation" hint="where the database lives">
		<Choice
			label="Isolation"
			value={c.isolation}
			options={[
				{ value: 'shared', description: "a database in the platform's shared pool" },
				{ value: 'project', description: 'one cluster shared by this project' },
				{ value: 'dedicated', description: 'a cluster of its own' }
			]}
		/>
	</ConfigCard>

	<ConfigCard title="Availability" hint="how writes are replicated">
		<Choice
			label="Availability"
			value={c.availability}
			options={[
				{ value: 'single', description: 'one instance, no replica' },
				{ value: 'asynchronous', description: 'a replica that trails the primary' },
				{ value: 'synchronous', description: 'commits wait for the replica' }
			]}
		/>
	</ConfigCard>
</div>
