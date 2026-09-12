<script lang="ts">
	import { afterNavigate } from '$app/navigation';
	import MenuIcon from '@lucide/svelte/icons/menu';
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
	let navigationOpen = $state(false);
	afterNavigate(() => {
		navigationOpen = false;
	});

	$effect.pre(() => {
		setUser(data.user);
	});
</script>

<svelte:window
	onkeydown={(e) => {
		if (e.key === 'Escape') navigationOpen = false;
		if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
			e.preventDefault();
			modal.open(CommandPaletteModal, {}, commandPaletteOptions);
		}
	}}
/>

<!-- No top padding: the 62px logo/topbar band provides the breathing room,
     so its content centers between the window edge and the card. -->
<div class="bg-glow-app flex h-screen gap-2.5 px-2.5 pb-2.5">
	<div class="hidden md:contents"><Sidebar /></div>
	<div class="flex min-w-0 flex-1 flex-col">
		<div class="flex min-w-0 items-center gap-3">
			<details class="relative flex-none md:hidden" bind:open={navigationOpen}>
				<summary
					aria-label="Navigation"
					class="text-text-primary cursor-pointer list-none rounded-lg p-2 hover:bg-white/4"
					><MenuIcon size={20} /></summary
				>
				<div
					class="bg-surface-base border-border-default absolute top-10 left-0 z-40 max-h-[80dvh] overflow-y-auto rounded-xl border p-3"
				>
					<Sidebar />
				</div>
			</details>
			<div class="min-w-0 flex-1"><Topbar /></div>
		</div>
		<div class="flex min-h-0 flex-1 gap-4">
			<!-- scrollbar-gutter keeps the scrollbar's column reserved on short
			     pages too, so content does not shift sideways when navigating
			     between a page that scrolls and one that does not. both-edges
			     mirrors that column on the left so the content stays centered;
			     px-4 plus the gutter lands close to the pt-5.5 top inset. -->
			<main
				class="bg-surface-raised border-border-default min-w-0 flex-1 overflow-y-auto rounded-2xl border px-4 pt-4 [scrollbar-gutter:stable_both-edges] sm:pt-5.5"
			>
				{@render children()}
			</main>
			<!-- Right-hand detail panel (store-driven); a flex sibling so <main>
			     cedes space instead of being overlaid. -->
			<SidePanel />
		</div>
	</div>
</div>
