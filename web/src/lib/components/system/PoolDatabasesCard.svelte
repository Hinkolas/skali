<script lang="ts">
	import { resolve } from '$app/paths';
	import { formatBytes } from '$lib/format';
	import type { DatabasePoolDetail } from '$lib/types/pools';
	import Button from '$lib/components/ui/Button.svelte';
	import Card from '$lib/components/ui/Card.svelte';

	// Who is on this pool, at a glance: the count by owner and the largest
	// databases; the Databases tab has every row with links.
	let { pool }: { pool: DatabasePoolDetail } = $props();

	const TOP = 5;
	const service = $derived(pool.databases.filter((d) => d.owner === 'service').length);
	const system = $derived(pool.databases.length - service);
	const headline = $derived.by(() => {
		const n = pool.databases.length;
		if (n === 0) return 'no databases yet';
		const parts = [`${n} database${n === 1 ? '' : 's'}`];
		if (system > 0 && service > 0) parts.push(`${service} from projects`, `${system} system`);
		else if (system > 0) parts.push('all system');
		return parts.join(' · ');
	});
	// Measured sizes first, largest on top; unmeasured ones after by name.
	const top = $derived(
		pool.databases
			.toSorted(
				(a, b) =>
					(b.used_bytes ?? -1) - (a.used_bytes ?? -1) ||
					a.database_name.localeCompare(b.database_name)
			)
			.slice(0, TOP)
	);
	function ownerText(database: DatabasePoolDetail['databases'][number]): string {
		if (database.owner === 'system') return `system · ${database.system_key ?? ''}`;
		const project = database.project?.display_name || database.project?.name || 'unknown project';
		return database.environment ? `${project} · ${database.environment.name}` : project;
	}
</script>

<Card class="flex flex-col p-5">
	<div class="mb-3.5 flex flex-wrap items-center gap-x-3 gap-y-1.5">
		<h3 class="text-text-primary text-xl font-semibold">Databases</h3>
		<span class="text-text-muted text-md">{headline}</span>
		{#if pool.databases.length > 0}
			<div class="ml-auto">
				<Button
					size="sm"
					variant="ghost"
					href={resolve('/(app)/system/databases/[pool]/databases', { pool: pool.name })}
				>
					All databases
				</Button>
			</div>
		{/if}
	</div>
	{#if top.length > 0}
		<div class="flex flex-col">
			{#each top as database (database.database_name)}
				<div class="border-border-subtle flex items-center gap-3 border-b py-2.5 last:border-0">
					<div class="flex min-w-0 flex-1 flex-col gap-0.5">
						<span class="text-text-primary truncate font-mono text-md"
							>{database.database_name}</span
						>
						<span class="text-text-faint truncate text-xs">{ownerText(database)}</span>
					</div>
					<span class="text-text-secondary flex-none font-mono text-md">
						{database.used_bytes == null ? 'not measured' : formatBytes(database.used_bytes)}
					</span>
				</div>
			{/each}
		</div>
		{#if pool.databases.length > TOP}
			<div class="font-mono text-text-faint mt-2 text-xs">
				the {TOP} largest of {pool.databases.length}
			</div>
		{/if}
	{:else}
		<div class="font-mono text-text-faint py-2 text-md">
			the first declared database on this pool lands here
		</div>
	{/if}
</Card>
