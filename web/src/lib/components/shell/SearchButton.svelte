<script lang="ts">
	import { modal } from '$lib/stores/modal.svelte';
	import CommandPaletteModal, {
		modalOptions as commandPaletteOptions
	} from './CommandPaletteModal.svelte';
</script>

<button
	type="button"
	onclick={() => modal.open(CommandPaletteModal, {}, commandPaletteOptions)}
	class="border-border-strong text-text-muted bg-white/2 flex h-8 cursor-pointer items-center gap-2 rounded-[10px] border px-3.5 text-[12.5px] transition-colors hover:bg-white/5"
>
	Search
	<kbd class="font-mono border-border-strong rounded-[5px] border px-1.25 py-px text-[10px]">
		⌘K
	</kbd>
</button>
