<script lang="ts">
	import type { Snippet } from 'svelte';
	import ChevronsUpDown from '@lucide/svelte/icons/chevrons-up-down';
	import { toast } from '$lib/stores/toast.svelte';

	// Context switcher card at the top of the sidebar (org / project / service).
	// Switching is a visual stub in the prototype.
	let {
		title,
		subtitle,
		leading
	}: {
		title: string;
		subtitle: string;
		leading: Snippet;
	} = $props();
</script>

<button
	type="button"
	onclick={() => toast.info('Switching is coming soon')}
	class="bg-surface-overlay border-border-raised flex w-full cursor-pointer items-center gap-2.5 rounded-xl border px-3 py-2.5 text-left transition-colors hover:border-white/16"
>
	{@render leading()}
	<span class="flex min-w-0 flex-col gap-px">
		<span class="text-text-primary truncate text-[13.5px] font-semibold">{title}</span>
		<span class="font-mono text-text-faint truncate text-[10px]">{subtitle}</span>
	</span>
	<ChevronsUpDown size={13} class="text-text-ghost ml-auto flex-none" />
</button>
