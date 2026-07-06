import type { Component } from 'svelte';

// Component-based side panel: `sidepanel.open(Component, props, options)`
// renders into the <SidePanel> host mounted in the (app) shell as a flex
// sibling of <main>, so main cedes space instead of being overlaid. The host
// injects `close()` over the spread props (same convention as the modal).
//
// Unlike the modal there is no result promise and no backdrop — the panel is
// persistent, non-blocking state owned by the page that opened it.

export type SidePanelOptions = {
	/** aria-label for the <aside>. */
	label?: string;
	/** Tailwind width class for the content, default 'w-[380px]'. */
	width?: string;
	/** Fires on close() — NOT when replaced by another open(). */
	onClose?: () => void;
};

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type SidePanelComponent = Component<any>;

type SidePanelInstance = {
	component: SidePanelComponent;
	props: Record<string, unknown>;
	options: SidePanelOptions;
};

function createSidePanelStore() {
	let current = $state<SidePanelInstance | null>(null);

	// Re-opening with the SAME component updates props in place (Svelte keeps
	// the dynamic-component instance when the constructor is unchanged) — this
	// is load-bearing: pages refresh panel props on every data poll without a
	// remount or transition replay. A different component swaps the content.
	function open(
		component: SidePanelComponent,
		props: Record<string, unknown> = {},
		options: SidePanelOptions = {}
	) {
		current = { component, props, options };
	}

	function close() {
		if (!current) return;
		const inst = current;
		current = null;
		inst.options.onClose?.();
	}

	return {
		get current() {
			return current;
		},
		open,
		close
	};
}

export const sidepanel = createSidePanelStore();
