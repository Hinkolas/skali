<script lang="ts">
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import type { DialogProps } from '$lib/stores/dialog.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import TextInput from '$lib/components/ui/TextInput.svelte';

	// Built-in content for the high-level `dialog` store. A confirm is a
	// question, not a workbench: one compact block, buttons inline, no footer
	// band (that band belongs to forms). The danger variant leads with the
	// hazard tile and can demand the resource's name before the button arms.
	let {
		title,
		description,
		confirmLabel = 'Confirm',
		cancelLabel = 'Cancel',
		variant = 'default',
		alert = false,
		typeToConfirm,
		onConfirm,
		close
	}: DialogProps & { close: (result?: boolean) => void } = $props();

	let busy = $state(false);
	let typed = $state('');

	const armed = $derived(!typeToConfirm || typed === typeToConfirm);

	async function confirm() {
		if (busy || !armed) return;
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

<div class="px-5.5 pt-5 pb-4.5">
	{#if variant === 'danger'}
		<div class="flex items-center gap-3">
			<div
				class="border-status-danger/25 bg-status-danger/10 text-status-danger flex size-8.5 flex-none items-center justify-center rounded-[10px] border"
			>
				<TriangleAlert size={17} strokeWidth={1.75} />
			</div>
			<h2 class="text-text-primary text-lg font-semibold tracking-tight">{title}</h2>
		</div>
	{:else}
		<h2 class="text-text-primary text-lg font-semibold tracking-tight">{title}</h2>
	{/if}

	{#if description}
		<p class="text-text-muted mt-2 text-base leading-relaxed">{description}</p>
	{/if}

	{#if typeToConfirm}
		<label class="mt-3.5 flex flex-col gap-1.5">
			<span class="text-text-tertiary text-md">
				Type <span class="font-mono text-text-secondary">{typeToConfirm}</span> to confirm
			</span>
			<TextInput
				bind:value={typed}
				mono
				autofocus
				placeholder={typeToConfirm}
				onkeydown={(e) => {
					if (e.key === 'Enter') void confirm();
				}}
			/>
		</label>
	{/if}

	<div class="mt-5 flex justify-end gap-2">
		{#if !alert}
			<Button variant="ghost" disabled={busy} onclick={() => close(false)}>{cancelLabel}</Button>
		{/if}
		<Button
			variant={variant === 'danger' ? 'danger' : 'primary'}
			{busy}
			disabled={!armed}
			title={armed ? undefined : `type ${typeToConfirm} to enable`}
			onclick={confirm}
		>
			{confirmLabel}
		</Button>
	</div>
</div>
