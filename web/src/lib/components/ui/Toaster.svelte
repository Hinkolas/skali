<script lang="ts">
	import { fly } from 'svelte/transition';
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import CircleAlert from '@lucide/svelte/icons/circle-alert';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import Info from '@lucide/svelte/icons/info';
	import LoaderCircle from '@lucide/svelte/icons/loader-circle';
	import X from '@lucide/svelte/icons/x';
	import { toast, type ToastVariant } from '$lib/stores/toast.svelte';

	// Global host for the toast stack. Mounted once in the root layout; driven by
	// the `toast` store (toast.success/error/info/warning/loading/promise).
	// z-[60]: toasts stay visible above the modal host (z-50).
	const icon: Record<ToastVariant, typeof CircleCheck> = {
		success: CircleCheck,
		error: CircleAlert,
		warning: TriangleAlert,
		info: Info,
		loading: LoaderCircle
	};

	const iconColor: Record<ToastVariant, string> = {
		success: 'text-status-success',
		error: 'text-status-danger',
		warning: 'text-status-warning',
		info: 'text-accent-light',
		loading: 'text-text-faint'
	};
</script>

<div
	class="pointer-events-none fixed right-4 bottom-4 z-[60] flex w-[min(396px,calc(100vw-2rem))] flex-col-reverse gap-2"
	role="region"
	aria-label="Notifications"
>
	{#each toast.items as t (t.id)}
		{@const ToastIcon = icon[t.variant]}
		<div
			class="bg-surface-overlay border-border-strong pointer-events-auto flex items-start gap-3 rounded-xl border px-4 py-3 shadow-lg shadow-black/40"
			role="status"
			transition:fly={{ y: 12, duration: 180 }}
		>
			<div class="mt-0.5 shrink-0 {iconColor[t.variant]}">
				<ToastIcon class="size-[20px] {t.variant === 'loading' ? 'animate-spin' : ''}" />
			</div>
			<div class="min-w-0 flex-1">
				<div class="text-text-primary text-lg font-semibold">{t.title}</div>
				{#if t.description}
					<div class="text-text-muted mt-0.5 text-base leading-snug">{t.description}</div>
				{/if}
			</div>
			<button
				type="button"
				onclick={() => toast.dismiss(t.id)}
				class="text-text-faint hover:text-text-primary -mt-0.5 -mr-1 shrink-0 cursor-pointer rounded-lg p-1 transition hover:bg-white/5"
				aria-label="Dismiss"
			>
				<X class="size-3.5" />
			</button>
		</div>
	{/each}
</div>
