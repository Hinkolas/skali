<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'Pull image'
	} satisfies ModalOptions;

	export interface PullImageResult {
		nodeId: string;
		reference: string;
	}
</script>

<script lang="ts">
	import Button from '$lib/components/ui/Button.svelte';
	import ModalHeader from '$lib/components/ui/ModalHeader.svelte';

	// Input collection only — the sudo-gated pull call runs in the page
	// AFTER this modal closes, so the reauth modal (single modal slot) is
	// never displaced and the (possibly minutes-long) pull doesn't hold a
	// modal open.
	let {
		nodes,
		initialNodeId = null,
		close
	}: {
		nodes: { id: string; name: string }[];
		/** Preselected target (e.g. the page's active node filter). */
		initialNodeId?: string | null;
		close: (result?: PullImageResult) => void;
	} = $props();

	let pickedNodeId = $state<string | null>(null);
	const nodeId = $derived(
		pickedNodeId ??
			(initialNodeId && nodes.some((n) => n.id === initialNodeId)
				? initialNodeId
				: (nodes[0]?.id ?? ''))
	);
	let reference = $state('');

	const valid = $derived(nodeId !== '' && reference.trim() !== '');

	function submit() {
		if (!valid) return;
		close({ nodeId, reference: reference.trim() });
	}
</script>

<ModalHeader
	title="Pull image"
	description="Pre-warms an image on a node so later container starts are instant. Pulling can take a while."
/>

<form
	class="flex flex-col gap-3.5 px-5.5 py-4"
	onsubmit={(e) => {
		e.preventDefault();
		submit();
	}}
>
	<div class="flex flex-col gap-1.5">
		<span class="text-text-tertiary text-[12.5px] font-medium">Node</span>
		<div class="flex max-h-40 flex-col gap-2 overflow-y-auto">
			{#each nodes as n (n.id)}
				{@const active = nodeId === n.id}
				<button
					type="button"
					onclick={() => (pickedNodeId = n.id)}
					class="flex cursor-pointer items-center rounded-[10px] border px-3 py-2.5 text-[13px] font-medium transition-colors {active
						? 'border-accent/50 bg-accent/10 text-accent-nav'
						: 'border-border-strong text-text-tertiary hover:bg-white/4'}"
				>
					{n.name}
				</button>
			{/each}
		</div>
	</div>

	<label class="flex flex-col gap-1.5">
		<span class="text-text-tertiary text-[12.5px] font-medium">Image reference</span>
		<input
			bind:value={reference}
			type="text"
			required
			placeholder="nginx:alpine"
			class="border-border-strong bg-surface-input text-text-primary focus:border-accent/50 w-full rounded-[10px] border px-3.25 py-2.75 font-mono text-[13px] transition-colors focus:outline-none"
		/>
	</label>

	<!-- Hidden submit so Enter works; the visible buttons live in the footer. -->
	<button type="submit" class="hidden" aria-hidden="true"></button>
</form>

<div class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3">
	<Button variant="ghost" onclick={() => close(undefined)}>Cancel</Button>
	<Button variant="primary" disabled={!valid} onclick={submit}>Pull image</Button>
</div>
