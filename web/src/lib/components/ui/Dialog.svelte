<script lang="ts">
	import X from '@lucide/svelte/icons/x';
	import LoaderCircle from '@lucide/svelte/icons/loader-circle';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import type { DialogProps } from '$lib/stores/dialog.svelte';

	// Built-in content for the high-level `dialog` store: a simple title +
	// description + confirm/cancel layout. Rendered through the modal system, so
	// it only owns its body and receives `close` from the <Modal> host.
	let {
		title,
		description,
		confirmLabel = 'Confirm',
		cancelLabel = 'Cancel',
		variant = 'default',
		alert = false,
		onConfirm,
		close
	}: DialogProps & { close: (result?: boolean) => void } = $props();

	let busy = $state(false);

	async function confirm() {
		if (busy) return;
		busy = true;
		try {
			await onConfirm?.();
			close(true);
		} finally {
			// on success we've already closed; on error we stay open so the caller
			// (e.g. a toast in onConfirm) can surface what went wrong
			busy = false;
		}
	}
</script>

<div class="flex items-start justify-between gap-4 px-5.5 pt-5 pb-5">
	<div>
		{#if variant === 'danger'}
			<div
				class="border-status-danger/25 bg-status-danger/10 text-status-danger mb-3 flex size-9 items-center justify-center rounded-[10px] border"
			>
				<TriangleAlert size={18} strokeWidth={1.75} />
			</div>
		{/if}
		<h2 class="text-text-primary text-[16px] font-semibold tracking-tight">{title}</h2>
		{#if description}
			<p class="text-text-muted mt-1.5 text-[13px] leading-relaxed">{description}</p>
		{/if}
	</div>
	<button
		type="button"
		onclick={() => close(false)}
		disabled={busy}
		class="text-text-faint hover:text-text-primary -mt-1 -mr-2 shrink-0 cursor-pointer rounded-lg p-1.5 transition hover:bg-white/5 disabled:opacity-50"
		aria-label="Close"
	>
		<X class="size-4" />
	</button>
</div>

<div class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3">
	{#if !alert}
		<button
			type="button"
			onclick={() => close(false)}
			disabled={busy}
			class="text-text-tertiary hover:text-text-primary cursor-pointer rounded-[10px] px-3.5 py-2 text-[13px] font-medium transition hover:bg-white/5 disabled:opacity-50"
		>
			{cancelLabel}
		</button>
	{/if}
	<button
		type="button"
		onclick={confirm}
		disabled={busy}
		class="inline-flex cursor-pointer items-center gap-2 rounded-[10px] px-3.5 py-2 text-[13px] font-semibold transition-[filter] hover:brightness-108 disabled:opacity-60 {variant ===
		'danger'
			? 'bg-status-danger text-white'
			: 'from-accent-from to-accent-to text-surface-base shadow-glow bg-linear-135'}"
	>
		{#if busy}
			<LoaderCircle class="size-4 animate-spin" />
		{/if}
		{confirmLabel}
	</button>
</div>
