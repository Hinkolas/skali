<script lang="ts">
	import Copy from '@lucide/svelte/icons/copy';
	import Check from '@lucide/svelte/icons/check';
	import { shortId } from '$lib/format';
	import { toast } from '$lib/stores/toast.svelte';

	// An inline id chip: the short form on screen, the full id on the
	// clipboard (the CLI wants the whole UUID). `hint` is the toast's
	// description, e.g. the command the id is for.
	let {
		id,
		label = 'Run id',
		hint,
		class: className = ''
	}: {
		id: string;
		label?: string;
		hint?: string;
		class?: string;
	} = $props();

	let copied = $state(false);

	async function copy() {
		try {
			await navigator.clipboard.writeText(id);
			copied = true;
			toast.success(`${label} copied`, { description: hint ?? id });
			setTimeout(() => (copied = false), 1600);
		} catch {
			toast.error('Could not copy', { description: 'Clipboard access was denied' });
		}
	}
</script>

<button
	type="button"
	onclick={copy}
	title="{label} {id}: click to copy"
	aria-label="Copy {label} {id}"
	class="font-mono text-text-faint hover:text-text-primary inline-flex flex-none cursor-pointer items-center gap-1 transition-colors {className}"
>
	{shortId(id)}
	{#if copied}
		<Check class="text-status-success size-3" />
	{:else}
		<Copy class="size-3" />
	{/if}
</button>
