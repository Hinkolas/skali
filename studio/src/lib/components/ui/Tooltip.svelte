<script lang="ts">
	import type { Snippet } from 'svelte';

	// Hover/focus popover for explanatory detail that has nowhere else to go
	// (health diagnostics, truncated values). Not a menu: nothing inside is
	// interactive, so it never traps focus and only closes on leave/blur/Escape.
	//
	// The panel sits in a wrapper that owns the gap between trigger and card,
	// so pointing at the tooltip to read it does not cross a dead zone and
	// dismiss it.
	let {
		side = 'bottom',
		align = 'start',
		focusable = true,
		panelClass = 'w-80',
		trigger,
		children
	}: {
		/** Which side of the trigger the panel opens on. */
		side?: 'bottom' | 'top';
		/** Panel edge alignment relative to the trigger. */
		align?: 'start' | 'end';
		/**
		 * Whether the trigger takes keyboard focus. False inside an already
		 * interactive ancestor (a card that is one big link), where a nested
		 * tab stop would be invalid; those call sites are hover-only and the
		 * same detail must stay reachable on the page they link to.
		 */
		focusable?: boolean;
		/** Width/extra classes for the panel. */
		panelClass?: string;
		trigger: Snippet;
		children: Snippet;
	} = $props();

	const id = $props.id();
	let open = $state(false);

	function onKeydown(e: KeyboardEvent) {
		// stopPropagation so window-level Escape handlers (side panel, modal)
		// don't also fire while a tooltip is open.
		if (e.key === 'Escape' && open) {
			e.stopPropagation();
			open = false;
		}
	}
</script>

<div
	class="relative inline-flex"
	onmouseenter={() => (open = true)}
	onmouseleave={() => (open = false)}
	onfocusin={focusable ? () => (open = true) : undefined}
	onfocusout={focusable ? () => (open = false) : undefined}
	onkeydown={onKeydown}
	role="presentation"
>
	{#if focusable}
		<!-- A button, not a span with tabindex: the trigger of a tooltip has to
		     be a natively focusable element to be reachable and announced. It
		     has no click behaviour of its own, focus is the whole point. -->
		<button
			type="button"
			aria-describedby={open ? id : undefined}
			class="focus-visible:ring-accent/50 inline-flex rounded-full outline-none focus-visible:ring-2"
		>
			{@render trigger()}
		</button>
	{:else}
		<span aria-describedby={open ? id : undefined} class="inline-flex">
			{@render trigger()}
		</span>
	{/if}

	{#if open}
		<div
			class="absolute z-30 {side === 'top' ? 'bottom-full pb-1.5' : 'top-full pt-1.5'} {align ===
			'end'
				? 'right-0'
				: 'left-0'}"
		>
			<div
				{id}
				role="tooltip"
				class="bg-surface-overlay border-border-default rounded-xl border px-3 py-2.5 shadow-lg {panelClass}"
			>
				{@render children()}
			</div>
		</div>
	{/if}
</div>
