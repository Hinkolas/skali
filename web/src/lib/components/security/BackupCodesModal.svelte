<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	// Not dismissable: Esc/backdrop would lose the codes forever.
	export const modalOptions = {
		label: 'New backup codes',
		dismissable: false
	} satisfies ModalOptions;
</script>

<script lang="ts">
	import Button from '$lib/components/ui/Button.svelte';
	import BackupCodes from '$lib/components/security/BackupCodes.svelte';

	let { codes, close }: { codes: string[]; close: (saved?: boolean) => void } = $props();
</script>

<div class="px-5.5 pt-5 pb-2">
	<h2 class="text-text-primary text-[16px] font-semibold tracking-tight">New backup codes</h2>
	<p class="text-text-muted mt-1 text-[13px]">
		Your previous codes no longer work. Store these somewhere safe before closing — this is the only
		time they are shown.
	</p>
</div>

<div class="px-5.5 py-4">
	<BackupCodes {codes} />
</div>

<div class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3">
	<Button variant="primary" onclick={() => close(true)}>I saved my backup codes</Button>
</div>
