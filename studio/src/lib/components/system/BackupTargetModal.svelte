<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'Backup target',
		size: 'lg'
	} satisfies ModalOptions;
</script>

<script lang="ts">
	import { api, ApiError } from '$lib/api/client';
	import { toast } from '$lib/stores/toast.svelte';
	import type { BackupTarget, BackupTargetInput } from '$lib/types/backups';
	import Button from '$lib/components/ui/Button.svelte';
	import Field from '$lib/components/ui/Field.svelte';
	import ModalHeader from '$lib/components/ui/ModalHeader.svelte';
	import SecretInput from '$lib/components/ui/SecretInput.svelte';
	import TextInput from '$lib/components/ui/TextInput.svelte';

	// Configures the backup target, or changes the stored one. The whole
	// target is replaced, so the secret is asked for every time: it is
	// write-only, and a save without it fails server-side rather than
	// silently blanking the stored key. Resolves true once saved.
	let {
		target,
		close
	}: {
		target: BackupTarget | null;
		close: (saved?: boolean) => void;
	} = $props();

	// The modal host mounts this component fresh per open, so seeding the form
	// from the initial prop value is exactly right.
	// svelte-ignore state_referenced_locally
	let form = $state<BackupTargetInput>({
		endpoint: target?.endpoint ?? '',
		region: target?.region ?? '',
		bucket: target?.bucket ?? '',
		prefix: target?.prefix ?? '',
		access_key_id: target?.access_key_id ?? '',
		secret_access_key: ''
	});
	let busy = $state(false);
	let errorMessage = $state('');

	const complete = $derived(
		!!form.endpoint.trim() &&
			!!form.bucket.trim() &&
			!!form.access_key_id.trim() &&
			!!form.secret_access_key
	);
	const saveTitle = $derived.by(() => {
		if (!form.endpoint.trim() || !form.bucket.trim() || !form.access_key_id.trim()) {
			return 'endpoint, bucket, and access key id are required';
		}
		if (!form.secret_access_key) return 'enter the secret access key to save';
		return undefined;
	});

	async function save() {
		if (!complete || busy) return;
		busy = true;
		errorMessage = '';
		try {
			await api.put('/v1/system/backup-target', {
				...form,
				endpoint: form.endpoint.trim(),
				region: form.region.trim(),
				bucket: form.bucket.trim(),
				prefix: form.prefix.trim(),
				access_key_id: form.access_key_id.trim()
			});
			toast.success('Backup target saved', {
				description: 'Snapshots and schedules write to it from now on.'
			});
			close(true);
		} catch (err) {
			errorMessage = err instanceof ApiError ? err.message : 'Could not save the backup target.';
			busy = false;
		}
	}
</script>

<ModalHeader
	title={target ? 'Edit backup target' : 'Set backup target'}
	description="Any S3-compatible service: AWS, Cloudflare R2, Backblaze B2, MinIO, Hetzner."
/>

<form
	class="flex min-h-0 flex-col gap-4 overflow-y-auto px-5.5 py-4"
	onsubmit={(e) => {
		e.preventDefault();
		void save();
	}}
>
	{#if errorMessage}
		<div
			class="border-status-danger/40 bg-status-danger/10 text-status-danger rounded-[11px] border px-4 py-3 text-base"
		>
			{errorMessage}
		</div>
	{/if}
	<Field
		label="Endpoint"
		description="http or https URL of the S3 API, for example https://s3.eu-central-1.amazonaws.com"
	>
		<TextInput
			bind:value={form.endpoint}
			mono
			placeholder="https://s3.example.com"
			autocomplete="off"
		/>
	</Field>
	<div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
		<Field label="Bucket" description="The bucket must exist.">
			<TextInput bind:value={form.bucket} mono placeholder="skali-backups" autocomplete="off" />
		</Field>
		<Field label="Region" description="Leave empty when the provider does not use regions.">
			<TextInput bind:value={form.region} mono placeholder="eu-central-1" autocomplete="off" />
		</Field>
	</div>
	<Field
		label="Prefix"
		description="Optional; keeps several installations apart in one bucket. Snapshots live under <prefix>/skali/v1/<project>/<environment>/."
	>
		<TextInput bind:value={form.prefix} mono placeholder="prod" autocomplete="off" />
	</Field>
	<div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
		<Field label="Access key id">
			<TextInput bind:value={form.access_key_id} mono placeholder="AKIA..." autocomplete="off" />
		</Field>
		<Field
			label="Secret access key"
			description={target
				? 'Write-only; enter it again to save any change.'
				: 'Stored encrypted; never shown again.'}
		>
			<SecretInput
				label="Secret access key"
				bind:value={form.secret_access_key}
				placeholder={target ? '•••••••••••• stored' : 'secret access key'}
			/>
		</Field>
	</div>
	<button type="submit" class="hidden" aria-hidden="true" tabindex="-1"></button>
</form>

<div class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3">
	<Button variant="ghost" onclick={() => close(false)} disabled={busy}>Cancel</Button>
	<Button variant="primary" {busy} disabled={!complete} title={saveTitle} onclick={save}>
		{target ? 'Save changes' : 'Set target'}
	</Button>
</div>
