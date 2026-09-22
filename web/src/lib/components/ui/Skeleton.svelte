<script lang="ts">
	// Placeholder for content that is still loading: the shape of what will
	// appear, pulsing, so the page paints its structure before its data.
	// Pair it with `deferred()` (see $lib/deferred.svelte.ts) and show it only
	// while nothing was ever loaded; a reload keeps the previous value on
	// screen instead of flashing back to this.
	type Variant = 'line' | 'pill' | 'block' | 'card';
	let {
		variant = 'line',
		class: className = ''
	}: {
		variant?: Variant;
		/** Sizing overrides (width, height) on top of the variant's shape. */
		class?: string;
	} = $props();

	const SHAPE: Record<Variant, string> = {
		line: 'h-3.5 w-24 rounded-md',
		pill: 'h-5 w-16 rounded-full',
		block: 'h-8 w-full rounded-lg',
		card: 'h-32 w-full rounded-[15px]'
	};
</script>

<span aria-hidden="true" class="block animate-pulse bg-white/6 {SHAPE[variant]} {className}"></span>
