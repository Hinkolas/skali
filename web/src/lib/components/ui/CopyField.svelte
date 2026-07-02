<script lang="ts">
	import Copy from '@lucide/svelte/icons/copy';
	import Check from '@lucide/svelte/icons/check';
	import { toast } from '$lib/stores/toast.svelte';

	// Labeled mono value with a copy button. `masked` hides the display value
	// but still copies the real one.
	let {
		label,
		value,
		masked = false
	}: {
		label: string;
		value: string;
		masked?: boolean;
	} = $props();

	let copied = $state(false);

	async function copy() {
		try {
			await navigator.clipboard.writeText(value);
			copied = true;
			toast.success('Copied to clipboard', { description: label });
			setTimeout(() => (copied = false), 1600);
		} catch {
			toast.error('Could not copy', { description: 'Clipboard access was denied' });
		}
	}
</script>

<div class="flex items-center gap-3">
	<div class="text-text-faint w-19 flex-none text-[12px]">{label}</div>
	<div
		class="font-mono bg-surface-input border-border-default min-w-0 flex-1 overflow-hidden rounded-lg border px-2.75 py-2 text-[11.5px] text-ellipsis whitespace-nowrap {masked
			? 'text-text-faint tracking-[0.15em]'
			: 'text-text-secondary'}"
	>
		{masked ? '••••••••••••••••' : value}
	</div>
	<button
		type="button"
		onclick={copy}
		class="text-text-faint hover:text-text-primary flex-none cursor-pointer rounded-md p-1 transition-colors"
		aria-label="Copy {label}"
	>
		{#if copied}
			<Check class="text-status-success size-3.5" />
		{:else}
			<Copy class="size-3.5" />
		{/if}
	</button>
</div>
