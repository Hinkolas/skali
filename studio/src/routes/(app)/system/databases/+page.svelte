<script lang="ts">
	import { resolve } from '$app/paths';
	import ChevronRight from '@lucide/svelte/icons/chevron-right';
	import Database from '@lucide/svelte/icons/database';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
	import Pill from '$lib/components/ui/Pill.svelte';
	import Table from '$lib/components/ui/Table.svelte';
	import { formatBytes } from '$lib/format';
	import { poolPhase, type DatabasePool } from '$lib/types/pools';
	import type { PageData } from './$types';

	// Every managed pool with its budget; each row opens the pool's own
	// pages (overview, tuning, databases).
	let { data }: { data: PageData } = $props();

	const grid = 'grid-cols-[1.3fr_0.8fr_1.2fr_0.9fr_0.9fr_1.3fr_1.5fr_auto]';

	function memoryText(pool: DatabasePool): string {
		return pool.memory.bytes === null ? 'unknown' : formatBytes(pool.memory.bytes);
	}
	function poolHref(pool: DatabasePool): string {
		return resolve('/(app)/system/databases/[pool]', { pool: pool.name });
	}
</script>

<svelte:head>
	<title>Databases — skali</title>
</svelte:head>

<PageHeader title="Databases">
	{#snippet subtitle()}
		{data.pools.length === 1 ? '1 pool' : `${data.pools.length} pools`}
	{/snippet}
</PageHeader>

<div class="pb-6">
	{#if data.pools.length > 0}
		<Table
			columns={['Pool', 'Class', 'Engine', 'Instances', 'Storage', 'Memory', 'Status', '']}
			{grid}
		>
			{#each data.pools as pool (pool.name)}
				{@const phase = poolPhase(pool)}
				<div
					class="border-border-subtle flex flex-wrap items-center gap-x-4 gap-y-1.5 border-b px-4.5 py-3 transition-colors last:border-b-0 hover:bg-white/2 @2xl:grid @2xl:gap-x-0 {grid}"
				>
					<a
						href={resolve('/(app)/system/databases/[pool]', { pool: pool.name })}
						class="text-text-primary font-mono text-md hover:underline"
					>
						{pool.name}
					</a>
					<span class="text-text-secondary text-md">{pool.class}</span>
					<span class="text-text-secondary font-mono text-md">{pool.engine} {pool.major}</span>
					<span class="text-text-secondary font-mono text-md">{pool.instances}</span>
					<span class="text-text-secondary font-mono text-md"
						>{formatBytes(pool.storage_bytes)}</span
					>
					<span class="flex items-center gap-2">
						<span class="text-text-secondary font-mono text-md">{memoryText(pool)}</span>
						{#if pool.memory.bytes !== null && pool.memory.auto}
							<Pill text={pool.memory.capped ? 'auto · capped' : 'auto'} />
						{/if}
					</span>
					<span class="@max-2xl:ml-auto"><Pill text={phase.text} tone={phase.tone} /></span>
					<span class="flex justify-end">
						<Button size="sm" variant="ghost" href={poolHref(pool)} title="Open this pool">
							Manage
							<ChevronRight size={14} />
						</Button>
					</span>
				</div>
			{/each}
		</Table>
		<p class="text-text-muted mt-3 px-4.5 text-md">
			Every pool runs a PostgreSQL parameter set derived from its memory budget. Automatic budgets
			follow the smallest database node; explicit ones and per-parameter overrides are yours to set.
		</p>
	{:else}
		<EmptyState
			icon={Database}
			title="No database pools"
			description="the first declared database creates the shared pool"
		/>
	{/if}
</div>
