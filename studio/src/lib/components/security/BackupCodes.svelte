<script lang="ts">
	import Copy from '@lucide/svelte/icons/copy';
	import Check from '@lucide/svelte/icons/check';
	import { toast } from '$lib/stores/toast.svelte';

	// The one-time backup-code reveal: a mono grid plus copy-all. Used by both
	// the enrollment modal and the regenerate flow.
	let { codes }: { codes: string[] } = $props();

	let copied = $state(false);

	async function copyAll() {
		try {
			await navigator.clipboard.writeText(codes.join('\n'));
			copied = true;
			toast.success('Backup codes copied');
			setTimeout(() => (copied = false), 1600);
		} catch {
			toast.error('Could not copy', { description: 'Clipboard access was denied' });
		}
	}
</script>

<div class="flex flex-col gap-3">
	<div
		class="bg-surface-input border-border-default grid grid-cols-2 gap-x-6 gap-y-1.5 rounded-[11px] border px-4 py-3.5"
	>
		{#each codes as code (code)}
			<span class="font-mono text-text-secondary text-base tracking-wide">{code}</span>
		{/each}
	</div>
	<div class="flex items-center justify-between gap-3">
		<p class="text-text-faint text-md">
			Each code signs you in once if you lose your authenticator. They are never shown again.
		</p>
		<button
			type="button"
			onclick={copyAll}
			class="text-text-tertiary hover:text-text-primary flex flex-none cursor-pointer items-center gap-1.5 rounded-md px-2 py-1 text-md font-medium transition-colors hover:bg-white/5"
		>
			{#if copied}
				<Check class="text-status-success size-3.5" />
			{:else}
				<Copy class="size-3.5" />
			{/if}
			Copy all
		</button>
	</div>
</div>
