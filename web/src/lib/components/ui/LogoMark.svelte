<script lang="ts">
	/**
	 * The skali mark (src/lib/assets/skali-mark.png): near-black glyph on a
	 * transparent square, cropped to its bounding box. It reads as
	 * text-surface-base on the gradient tiles, so no recolouring is needed;
	 * size it from the parent with a width/height class.
	 */
	import mark from '$lib/assets/skali-mark.png';

	let { class: cls = '' }: { class?: string } = $props();
</script>

<img src={mark} alt="" class={cls} draggable="false" />
