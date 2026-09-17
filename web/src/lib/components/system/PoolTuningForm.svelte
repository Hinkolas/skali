<script lang="ts">
	// The tuning form of one pool: the memory budget (automatic or an
	// explicit GiB figure) and the per-parameter overrides, grouped by what
	// they govern. Each row shows what the pool effectively runs; an
	// override replaces one derived value. Saves a PUT and lets the layout
	// reload the pool; the host remounts this form on the new document.
	import { slide } from 'svelte/transition';
	import { cubicOut } from 'svelte/easing';
	import { invalidateAll } from '$app/navigation';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import X from '@lucide/svelte/icons/x';
	import { api, ApiError } from '$lib/api/client';
	import { toast } from '$lib/stores/toast.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Card from '$lib/components/ui/Card.svelte';
	import TextInput from '$lib/components/ui/TextInput.svelte';
	import { formatBytes } from '$lib/format';
	import {
		formatGiB,
		groupedParameters,
		MIN_POOL_MEMORY_BYTES,
		overridesEqual,
		parseGiB,
		restartRequired,
		UNIT_HINT,
		type DatabasePool
	} from '$lib/types/pools';

	let { pool }: { pool: DatabasePool } = $props();

	// Seeded once from the mount-time snapshot: the host keys this form on
	// the pool's updated_at, so a saved change remounts it on fresh facts
	// and an unrelated reload leaves edits in progress alone.
	// svelte-ignore state_referenced_locally
	const initialAuto = pool.memory.auto;
	// svelte-ignore state_referenced_locally
	const initialBytes = pool.memory.bytes;
	let auto = $state(initialAuto);
	let memoryInput = $state(initialBytes === null ? '' : formatGiB(initialBytes));
	// Every allowed key is present from the start (empty = no override) so
	// the row inputs always bind to a defined value.
	let overrides = $state<Record<string, string>>(seedOverrides());
	let saving = $state(false);

	function seedOverrides(): Record<string, string> {
		return Object.fromEntries(
			pool.parameters.allowed.map((key) => [key, pool.parameters.overrides[key] ?? ''])
		);
	}

	// svelte-ignore state_referenced_locally
	const budgetUnknown = pool.memory.bytes === null;
	const memoryBytes = $derived(auto ? null : parseGiB(memoryInput));
	const memoryInvalid = $derived(
		!auto && (memoryBytes === null || memoryBytes < MIN_POOL_MEMORY_BYTES)
	);
	const memoryChanged = $derived(auto !== initialAuto || (!auto && memoryBytes !== initialBytes));
	const cleanOverrides = $derived.by(() => {
		const out: Record<string, string> = {};
		for (const [key, value] of Object.entries(overrides)) {
			if (value.trim() !== '') out[key] = value.trim();
		}
		return out;
	});
	const overridesChanged = $derived(!overridesEqual(cleanOverrides, pool.parameters.overrides));
	const dirty = $derived(memoryChanged || overridesChanged);
	const restarts = $derived(restartRequired(pool, memoryChanged, cleanOverrides));
	const changes = $derived.by(() => {
		let n = memoryChanged ? 1 : 0;
		const keys = new Set([
			...Object.keys(cleanOverrides),
			...Object.keys(pool.parameters.overrides)
		]);
		for (const key of keys) if (cleanOverrides[key] !== pool.parameters.overrides[key]) n++;
		return n;
	});

	const groups = $derived(groupedParameters(pool));

	const budgetHint = $derived.by(() => {
		if (auto) {
			if (pool.memory.bytes === null) return 'Sized once a database node reports its memory.';
			const node = pool.memory.node ? ` from node ${pool.memory.node}` : '';
			const capped = pool.memory.capped ? ', capped so every pool fits the node' : '';
			return `${formatBytes(pool.memory.bytes)}${node}${capped}.`;
		}
		if (memoryInvalid) return `At least ${formatBytes(MIN_POOL_MEMORY_BYTES)}, in GiB.`;
		return `shared_buffers becomes a quarter of ${formatBytes(memoryBytes ?? 0)}; the scheduler reserves the whole budget on the node.`;
	});

	function reset() {
		auto = initialAuto;
		memoryInput = initialBytes === null ? '' : formatGiB(initialBytes);
		overrides = seedOverrides();
	}

	async function save() {
		if (!dirty || saving || memoryInvalid) return;
		const body: { memory_bytes?: number | null; parameters?: Record<string, string> } = {};
		if (memoryChanged) body.memory_bytes = auto ? null : memoryBytes;
		if (overridesChanged) body.parameters = cleanOverrides;
		saving = true;
		try {
			await api.put(`/v1/system/database-pools/${encodeURIComponent(pool.name)}/settings`, body);
			toast.success(
				restarts.length > 0
					? `Pool ${pool.name} retuned; its instances restart for ${restarts.join(', ')}`
					: `Pool ${pool.name} retuned`
			);
			await invalidateAll();
		} catch (e) {
			toast.error(e instanceof ApiError ? e.message : 'Could not save the pool settings');
		} finally {
			saving = false;
		}
	}
</script>

<div class="grid grid-cols-1 gap-3.5">
	<!-- Budget: automatic follows the node; custom is a GiB figure. -->
	<Card class="p-5">
		<h3 class="text-text-primary mb-1 text-xl font-semibold">Memory budget</h3>
		<p class="text-text-muted mb-3.5 text-base">
			Every derived parameter follows this one number; the instances request it from the scheduler.
		</p>
		<div class="flex flex-col gap-2.5 @2xl:max-w-lg">
			<div
				role="radiogroup"
				aria-label="Memory budget of {pool.name}"
				class="border-border-strong grid grid-cols-2 overflow-hidden rounded-lg border"
			>
				<button
					type="button"
					role="radio"
					aria-checked={auto}
					onclick={() => (auto = true)}
					class="py-1.75 font-mono text-sm transition-colors {auto
						? 'inset-ring inset-ring-accent/25 bg-accent/15 text-accent-nav'
						: 'text-text-ghost hover:bg-white/4 hover:text-text-tertiary'}"
				>
					automatic
				</button>
				<button
					type="button"
					role="radio"
					aria-checked={!auto}
					onclick={() => (auto = false)}
					class="border-border-subtle border-l py-1.75 font-mono text-sm transition-colors {!auto
						? 'inset-ring inset-ring-accent/25 bg-accent/15 text-accent-nav'
						: 'text-text-ghost hover:bg-white/4 hover:text-text-tertiary'}"
				>
					custom
				</button>
			</div>
			{#if !auto}
				<div transition:slide={{ duration: 180, easing: cubicOut }}>
					<div class="flex items-center gap-2">
						<div class="w-32">
							<TextInput bind:value={memoryInput} mono invalid={memoryInvalid} placeholder="4" />
						</div>
						<span class="text-text-muted font-mono text-md">GiB</span>
					</div>
				</div>
			{/if}
			<span class="text-text-muted text-md leading-relaxed">{budgetHint}</span>
		</div>
	</Card>

	<!-- Parameters: the effective value per key, an override input beside it. -->
	<Card class="p-5">
		<div class="mb-1 flex items-baseline justify-between gap-3">
			<h3 class="text-text-primary text-xl font-semibold">Parameters</h3>
			<span class="text-text-muted text-md">effective value · override</span>
		</div>
		<p class="text-text-muted mb-2 text-base">
			Memory values in PostgreSQL syntax (64MB, 2GB). Keys marked * restart the instances when
			changed; the rest reload live.
		</p>
		{#each groups as group (group.id)}
			<div class="text-text-tertiary mt-4 mb-1.5 text-xs font-semibold tracking-[0.12em] uppercase">
				{group.label}
			</div>
			<!-- The key and its blurb take the slack; the effective value sits
			     right-aligned beside the override so the two read as a pair in
			     every group whatever the key lengths. -->
			<div class="grid grid-cols-[minmax(0,1fr)_auto_8.5rem_auto] items-center gap-x-3 gap-y-2">
				{#each group.rows as row (row.key)}
					<div class="flex min-w-0 flex-col py-0.5">
						<span class="text-text-secondary font-mono text-md">
							{row.key}{#if row.restart}<span
									class="text-text-faint"
									title="changing this restarts the instances">*</span
								>{/if}
						</span>
						{#if row.blurb}
							<span class="text-text-faint text-xs leading-snug">{row.blurb}</span>
						{/if}
					</div>
					<span
						class="text-text-muted max-w-40 truncate text-right font-mono text-md"
						title={row.effective}
					>
						{#if row.effective}
							{row.effective}
						{:else if budgetUnknown}
							<span class="text-text-faint">unknown</span>
						{:else}
							<span class="text-text-faint">default</span>
						{/if}
					</span>
					<span title={UNIT_HINT[row.unit]}>
						<TextInput
							bind:value={overrides[row.key]}
							name={row.key}
							size="sm"
							mono
							placeholder={row.effective ? 'derived' : 'default'}
						/>
					</span>
					<Button
						size="icon"
						variant="ghost"
						ariaLabel="Clear override for {row.key}"
						title="Clear override"
						disabled={!(overrides[row.key] ?? '').trim()}
						onclick={() => (overrides[row.key] = '')}
					>
						<X size={13} />
					</Button>
				{/each}
			</div>
		{/each}
	</Card>
</div>

<!-- Sticky so Save stays in reach while scrolling sixteen rows; the
     backdrop keeps the rows legible underneath. -->
<div
	class="border-border-subtle bg-surface-raised/90 sticky bottom-0 -mx-4 mt-3.5 flex flex-wrap items-center gap-x-4 gap-y-2 border-t px-4 py-3 backdrop-blur"
>
	{#if restarts.length > 0}
		<div class="text-status-warning flex min-w-0 flex-1 items-center gap-1.5">
			<TriangleAlert size={13} class="flex-none" />
			<span class="text-md">
				Saving restarts the pool's instances for {restarts.join(', ')}; a single-instance pool is
				briefly unavailable.
			</span>
		</div>
	{:else}
		<span class="text-text-muted flex-1 text-md">
			{changes === 0 ? 'no changes' : changes === 1 ? '1 change' : `${changes} changes`}
		</span>
	{/if}
	<div class="ml-auto flex items-center gap-2">
		<Button variant="ghost" disabled={!dirty || saving} onclick={reset}>Reset</Button>
		<Button variant="primary" busy={saving} disabled={!dirty || memoryInvalid} onclick={save}>
			Save
		</Button>
	</div>
</div>
