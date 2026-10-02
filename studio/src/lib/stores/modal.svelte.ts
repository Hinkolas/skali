import type { Component } from 'svelte';

// Component-based modal system: `modal.open(Component, props, options)` renders
// the component inside the global <Modal> host (mounted once in the root
// layout). The host injects a `close(result?)` prop on top of the component's
// own props; awaiting the returned handle's `result` gives promise-based flows.
//
// Modals form a stack: `open` replaces everything, `push` layers on top of
// whatever is open. Layering is what lets a confirm dialog or the sudo reauth
// prompt appear above a settings modal without destroying it.

export type ModalControls<Result = unknown> = {
	close: (result?: Result) => void;
};

// The task shapes the modal. Each archetype carries its own anatomy in the
// host: placement, width, chrome, and entrance. Content components state
// what they are; the host keeps the family coherent.
//
// - form:       centered workbench; header, body, footer band. The default.
// - confirm:    a compact question; inline buttons, no footer band.
// - danger:     a stop moment; red-tinted chrome, inline buttons.
// - checkpoint: the sudo gate; narrow, centered ceremony, dimmer backdrop.
// - palette:    a finder; drops in near the top, search-first.
export type ModalArchetype = 'form' | 'confirm' | 'danger' | 'checkpoint' | 'palette';

export type ModalOptions = {
	label?: string;
	role?: 'dialog' | 'alertdialog';
	archetype?: ModalArchetype;
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
	let stack = $state<ModalInstance[]>([]);

	// Layer on top of whatever is open. The new modal becomes the interactive
	// one; the layers below stay mounted (their state survives) but inert.
	function push<Result = unknown>(
		component: ModalComponent,
		props: Record<string, unknown> = {},
		options: ModalOptions = {}
	): ModalHandle<Result> {
		const id = newId();
		let resolve!: (result: unknown) => void;
		const result = new Promise<Result>((res) => {
			resolve = res as (result: unknown) => void;
		});

		stack = [...stack, { id, component, props, options, resolve }];
		return { id, result, close: (r) => close(id, r) };
	}

	// Replace the whole stack (resolving every open modal as dismissed).
	function open<Result = unknown>(
		component: ModalComponent,
		props: Record<string, unknown> = {},
		options: ModalOptions = {}
	): ModalHandle<Result> {
		while (stack.length > 0) close(stack[stack.length - 1].id);
		return push(component, props, options);
	}

	// Programmatic close. Without an id, closes the top modal; with one, closes
	// that modal wherever it sits. No-ops on an unknown id (stale handle).
	function close(id?: string, result?: unknown) {
		const inst = id ? stack.find((m) => m.id === id) : stack.at(-1);
		if (!inst) return;
		stack = stack.filter((m) => m !== inst);
		inst.resolve(result);
		inst.options.onClose?.(result);
	}

	// User-initiated close (Esc / backdrop) of the top modal. Honours `dismissable: false`.
	function dismiss() {
		const top = stack.at(-1);
		if (!top || top.options.dismissable === false) return;
		close(top.id);
	}

	return {
		get stack() {
			return stack;
		},
		/** The top of the stack, the modal the user is interacting with. */
		get current() {
			return stack.at(-1) ?? null;
		},
		open,
		push,
		close,
		dismiss
	};
}

export const modal = createModalStore();
