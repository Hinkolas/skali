<script lang="ts">
	import Search from '@lucide/svelte/icons/search';
	import { modal } from '$lib/stores/modal.svelte';
	import CommandPaletteModal, {
		modalOptions as commandPaletteOptions
	} from './CommandPaletteModal.svelte';
</script>

<button
	type="button"
	onclick={() => modal.open(CommandPaletteModal, {}, commandPaletteOptions)}
	class="border-border-strong text-text-muted bg-white/2 flex h-8 w-36 cursor-pointer xl:w-44 items-center justify-between gap-2 rounded-[11px] border px-3.5 text-base transition-colors hover:bg-white/5"
>
	<span class="flex items-center gap-2">
		<Search size={14} class="text-text-ghost flex-none" />
		Search
	</span>
	<kbd
		class="font-mono border-border-strong flex items-center gap-[2px] rounded-[6px] border px-1.25 py-px text-xs"
	>
		<span>⌘</span><span>K</span>
	</kbd>
</button>
