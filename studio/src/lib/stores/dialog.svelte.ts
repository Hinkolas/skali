import { modal, type ModalHandle } from './modal.svelte';
import Dialog from '$lib/components/ui/Dialog.svelte';

// Thin sugar over the modal system for title/description confirm and alert
// boxes. For bespoke content, write a component and open it with
// `modal.open(...)` directly.

export type DialogVariant = 'default' | 'danger';

export type DialogOptions = {
	title: string;
	description?: string;
	confirmLabel?: string;
	cancelLabel?: string;
	variant?: DialogVariant;
	alert?: boolean; // single acknowledge button, no cancel
	/**
	 * The name the user must type before the confirm button arms. Reserve it
	 * for one-way destructions (purge, delete); reversible dangers stay
	 * one-click.
	 */
	typeToConfirm?: string;
	/** May be async: confirm button spins while pending; throwing keeps the dialog open. */
	onConfirm?: () => void | Promise<void>;
	onCancel?: () => void;
};

// Props handed to the <Dialog> content component (everything but the lifecycle hooks).
export type DialogProps = Omit<DialogOptions, 'onCancel'>;

// Dialogs layer on the modal stack: a confirm asked from inside an open
// modal (remove a member, tear down an environment) appears above it and
// hands control back on close instead of destroying the form underneath.
// The variant picks the shell archetype: dangers get the red-tinted chrome.
function open(options: DialogOptions): ModalHandle<boolean> {
	const { onCancel, ...props } = options;
	return modal.push<boolean>(Dialog, props, {
		role: 'alertdialog',
		archetype: options.variant === 'danger' ? 'danger' : 'confirm',
		label: options.title,
		onClose: (result) => {
			if (result !== true) onCancel?.();
		}
	});
}

/** Resolves `true` if confirmed, `false` if cancelled/dismissed. */
function confirm(options: DialogOptions): Promise<boolean> {
	return open(options).result.then((r) => r === true);
}

/** A single-button acknowledgement. Resolves when dismissed. */
function alert(options: Omit<DialogOptions, 'alert' | 'cancelLabel' | 'onCancel'>): Promise<void> {
	return open({ confirmLabel: 'OK', ...options, alert: true }).result.then(() => undefined);
}

export const dialog = { open, confirm, alert };
