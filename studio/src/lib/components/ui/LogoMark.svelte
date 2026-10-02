<script lang="ts">
	/**
	 * The mark shown inside the gradient logo tiles (sidebar, login, error
	 * pages). A plain lucide icon for now: it inherits the tile's
	 * text-surface-base colour through currentColor, so size it from the
	 * parent with a width/height class and nothing else.
	 */
	import Server from '@lucide/svelte/icons/server';

	let { class: cls = '' }: { class?: string } = $props();
</script>

<Server class={cls} strokeWidth={2.25} aria-hidden="true" />
