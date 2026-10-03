<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'Rotate credentials',
		archetype: 'danger'
	} satisfies ModalOptions;
</script>

<script lang="ts">
	// Rotation flow for one bucket or database: pick how long the current
	// credentials stay accepted, read what will happen, go. The server
	// issues new credentials (a keypair, a login role), restarts the
	// applications referencing the service with them, and retires the
	// previous ones once the window passes: URLs signed with a retired
	// keypair fail, sessions of a retired login role are terminated and the
	// role is dropped. Sudo-gated: the API client replays after a fresh
	// login.
	import KeyRound from '@lucide/svelte/icons/key-round';
	import { api, ApiError } from '$lib/api/client';
	import { sidepanel } from '$lib/stores/sidepanel.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import ModalHeader from '$lib/components/ui/ModalHeader.svelte';
	import Select from '$lib/components/ui/Select.svelte';
	import RunDetailPanel from '$lib/components/run/RunDetailPanel.svelte';

	let {
		collection,
		envId,
		serviceKey,
		serviceName,
		close
	}: {
		collection: 'buckets' | 'databases';
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
	const isBucket = $derived(collection === 'buckets');
	const credential = $derived(isBucket ? 'keypair' : 'credentials');

	async function rotate() {
		if (rotating) return;
		rotating = true;
		try {
			const res = await api.post<{ run_id: string }>(
				`/v1/environments/${envId}/${collection}/${serviceKey}/credentials/rotate`,
				{ retire_after_seconds: seconds }
			);
			toast.success(`Rotating the ${credential} of ${serviceName}`);
			close(true);
			sidepanel.open(RunDetailPanel, { runId: res.run_id }, { label: 'Run details' });
		} catch (err) {
			toast.error(err instanceof ApiError ? err.message : 'Could not start the rotation');
		} finally {
			rotating = false;
		}
	}
</script>

<ModalHeader title="Rotate the {credential} of {serviceName}" icon={KeyRound} tone="danger">
	{#if isBucket}
		A new keypair is issued and the applications using
		<span class="font-mono text-text-secondary">buckets.{serviceKey}</span> restart with it.
	{:else}
		A new login role is issued and the applications using
		<span class="font-mono text-text-secondary">databases.{serviceKey}</span> restart with it.
	{/if}
</ModalHeader>

<div class="flex flex-col gap-4 px-5.5 py-4">
	<label class="flex flex-col gap-1.5">
		<span class="text-text-tertiary text-base font-medium"
			>Keep the current {credential} working for</span
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
		{#if isBucket}
			<p>
				Requests and presigned URLs signed with the current keypair keep working for {windowLabel},
				then fail for good. The window must cover the longest URL the application issues. Anything
				holding the keypair outside the cluster (a <span class="font-mono">skali dev</span> host run,
				a revealed copy) must fetch it again.
			</p>
		{:else}
			<p>
				Connections opened with the current credentials keep working for {windowLabel}; then they
				are terminated and the login role is dropped. The database and everything in it stay owned
				by the owner role, so migrations and restores are unaffected. Anything holding the
				credentials outside the cluster (a <span class="font-mono">skali dev</span> host run, a revealed
				copy) must fetch them again; the username changes with every rotation.
			</p>
		{/if}
	</div>
</div>

<div class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3">
	<Button variant="secondary" onclick={() => close(false)} disabled={rotating}>Cancel</Button>
	<Button variant="danger" onclick={rotate} busy={rotating}
		>Rotate {isBucket ? 'keypair' : 'credentials'}</Button
	>
</div>
