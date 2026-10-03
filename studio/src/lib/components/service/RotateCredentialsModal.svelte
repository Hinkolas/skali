<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'Rotate keypair',
		archetype: 'danger'
	} satisfies ModalOptions;
</script>

<script lang="ts">
	// Rotation flow for one bucket: pick how long the current keypair stays
	// accepted, read what will happen, go. The server issues a new keypair,
	// restarts the applications referencing the bucket with it, and retires
	// the previous keypair once the window passes; URLs signed with it fail
	// from then on. Sudo-gated: the API client replays after a fresh login.
	import KeyRound from '@lucide/svelte/icons/key-round';
	import { api, ApiError } from '$lib/api/client';
	import { sidepanel } from '$lib/stores/sidepanel.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import ModalHeader from '$lib/components/ui/ModalHeader.svelte';
	import Select from '$lib/components/ui/Select.svelte';
	import RunDetailPanel from '$lib/components/run/RunDetailPanel.svelte';

	let {
		envId,
		serviceKey,
		serviceName,
		close
	}: {
		envId: string;
		serviceKey: string;
		serviceName: string;
		close: (rotated?: boolean) => void;
	} = $props();

	const windows: { seconds: number; label: string }[] = [
		{ seconds: 15 * 60, label: '15 minutes' },
		{ seconds: 60 * 60, label: '1 hour' },
		{ seconds: 6 * 60 * 60, label: '6 hours' },
		{ seconds: 24 * 60 * 60, label: '24 hours' },
		{ seconds: 7 * 24 * 60 * 60, label: '7 days' }
	];
	let seconds = $state(60 * 60);
	let rotating = $state(false);

	const windowLabel = $derived(windows.find((w) => w.seconds === seconds)?.label ?? '');

	async function rotate() {
		if (rotating) return;
		rotating = true;
		try {
			const res = await api.post<{ run_id: string }>(
				`/v1/environments/${envId}/buckets/${serviceKey}/credentials/rotate`,
				{ retire_after_seconds: seconds }
			);
			toast.success(`Rotating the keypair of ${serviceName}`);
			close(true);
			sidepanel.open(RunDetailPanel, { runId: res.run_id }, { label: 'Run details' });
		} catch (err) {
			toast.error(err instanceof ApiError ? err.message : 'Could not start the rotation');
		} finally {
			rotating = false;
		}
	}
</script>

<ModalHeader title="Rotate the keypair of {serviceName}" icon={KeyRound} tone="danger">
	A new keypair is issued and the applications using
	<span class="font-mono text-text-secondary">buckets.{serviceKey}</span> restart with it.
</ModalHeader>

<div class="flex flex-col gap-4 px-5.5 py-4">
	<label class="flex flex-col gap-1.5">
		<span class="text-text-tertiary text-base font-medium"
			>Keep the current keypair working for</span
		>
		<Select bind:value={seconds} disabled={rotating}>
			{#each windows as w (w.seconds)}
				<option value={w.seconds}>{w.label}</option>
			{/each}
		</Select>
	</label>

	<div
		class="border-status-danger/25 bg-status-danger/8 text-text-secondary rounded-[11px] border px-3.5 py-3 text-md"
	>
		<p>
			Requests and presigned URLs signed with the current keypair keep working for {windowLabel},
			then fail for good. The window must cover the longest URL the application issues. Anything
			holding the keypair outside the cluster (a <span class="font-mono">skali dev</span> host run, a
			revealed copy) must fetch it again.
		</p>
	</div>
</div>

<div class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3">
	<Button variant="secondary" onclick={() => close(false)} disabled={rotating}>Cancel</Button>
	<Button variant="danger" onclick={rotate} busy={rotating}>Rotate keypair</Button>
</div>
