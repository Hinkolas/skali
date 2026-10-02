<script lang="ts">
	import { setContext, tick, type Snippet } from 'svelte';

	let {
		label,
		side = 'bottom',
		align = 'start',
		triggerClass = '',
		panelClass = 'min-w-52',
		class: className = '',
		trigger,
		children
	}: {
		/** aria-label for the menu panel. */
		label: string;
		/** Which side of the trigger the panel opens on. */
		side?: 'bottom' | 'top';
		/** Panel edge alignment relative to the trigger. */
		align?: 'start' | 'end';
		triggerClass?: string;
		/** Width/extra classes for the panel. */
		panelClass?: string;
		/** Classes for the root wrapper: min-w-0 lets a truncating trigger shrink in a flex row, grid placement puts the menu in a cell. */
		class?: string;
		trigger: Snippet<[{ open: boolean }]>;
		children: Snippet;
	} = $props();

	let open = $state(false);
	let root = $state<HTMLDivElement | null>(null);
	let triggerEl = $state<HTMLButtonElement | null>(null);

	// The panel is position:fixed and placed from the trigger's viewport rect,
	// so it escapes overflow-hidden/auto ancestors (cards, scrolling modal
	// bodies) that would clip an absolutely positioned dropdown. Measured
	// before opening; scrolling anywhere outside the menu closes it instead of
	// dragging a stale position around.
	let panelStyle = $state('');

	function place() {
		if (!triggerEl) return;
		const r = triggerEl.getBoundingClientRect();
		const gap = 6;
		const parts: string[] = [];
		if (side === 'top') {
			parts.push(`bottom: ${window.innerHeight - r.top + gap}px`);
			parts.push(`max-height: ${Math.max(r.top - gap - 8, 120)}px`);
		} else {
			parts.push(`top: ${r.bottom + gap}px`);
			parts.push(`max-height: ${Math.max(window.innerHeight - r.bottom - gap - 8, 120)}px`);
		}
		if (align === 'end') parts.push(`right: ${window.innerWidth - r.right}px`);
		else parts.push(`left: ${r.left}px`);
		panelStyle = parts.join('; ');
	}

	function toggle() {
		if (!open) place();
		open = !open;
	}

	function onWindowScroll(e: Event) {
		if (open && root && e.target instanceof Node && !root.contains(e.target)) open = false;
	}

	// Items self-close on select via this context (see MenuItem).
	setContext('menu', { close: () => (open = false) });

	// Disabled items stay visible but leave the roving focus order.
	function items(): HTMLElement[] {
		return root
			? [...root.querySelectorAll<HTMLElement>('[role="menuitem"]:not([aria-disabled="true"])')]
			: [];
	}

	function focusItem(index: number) {
		const els = items();
		if (els.length) els[((index % els.length) + els.length) % els.length].focus();
	}

	async function openAndFocus(index: number) {
		place();
		open = true;
		await tick();
		focusItem(index);
	}

	function closeToTrigger(e: KeyboardEvent) {
		// stopPropagation so window-level Escape handlers (side panel) don't
		// also fire while a menu is open.
		e.stopPropagation();
		open = false;
		triggerEl?.focus();
	}

	function onWindowClick(e: MouseEvent) {
		if (open && root && !root.contains(e.target as Node)) open = false;
	}

	function onTriggerKeydown(e: KeyboardEvent) {
		if (e.key === 'Escape' && open) {
			closeToTrigger(e);
		} else if (e.key === 'ArrowDown') {
			e.preventDefault();
			void openAndFocus(0);
		} else if (e.key === 'ArrowUp') {
			e.preventDefault();
			void openAndFocus(-1);
		}
	}

	function onPanelKeydown(e: KeyboardEvent) {
		const els = items();
		const current = els.indexOf(document.activeElement as HTMLElement);
		switch (e.key) {
			case 'Escape':
				closeToTrigger(e);
				break;
			case 'ArrowDown':
				e.preventDefault();
				focusItem(current + 1);
				break;
			case 'ArrowUp':
				e.preventDefault();
				focusItem(current <= 0 ? els.length - 1 : current - 1);
				break;
			case 'Home':
				e.preventDefault();
				focusItem(0);
				break;
			case 'End':
				e.preventDefault();
				focusItem(els.length - 1);
				break;
			case 'Tab':
				open = false;
				break;
		}
	}
</script>

<svelte:window
	onclick={onWindowClick}
	onresize={() => open && place()}
	onscrollcapture={onWindowScroll}
/>

<div bind:this={root} class="relative {className}">
	<button
		bind:this={triggerEl}
		type="button"
		aria-haspopup="menu"
		aria-expanded={open}
		class={triggerClass}
		onclick={toggle}
		onkeydown={onTriggerKeydown}
	>
		{@render trigger({ open })}
	</button>

	{#if open}
		<div
			role="menu"
			tabindex="-1"
			aria-label={label}
			onkeydown={onPanelKeydown}
			style={panelStyle}
			class="bg-surface-overlay border-border-default fixed z-80 flex flex-col gap-0.5 overflow-y-auto rounded-xl border p-1.5 shadow-lg {panelClass}"
		>
			{@render children()}
		</div>
	{/if}
</div>
