<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'Revealed credentials'
	} satisfies ModalOptions;
</script>

<script lang="ts">
	import Button from '$lib/components/ui/Button.svelte';
	import CopyField from '$lib/components/ui/CopyField.svelte';
	import ModalHeader from '$lib/components/ui/ModalHeader.svelte';

	// Displays freshly revealed credentials exactly once: the values live in
	// these props for the modal's lifetime and are never stored anywhere else.
	let {
		title,
		fields,
		close
	}: {
		title: string;
		fields: { label: string; value: string; masked?: boolean }[];
		close: () => void;
	} = $props();
</script>

<ModalHeader
	{title}
	description="Shown once; copy what you need. Values are never cached by the UI."
/>

<div class="flex flex-col gap-2.25 px-5.5 py-4">
	{#each fields as field (field.label)}
		<CopyField label={field.label} value={field.value} masked={field.masked ?? false} />
	{/each}
</div>

<div class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3">
	<Button variant="ghost" onclick={() => close()}>Close</Button>
</div>
