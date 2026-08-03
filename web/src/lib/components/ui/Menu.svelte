<script lang="ts">
	import { setContext, tick, type Snippet } from 'svelte';

	let {
		label,
		side = 'bottom',
		align = 'start',
		triggerClass = '',
		panelClass = 'min-w-52',
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
		trigger: Snippet<[{ open: boolean }]>;
		children: Snippet;
	} = $props();

	let open = $state(false);
	let root = $state<HTMLDivElement | null>(null);
	let triggerEl = $state<HTMLButtonElement | null>(null);

	// Items self-close on select via this context (see MenuItem).
	setContext('menu', { close: () => (open = false) });

	function items(): HTMLElement[] {
		return root ? [...root.querySelectorAll<HTMLElement>('[role="menuitem"]')] : [];
	}

	function focusItem(index: number) {
		const els = items();
		if (els.length) els[((index % els.length) + els.length) % els.length].focus();
	}

	async function openAndFocus(index: number) {
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

<svelte:window onclick={onWindowClick} />

<div bind:this={root} class="relative">
	<button
		bind:this={triggerEl}
		type="button"
		aria-haspopup="menu"
		aria-expanded={open}
		class={triggerClass}
		onclick={() => (open = !open)}
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
			class="bg-surface-overlay border-border-default absolute z-30 flex flex-col gap-0.5 rounded-xl border p-1.5 shadow-lg {side ===
			'top'
				? 'bottom-full mb-1.5'
				: 'top-full mt-1.5'} {align === 'end' ? 'right-0' : 'left-0'} {panelClass}"
		>
			{@render children()}
		</div>
	{/if}
</div>
