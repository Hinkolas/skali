<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'Accept incomplete restore',
		archetype: 'danger',
		size: 'md'
	} satisfies ModalOptions;
</script>

<script lang="ts">
	// Lifting the hold an incomplete restore keeps on an environment: the
	// data stays as the restore left it, partially rewritten, and deploys
	// are allowed again. Re-running a restore is the better way back, so
	// this asks for the environment name like the restore itself. The
	// environment stays down until the next deploy. Sudo-gated: the API
	// client replays after a fresh login on its own.
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import { api, ApiError } from '$lib/api/client';
	import { formatDateTime } from '$lib/format';
	import { toast } from '$lib/stores/toast.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import ModalHeader from '$lib/components/ui/ModalHeader.svelte';
	import TextInput from '$lib/components/ui/TextInput.svelte';
	import type { Environment } from '$lib/types/project';
	import type { IncompleteRestore } from '$lib/types/status';

	let {
		env,
		restore,
		close
	}: {
		env: Environment;
		restore: IncompleteRestore;
		close: (accepted?: boolean) => void;
	} = $props();

	let typed = $state('');
	let accepting = $state(false);
	const armed = $derived(typed.trim() === env.name && !accepting);

	async function accept() {
		if (!armed) return;
		accepting = true;
		try {
			await api.post<{ run_id: string }>(`/v1/environments/${env.id}/restore/accept`, {
				restore_id: restore.id
			});
			toast.success(`${env.name} may deploy again; redeploy to bring it back`);
			close(true);
		} catch (err) {
			toast.error(err instanceof ApiError ? err.message : 'Could not accept the data');
		} finally {
			accepting = false;
		}
	}
</script>

<ModalHeader title="Accept the data in {env.name}?" icon={TriangleAlert} tone="danger">
	The restore of snapshot <span class="font-mono text-text-secondary">{restore.snapshot_id}</span>
	started {formatDateTime(restore.started_at)} and did not complete.
</ModalHeader>

<div class="flex flex-col gap-4 px-5.5 py-4">
	<div
		class="border-status-danger/25 bg-status-danger/8 text-text-secondary rounded-[11px] border px-3.5 py-3 text-md"
	>
		<p>
			{#if restore.components.length > 0}
				<span class="font-mono text-text-primary">{restore.components.join(', ')}</span>
			{:else}
				The components it rewrote
			{/if}
			may hold partial data; components the restore never reached still hold their data from before. Re-running
			a restore is the way back to complete data.
		</p>
		<p class="mt-2">
			Accepting allows deploys again. <span class="font-mono text-text-primary">{env.name}</span> stays
			down until the next deploy brings it back on this data.
		</p>
	</div>

	<label class="flex flex-col gap-1.5">
		<span class="text-text-tertiary text-base font-medium">
			Type <span class="font-mono text-text-primary">{env.name}</span> to confirm
		</span>
		<TextInput bind:value={typed} mono autofocus placeholder={env.name} autocomplete="off" />
	</label>
</div>

<div class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3">
	<Button variant="secondary" onclick={() => close(false)} disabled={accepting}>Cancel</Button>
	<Button variant="danger" onclick={accept} disabled={!armed} busy={accepting}>
		Accept current data
	</Button>
</div>
