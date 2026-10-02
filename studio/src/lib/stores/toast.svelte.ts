import { SvelteMap } from 'svelte/reactivity';

export type ToastVariant = 'success' | 'error' | 'info' | 'warning' | 'loading';

export type Toast = {
	id: string;
	variant: ToastVariant;
	title: string;
	description?: string;
	duration: number;
};

export type ToastInput = {
	description?: string;
	duration?: number;
};

const DEFAULT_DURATION: Record<ToastVariant, number> = {
	success: 3500,
	info: 3500,
	warning: 4500,
	error: 6000,
	loading: 0 // 0 => never auto-dismiss
};

type ResolvedMessage = string | { title: string; description?: string };

function resolveMessage<T>(
	message: ResolvedMessage | ((value: T) => ResolvedMessage),
	value: T
): { title: string; description?: string } {
	const out = typeof message === 'function' ? message(value) : message;
	return typeof out === 'string' ? { title: out } : out;
}

function createToastStore() {
	const items = $state<Toast[]>([]);
	const timers = new SvelteMap<string, ReturnType<typeof setTimeout>>();

	function newId(): string {
		return typeof crypto !== 'undefined' && 'randomUUID' in crypto
			? crypto.randomUUID()
			: `t_${Date.now()}_${Math.random().toString(36).slice(2, 8)}`;
	}

	function scheduleDismiss(id: string, duration: number) {
		const existing = timers.get(id);
		if (existing) clearTimeout(existing);
		if (duration > 0) {
			timers.set(
				id,
				setTimeout(() => dismiss(id), duration)
			);
		}
	}

	function push(variant: ToastVariant, title: string, opts: ToastInput = {}) {
		const id = newId();
		const toast: Toast = {
			id,
			variant,
			title,
			description: opts.description,
			duration: opts.duration ?? DEFAULT_DURATION[variant]
		};
		items.push(toast);
		scheduleDismiss(id, toast.duration);
		return id;
	}

	function update(
		id: string,
		next: { variant?: ToastVariant; title?: string; description?: string; duration?: number }
	): void {
		const i = items.findIndex((t) => t.id === id);
		if (i === -1) return;
		const variant = next.variant ?? items[i].variant;
		items[i] = {
			...items[i],
			variant,
			title: next.title ?? items[i].title,
			description: 'description' in next ? next.description : items[i].description,
			duration: next.duration ?? DEFAULT_DURATION[variant]
		};
		scheduleDismiss(id, items[i].duration);
	}

	function dismiss(id: string) {
		const t = timers.get(id);
		if (t) {
			clearTimeout(t);
			timers.delete(id);
		}
		const i = items.findIndex((t) => t.id === id);
		if (i !== -1) items.splice(i, 1);
	}

	function clear() {
		for (const t of timers.values()) clearTimeout(t);
		timers.clear();
		items.splice(0, items.length);
	}

	/** Loading toast that flips to success/error when `work` settles. Rethrows on failure. */
	async function promise<T>(
		work: Promise<T> | (() => Promise<T>),
		messages: {
			loading: ResolvedMessage;
			success: ResolvedMessage | ((value: T) => ResolvedMessage);
			error: ResolvedMessage | ((err: unknown) => ResolvedMessage);
		}
	): Promise<T> {
		const loadingMsg =
			typeof messages.loading === 'string' ? { title: messages.loading } : messages.loading;
		const id = push('loading', loadingMsg.title, { description: loadingMsg.description });
		try {
			const value = await (typeof work === 'function' ? work() : work);
			const msg = resolveMessage(messages.success, value);
			update(id, { variant: 'success', ...msg });
			return value;
		} catch (err) {
			const msg = resolveMessage(messages.error, err);
			update(id, { variant: 'error', ...msg });
			throw err;
		}
	}

	return {
		get items() {
			return items;
		},
		success: (title: string, opts?: ToastInput) => push('success', title, opts),
		error: (title: string, opts?: ToastInput) => push('error', title, opts),
		info: (title: string, opts?: ToastInput) => push('info', title, opts),
		warning: (title: string, opts?: ToastInput) => push('warning', title, opts),
		loading: (title: string, opts?: ToastInput) => push('loading', title, opts),
		update,
		promise,
		dismiss,
		clear
	};
}

export const toast = createToastStore();
