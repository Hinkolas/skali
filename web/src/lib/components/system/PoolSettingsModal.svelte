<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'Database pool settings',
		size: 'lg'
	} satisfies ModalOptions;
</script>

<script lang="ts">
	// Modal editor for one pool's tuning: the memory budget (automatic or an
	// explicit GiB figure) and the per-parameter overrides. The table shows
	// what the pool effectively runs; an override replaces one derived value.
	// Saves a PUT and closes; the sudo reauth prompt layers above this modal
	// on the stack.
	import { slide } from 'svelte/transition';
	import { cubicOut } from 'svelte/easing';
	import { invalidateAll } from '$app/navigation';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import X from '@lucide/svelte/icons/x';
	import { api, ApiError } from '$lib/api/client';
	import { toast } from '$lib/stores/toast.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import ModalHeader from '$lib/components/ui/ModalHeader.svelte';
	import TextInput from '$lib/components/ui/TextInput.svelte';
	import { formatBytes } from '$lib/format';
	import {
		formatGiB,
		MIN_POOL_MEMORY_BYTES,
		overridesEqual,
		parseGiB,
		restartRequired,
		type DatabasePool
	} from '$lib/types/pools';

	let { pool, close }: { pool: DatabasePool; close: (saved?: boolean) => void } = $props();

	// Seeded once from the open-time snapshot: the modal closes on save, so it
	// never has to track a reload underneath it.
	// svelte-ignore state_referenced_locally
	const initialAuto = pool.memory.auto;
	// svelte-ignore state_referenced_locally
	const initialBytes = pool.memory.bytes;
	let auto = $state(initialAuto);
	let memoryInput = $state(initialBytes === null ? '' : formatGiB(initialBytes));
	// Every allowed key is present from the start (empty = no override) so
	// the row inputs always bind to a defined value.
	// svelte-ignore state_referenced_locally
	let overrides = $state<Record<string, string>>(
		Object.fromEntries(
			pool.parameters.allowed.map((key) => [key, pool.parameters.overrides[key] ?? ''])
		)
	);
	let saving = $state(false);
	let error = $state('');

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

	// Every allowed key gets a row; keys the pool does not derive show the
	// PostgreSQL default as their effective value.
	const rows = $derived(
		[...pool.parameters.allowed].sort().map((key) => ({
			key,
			effective: pool.parameters.effective[key] ?? '',
			restart: pool.parameters.restart_keys.includes(key)
		}))
	);

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

	async function save() {
		if (!dirty || saving || memoryInvalid) return;
		const body: { memory_bytes?: number | null; parameters?: Record<string, string> } = {};
		if (memoryChanged) body.memory_bytes = auto ? null : memoryBytes;
		if (overridesChanged) body.parameters = cleanOverrides;
		saving = true;
		error = '';
		try {
			await api.put(`/v1/system/database-pools/${encodeURIComponent(pool.name)}/settings`, body);
			toast.success(
				restarts.length > 0
					? `Pool ${pool.name} retuned; its instances restart for ${restarts.join(', ')}`
					: `Pool ${pool.name} retuned`
			);
			await invalidateAll();
			close(true);
		} catch (e) {
			error = e instanceof ApiError ? e.message : 'Could not save the pool settings';
		} finally {
			saving = false;
		}
	}
</script>

<ModalHeader title={pool.name} mono>Database pool settings · {pool.class}</ModalHeader>

<div class="divide-border-subtle flex flex-col divide-y overflow-y-auto">
	<!-- Budget: automatic follows the node; custom is a GiB figure. -->
	<div class="flex flex-col gap-2.5 px-5.5 py-4">
		<span class="text-text-primary text-base font-medium">Memory budget</span>
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

	<!-- Parameters: the effective value per key, an override input beside it. -->
	<div class="flex flex-col gap-2.5 px-5.5 py-4">
		<div class="flex items-baseline justify-between">
			<span class="text-text-primary text-base font-medium">Parameters</span>
			<span class="text-text-muted text-md">effective value · override</span>
		</div>
		<!-- One grid for every row so the columns line up whatever the key
		     length; the longest key sets the first column. -->
		<div class="grid grid-cols-[auto_minmax(0,1fr)_8.5rem_auto] items-center gap-x-3 gap-y-1.5">
			{#each rows as row (row.key)}
				<span class="text-text-secondary font-mono text-md">
					{row.key}{#if row.restart}<span
							class="text-text-faint"
							title="changing this restarts the instances">*</span
						>{/if}
				</span>
				<span class="text-text-muted truncate font-mono text-md" title={row.effective}>
					{#if row.effective}
						{row.effective}
					{:else if budgetUnknown}
						<span class="text-text-faint">unknown</span>
					{:else}
						<span class="text-text-faint">default</span>
					{/if}
				</span>
				<TextInput
					bind:value={overrides[row.key]}
					name={row.key}
					size="sm"
					mono
					placeholder={row.effective ? 'derived' : 'default'}
				/>
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
		<span class="text-text-muted text-md leading-relaxed">
			Memory values in PostgreSQL syntax (64MB, 2GB). Keys marked * restart the instances when
			changed; the rest reload live.
		</span>
		{#if restarts.length > 0}
			<div transition:slide={{ duration: 180, easing: cubicOut }}>
				<div class="text-status-warning flex items-center gap-1.5">
					<TriangleAlert size={13} class="flex-none" />
					<span class="text-md">
						Saving restarts the pool's instances for {restarts.join(', ')}; a single-instance pool
						is briefly unavailable.
					</span>
				</div>
			</div>
		{/if}
		{#if error}
			<span class="text-status-danger text-md">{error}</span>
		{/if}
	</div>
</div>

<div class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3">
	<Button variant="ghost" onclick={() => close(false)}>Cancel</Button>
	<Button variant="primary" busy={saving} disabled={!dirty || memoryInvalid} onclick={save}>
		Save
	</Button>
</div>
