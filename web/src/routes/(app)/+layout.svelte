<script lang="ts">
	import { setUser } from '$lib/stores/auth.svelte';
	import { modal } from '$lib/stores/modal.svelte';
	import Sidebar from '$lib/components/shell/Sidebar.svelte';
	import Topbar from '$lib/components/shell/Topbar.svelte';
	import SidePanel from '$lib/components/ui/SidePanel.svelte';
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

<!-- No top padding: the 56px logo/topbar band provides the breathing room,
     so its content centers between the window edge and the card. -->
<div class="bg-glow-app flex h-screen gap-2.5 px-2.5 pb-2.5">
	<Sidebar />
	<div class="flex min-w-0 flex-1 flex-col">
		<Topbar />
		<div class="flex min-h-0 flex-1 gap-4">
			<main
				class="bg-surface-raised border-border-default min-w-0 flex-1 overflow-y-auto rounded-2xl border px-5.5 pt-5.5"
			>
				{@render children()}
			</main>
			<!-- Right-hand detail panel (store-driven); a flex sibling so <main>
			     cedes space instead of being overlaid. -->
			<SidePanel />
		</div>
	</div>
</div>
