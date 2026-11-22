<script lang="ts">
	// The store's volume size limit: the default or a figure in MB, with an
	// estimate of what the change does to the bucket headroom. Saves a PUT
	// and lets the page reload the store; the host remounts this card on
	// the new document.
	import { slide } from 'svelte/transition';
	import { cubicOut } from 'svelte/easing';
	import { refreshAll } from '$app/navigation';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import { api, ApiError } from '#lib/api/client.ts';
	import { toast } from '#lib/stores/toast.svelte.ts';
	import Button from '#lib/components/ui/Button.svelte';
	import Card from '#lib/components/ui/Card.svelte';
	import TextInput from '#lib/components/ui/TextInput.svelte';
	import { formatCount } from '#lib/format.ts';
	import {
		estimateCapacity,
		formatVolumeSize,
		parseVolumeSize,
		type ObjectStore
	} from '#lib/types/objectstorage.ts';

	let { store }: { store: ObjectStore } = $props();

	// Seeded once from the mount-time snapshot: the host keys this card on
	// the store's updated_at, so a saved change remounts it on fresh facts.
	// svelte-ignore state_referenced_locally
	const initialDefault = store.volume_size.default;
	// svelte-ignore state_referenced_locally
	const initialMb = store.volume_size.limit_mb;
	let useDefault = $state(initialDefault);
	let input = $state(String(initialMb));
	let saving = $state(false);

	const size = $derived(store.volume_size);
	const parsed = $derived(useDefault ? size.default_mb : parseVolumeSize(input));
	const invalid = $derived(
		!useDefault && (parsed === null || parsed < size.min_mb || parsed > size.max_mb)
	);
	const dirty = $derived(
		useDefault !== initialDefault || (!useDefault && parsed !== null && parsed !== initialMb)
	);
	const target = $derived(invalid ? null : parsed);
	// Common sizes with the buckets each would still fit, the current one
	// included, so the choice reads as a trade-off rather than a guess.
	const PRESETS = [128, 256, 512, 1000, 2000, 4000, 8000];
	const presets = $derived(
		[...new Set([...PRESETS, initialMb, size.default_mb])]
			.filter((mb) => mb >= size.min_mb && mb <= size.max_mb)
			.toSorted((a, b) => a - b)
			.map((mb) => ({ mb, estimate: estimateCapacity(store.capacity, initialMb, mb) }))
	);

	// A custom size off the table gets its own estimate line.
	const estimate = $derived(
		target === null || presets.some((preset) => preset.mb === target)
			? null
			: estimateCapacity(store.capacity, initialMb, target)
	);

	function pick(mb: number) {
		if (mb === size.default_mb) {
			useDefault = true;
		} else {
			useDefault = false;
			input = String(mb);
		}
	}

	const hint = $derived.by(() => {
		if (invalid) {
			return `Between ${formatVolumeSize(size.min_mb)} and ${formatVolumeSize(size.max_mb)}, in MB (or GB).`;
		}
		if (useDefault) return `The platform default, ${formatVolumeSize(size.default_mb)}.`;
		return `Volumes grow up to ${formatVolumeSize(target ?? initialMb)} before their bucket starts another.`;
	});

	const restartNote = $derived(
		!store.managed
			? 'Saving restarts the local store; buckets are briefly unavailable.'
			: store.masters < 2
				? 'Saving restarts the master; writes pause briefly.'
				: 'Saving restarts the masters one at a time.'
	);

	function reset() {
		useDefault = initialDefault;
		input = String(initialMb);
	}

	async function save() {
		if (!dirty || saving || invalid) return;
		saving = true;
		try {
			await api.put('/v1/system/object-storage/settings', {
				volume_size_limit_mb: useDefault ? null : target
			});
			toast.success(
				`Volume size set to ${formatVolumeSize(useDefault ? size.default_mb : (target ?? initialMb))}; ${store.managed ? 'the masters restart' : 'the store restarts'} to apply it`
			);
			await refreshAll();
		} catch (e) {
			toast.error(e instanceof ApiError ? e.message : 'Could not save the volume size');
		} finally {
			saving = false;
		}
	}
</script>

<Card class="flex flex-col p-5">
	<h3 class="text-text-primary mb-1 text-xl font-semibold">Volume size</h3>
	<p class="text-text-muted mb-3.5 text-base">
		Every bucket keeps volumes of its own, and each volume takes a slot. Servers offer their free
		disk divided by this size as slots, so a smaller size fits more buckets; a larger one suits a
		few big buckets.
	</p>
	<div class="flex flex-col gap-2.5 @2xl:max-w-lg">
		<div
			role="radiogroup"
			aria-label="Volume size limit"
			class="border-border-strong grid grid-cols-2 overflow-hidden rounded-lg border"
		>
			<button
				type="button"
				role="radio"
				aria-checked={useDefault}
				onclick={() => (useDefault = true)}
				class="py-1.75 font-mono text-sm transition-colors {useDefault
					? 'inset-ring inset-ring-accent/25 bg-accent/15 text-accent-nav'
					: 'text-text-ghost hover:bg-white/4 hover:text-text-tertiary'}"
			>
				default
			</button>
			<button
				type="button"
				role="radio"
				aria-checked={!useDefault}
				onclick={() => (useDefault = false)}
				class="border-border-subtle border-l py-1.75 font-mono text-sm transition-colors {!useDefault
					? 'inset-ring inset-ring-accent/25 bg-accent/15 text-accent-nav'
					: 'text-text-ghost hover:bg-white/4 hover:text-text-tertiary'}"
			>
				custom
			</button>
		</div>
		{#if !useDefault}
			<div transition:slide={{ duration: 180, easing: cubicOut }}>
				<div class="flex items-center gap-2">
					<div class="w-32">
						<TextInput
							bind:value={input}
							name="volume_size_limit_mb"
							mono
							{invalid}
							placeholder={String(size.default_mb)}
						/>
					</div>
					<span class="text-text-muted font-mono text-md">MB</span>
				</div>
			</div>
		{/if}
		<span class="text-text-muted text-md leading-relaxed">{hint}</span>
		{#if store.capacity}
			<div class="mt-1.5 flex flex-col">
				<div
					class="text-text-ghost mb-1 grid grid-cols-[5.5rem_1fr_1fr] text-xs font-semibold tracking-[0.12em] uppercase"
				>
					<span>size</span><span>more buckets</span><span>free slots</span>
				</div>
				{#each presets as preset (preset.mb)}
					{@const selected = (useDefault ? size.default_mb : parsed) === preset.mb}
					<button
						type="button"
						onclick={() => pick(preset.mb)}
						class="grid grid-cols-[5.5rem_1fr_1fr] rounded-md py-1 text-left font-mono text-md transition-colors {selected
							? 'bg-accent/10 text-accent-nav'
							: 'text-text-secondary hover:bg-white/4'}"
					>
						<span>
							{formatVolumeSize(preset.mb)}{#if preset.mb === initialMb}<span
									class="text-text-faint"
									title="the current size">*</span
								>{/if}
						</span>
						<span>
							{preset.estimate
								? `${preset.estimate.rough ? '~' : ''}${formatCount(preset.estimate.bucketHeadroom)}`
								: '–'}
						</span>
						<span>{preset.estimate ? formatCount(preset.estimate.freeSlots) : '–'}</span>
					</button>
				{/each}
				<span class="text-text-faint mt-1 text-xs">
					* current. Estimates from each server's free disk; existing buckets also take slots as
					they fill their volumes.
				</span>
			</div>
		{/if}
		{#if estimate && store.capacity}
			<span class="text-text-secondary font-mono text-xs">
				{estimate.rough ? 'roughly' : 'about'}
				{formatCount(estimate.bucketHeadroom)} more buckets after the change (now {formatCount(
					store.capacity.bucket_headroom
				)}), {formatCount(estimate.freeSlots)} free slots
			</span>
		{/if}
	</div>

	{#if dirty}
		<div
			class="border-border-subtle mt-4 flex flex-wrap items-center gap-x-4 gap-y-2 border-t pt-3.5"
			transition:slide={{ duration: 180, easing: cubicOut }}
		>
			<div class="text-status-warning flex min-w-0 flex-1 items-center gap-1.5">
				<TriangleAlert size={13} class="flex-none" />
				<span class="text-md">
					{restartNote} Volumes already over a lower size stay readable and stop taking writes; their
					buckets start new ones.
				</span>
			</div>
			<div class="ml-auto flex items-center gap-2">
				<Button variant="ghost" disabled={saving} onclick={reset}>Reset</Button>
				<Button variant="primary" busy={saving} disabled={invalid} onclick={save}>Save</Button>
			</div>
		</div>
	{/if}
</Card>
