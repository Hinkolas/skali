<script lang="ts">
	import { setUser } from '$lib/stores/auth.svelte';
	import { modal } from '$lib/stores/modal.svelte';
	import Sidebar from '$lib/components/shell/Sidebar.svelte';
	import CommandPaletteModal, {
		modalOptions as commandPaletteOptions
	} from '$lib/components/shell/CommandPaletteModal.svelte';
	import type { LayoutData } from './$types';

	let { data, children }: { data: LayoutData; children: import('svelte').Snippet } = $props();

	$effect.pre(() => {
		setUser(data.user);
	});
</script>

<svelte:window
	onkeydown={(e) => {
		if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
			e.preventDefault();
			modal.open(CommandPaletteModal, {}, commandPaletteOptions);
		}
	}}
/>

<div class="bg-glow-app flex h-screen gap-4 p-3.5">
	<Sidebar />
	<main class="min-w-0 flex-1 overflow-y-auto px-5.5 pt-5.5">
		{@render children()}
	</main>
</div>
