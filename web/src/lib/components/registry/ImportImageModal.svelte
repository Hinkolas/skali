<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'Import image'
	} satisfies ModalOptions;

	export interface ImportImageResult {
		reference: string;
	}
</script>

<script lang="ts">
	import Button from '$lib/components/ui/Button.svelte';
	import ModalHeader from '$lib/components/ui/ModalHeader.svelte';

	// Input collection only — the sudo-gated import call runs in the page
	// AFTER this modal closes, so the reauth modal (single modal slot) is
	// never displaced and the (possibly minutes-long) copy doesn't hold a
	// modal open.
	let {
		close
	}: {
		close: (result?: ImportImageResult) => void;
	} = $props();

	let reference = $state('');
	const valid = $derived(reference.trim() !== '');

	function submit() {
		if (!valid) return;
		close({ reference: reference.trim() });
	}
</script>

<ModalHeader
	title="Import image"
	description="Copies the upstream image into the cluster registry, pinned to its current digest. Nodes then pull it from the LAN. Importing can take a while."
/>

<form
	class="flex flex-col gap-3.5 px-5.5 py-4"
	onsubmit={(e) => {
		e.preventDefault();
		submit();
	}}
>
	<label class="flex flex-col gap-1.5">
		<span class="text-text-tertiary text-[12.5px] font-medium">Upstream reference</span>
		<input
			bind:value={reference}
			type="text"
			required
			placeholder="postgres:17"
			class="border-border-strong bg-surface-input text-text-primary focus:border-accent/50 w-full rounded-[10px] border px-3.25 py-2.75 font-mono text-[13px] transition-colors focus:outline-none"
		/>
	</label>
	<p class="text-text-ghost text-[11.5px] leading-relaxed">
		Tags only — the catalog pins each tag to a digest; re-importing a tag moves its pin to whatever
		upstream serves now.
	</p>

	<!-- Hidden submit so Enter works; the visible buttons live in the footer. -->
	<button type="submit" class="hidden" aria-hidden="true"></button>
</form>

<div class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3">
	<Button variant="ghost" onclick={() => close(undefined)}>Cancel</Button>
	<Button variant="primary" disabled={!valid} onclick={submit}>Import image</Button>
</div>
