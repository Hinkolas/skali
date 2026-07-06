<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'Enroll command',
		size: 'lg'
	} satisfies ModalOptions;
</script>

<script lang="ts">
	import Copy from '@lucide/svelte/icons/copy';
	import Check from '@lucide/svelte/icons/check';
	import { toast } from '$lib/stores/toast.svelte';
	import type { JoinTokenCreated } from '$lib/types/nodes';
	import Button from '$lib/components/ui/Button.svelte';
	import CopyField from '$lib/components/ui/CopyField.svelte';

	let { result, close }: { result: JoinTokenCreated; close: () => void } = $props();

	let copied = $state(false);

	async function copyCommand() {
		try {
			await navigator.clipboard.writeText(result.enroll_command);
			copied = true;
			toast.success('Enroll command copied');
			setTimeout(() => (copied = false), 1600);
		} catch {
			toast.error('Could not copy', { description: 'Clipboard access was denied' });
		}
	}

	function expiresIn(): string {
		const mins = Math.max(0, Math.round((new Date(result.expires_at).getTime() - Date.now()) / 60_000));
		return `${mins} minute${mins === 1 ? '' : 's'}`;
	}
</script>

<div class="px-5.5 pt-5 pb-2">
	<h2 class="text-text-primary text-[16px] font-semibold tracking-tight">Run this on the new machine</h2>
	<p class="text-text-muted mt-1 text-[13px]">
		Install skalid, paste the command, then start the node with <span class="font-mono">skalid agent</span>.
	</p>
</div>

<div class="flex flex-col gap-3 px-5.5 py-4">
	<div
		class="bg-surface-input border-border-default flex items-start justify-between gap-3 rounded-[10px] border px-4 py-3.5"
	>
		<code class="font-mono text-text-secondary text-[12.5px] leading-relaxed break-all">
			{result.enroll_command}
		</code>
		<button
			type="button"
			onclick={copyCommand}
			class="text-text-faint hover:text-text-primary flex-none cursor-pointer rounded-md p-1 transition-colors"
			aria-label="Copy enroll command"
		>
			{#if copied}
				<Check class="text-status-success size-4" />
			{:else}
				<Copy class="size-4" />
			{/if}
		</button>
	</div>

	<CopyField label="Token" value={result.token} masked />

	<p class="text-text-faint text-[11.5px]">
		The token is single-use, expires in {expiresIn()}, and is never shown again. It pins this
		cluster's certificate authority, so the new node only trusts your master.
	</p>
</div>

<div class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3">
	<Button variant="primary" onclick={() => close()}>Done</Button>
</div>
