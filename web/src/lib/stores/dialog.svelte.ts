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
	size?: 'md' | 'lg';
	/** May be async: confirm button spins while pending; throwing keeps the dialog open. */
	onConfirm?: () => void | Promise<void>;
	onCancel?: () => void;
};

// Props handed to the <Dialog> content component (everything but the lifecycle hooks).
export type DialogProps = Omit<DialogOptions, 'onCancel' | 'size'>;

function open(options: DialogOptions): ModalHandle<boolean> {
	const { onCancel, size, ...props } = options;
	return modal.open<boolean>(Dialog, props, {
		role: 'alertdialog',
		size: size ?? 'md',
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
