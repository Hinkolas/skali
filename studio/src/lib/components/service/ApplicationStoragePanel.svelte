<script lang="ts">
	import type { ServiceStorage } from '$lib/types/metrics';
	import { STORAGE_KIND_META } from '$lib/service-types';
	import { formatBytes } from '$lib/format';
	import Card from '$lib/components/ui/Card.svelte';
	import ProgressBar from '$lib/components/ui/ProgressBar.svelte';

	// The application's own storage footprints: the persistent volume it
	// mounts and the ephemeral (temporary) space its pods write, each against
	// its declared size. Rows use the project storage bar's kind colors so
	// the two views read as one.
	let {
		storage = null,
		temporaryStorage = null
	}: { storage?: ServiceStorage | null; temporaryStorage?: ServiceStorage | null } = $props();

	const rows = $derived(
		[
			storage ? { label: 'Volume', entry: storage, limitWord: 'used' } : null,
			temporaryStorage ? { label: 'Temporary', entry: temporaryStorage, limitWord: 'limit' } : null
		].filter((r) => r !== null)
	);
</script>

{#if rows.length > 0}
	<Card class="px-4.5 pt-1 pb-1.5">
		{#each rows as row (row.entry.kind)}
			{@const meta = STORAGE_KIND_META[row.entry.kind]}
			{@const used = row.entry.used_bytes}
			{@const capacity = row.entry.capacity_bytes}
			<div
				class="border-border-subtle grid grid-cols-[1.2fr_1fr_1.8fr] items-center gap-3 border-b py-2.5 last:border-0"
			>
				<div class="flex items-center gap-2">
					<span class="size-[8px] flex-none rounded-full {meta.class}"></span>
					<span class="text-text-muted text-md">{row.label} storage</span>
				</div>
				<div class="text-text-primary font-mono text-sm">
					{#if used != null}
						{formatBytes(used)}
					{:else}
						reserved {formatBytes(capacity)}
					{/if}
				</div>
				<div class="flex items-center gap-2.5">
					{#if used != null && capacity > 0}
						<div class="min-w-0 flex-1">
							<ProgressBar pct={Math.min(100, (used / capacity) * 100)} class={meta.class} />
						</div>
						<span class="text-text-faint flex-none font-mono text-xs">
							of {formatBytes(capacity)}
							{row.limitWord}
						</span>
					{/if}
				</div>
			</div>
		{/each}
	</Card>
{/if}
