<script lang="ts">
	/**
	 * The gradient tile with the server mark, as in the Studio sidebar and
	 * favicon. `size` is the tile's edge in px; the icon scales with it.
	 */
	import Server from '@lucide/svelte/icons/server';

	let { size = 26 }: { size?: number } = $props();
</script>

<span
	class="flex shrink-0 items-center justify-center bg-linear-135 from-accent-from to-accent-to text-surface-base"
	style:width="{size}px"
	style:height="{size}px"
	style:border-radius="{Math.round(size * 0.27)}px"
	aria-hidden="true"
>
	<Server size={Math.round(size * 0.6)} strokeWidth={2.4} />
</span>
