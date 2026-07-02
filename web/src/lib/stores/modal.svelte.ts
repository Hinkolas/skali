import type { Component } from 'svelte';

// Component-based modal system: `modal.open(Component, props, options)` renders
// the component inside the global <Modal> host (mounted once in the root
// layout). The host injects a `close(result?)` prop on top of the component's
// own props; awaiting the returned handle's `result` gives promise-based flows.

export type ModalControls<Result = unknown> = {
	close: (result?: Result) => void;
};

export type ModalOptions = {
	label?: string;
	role?: 'dialog' | 'alertdialog';
	size?: 'md' | 'lg';
	closeOnBackdrop?: boolean;
	dismissable?: boolean; // false => Esc/backdrop can't close
	panelClass?: string;
	onClose?: (result: unknown) => void;
};

// A handle returned from `open`: await `result` for the close value, or call `close`.
export type ModalHandle<Result = unknown> = {
	id: string;
	result: Promise<Result>;
	close: (result?: Result) => void;
};

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type ModalComponent = Component<any>;

type ModalInstance = {
	id: string;
	component: ModalComponent;
	props: Record<string, unknown>;
	options: ModalOptions;
	resolve: (result: unknown) => void;
};

function newId(): string {
	return typeof crypto !== 'undefined' && 'randomUUID' in crypto
		? crypto.randomUUID()
		: `m_${Date.now()}`;
}

function createModalStore() {
	let current = $state<ModalInstance | null>(null);

	function open<Result = unknown>(
		component: ModalComponent,
		props: Record<string, unknown> = {},
		options: ModalOptions = {}
	): ModalHandle<Result> {
		// single active modal: replace whatever's open (resolving it as dismissed)
		if (current) close(current.id);

		const id = newId();
		let resolve!: (result: unknown) => void;
		const result = new Promise<Result>((res) => {
			resolve = res as (result: unknown) => void;
		});

		current = { id, component, props, options, resolve };
		return { id, result, close: (r) => close(id, r) };
	}

	// Programmatic close. No-ops if `id` doesn't match the active modal (stale handle).
	function close(id?: string, result?: unknown) {
		if (!current) return;
		if (id && current.id !== id) return;
		const inst = current;
		current = null;
		inst.resolve(result);
		inst.options.onClose?.(result);
	}

	// User-initiated close (Esc / backdrop). Honours `dismissable: false`.
	function dismiss() {
		if (!current || current.options.dismissable === false) return;
		close(current.id);
	}

	return {
		get current() {
			return current;
		},
		open,
		close,
		dismiss
	};
}

export const modal = createModalStore();
