<script lang="ts">
	import { slide } from 'svelte/transition';
	import { cubicOut } from 'svelte/easing';
	import { afterNavigate } from '$app/navigation';
	import { sidepanel } from '$lib/stores/sidepanel.svelte';
	import { modal } from '$lib/stores/modal.svelte';

	// Panel content is route-coupled, so leaving the page closes it. Compare
	// pathnames only: same-path search-param navigations (?node= selection
	// sync) must not close the panel that was just opened. Shallow
	// replaceState doesn't fire afterNavigate at all, so it's safe either way.
	afterNavigate(({ from, to }) => {
		if (from && to && from.url.pathname !== to.url.pathname) sidepanel.close();
	});
</script>

<!-- Escape closes the topmost layer only: the modal host owns Escape while a
     modal is open, so defer to it. -->
<svelte:window
	onkeydown={(e) => {
		if (e.key === 'Escape' && !modal.current) sidepanel.close();
	}}
/>

{#if sidepanel.current}
	{@const p = sidepanel.current}
	{@const Content = p.component}
	<aside
		aria-label={p.options.label ?? 'Details'}
		transition:slide={{ axis: 'x', duration: 220, easing: cubicOut }}
		class="bg-surface-raised border-border-default flex-none overflow-hidden rounded-2xl border"
	>
		<!-- Fixed-width inner wrapper: `slide` animates the outer width while
		     the inner keeps its final width, so content is revealed/clipped
		     instead of reflowing during the transition. -->
		<div class="flex h-full flex-col {p.options.width ?? 'w-[380px]'}">
			<Content {...p.props} close={() => sidepanel.close()} />
		</div>
	</aside>
{/if}
