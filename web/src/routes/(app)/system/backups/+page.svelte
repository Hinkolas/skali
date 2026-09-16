<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import Archive from '@lucide/svelte/icons/archive';
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import CircleDashed from '@lucide/svelte/icons/circle-dashed';
	import { api, ApiError } from '$lib/api/client';
	import { formatDateTime, relativeTime } from '$lib/format';
	import { dialog } from '$lib/stores/dialog.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import type { BackupTarget, BackupTargetInput } from '$lib/types/backups';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Card from '$lib/components/ui/Card.svelte';
	import Field from '$lib/components/ui/Field.svelte';
	import SecretInput from '$lib/components/ui/SecretInput.svelte';
	import TextInput from '$lib/components/ui/TextInput.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	// The form edits a copy of the stored target; the secret is write-only,
	// so it starts empty and a save without it fails server-side rather
	// than silently blanking the stored key.
	const stored = $derived<BackupTarget | null>(data.target);
	let form = $state<BackupTargetInput>(blank());
	let busy = $state<'save' | 'remove' | null>(null);
	let errorMessage = $state('');

	function blank(): BackupTargetInput {
		return {
			endpoint: '',
			region: '',
			bucket: '',
			prefix: '',
			access_key_id: '',
			secret_access_key: ''
		};
	}
	function seed(target: BackupTarget | null) {
		form = target
			? {
					endpoint: target.endpoint,
					region: target.region,
					bucket: target.bucket,
					prefix: target.prefix,
					access_key_id: target.access_key_id,
					secret_access_key: ''
				}
			: blank();
	}
	$effect(() => seed(stored));

	const dirty = $derived(
		!stored ||
			form.endpoint !== stored.endpoint ||
			form.region !== stored.region ||
			form.bucket !== stored.bucket ||
			form.prefix !== stored.prefix ||
			form.access_key_id !== stored.access_key_id ||
			form.secret_access_key !== ''
	);
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
		if (!complete) return;
		busy = 'save';
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
			await invalidateAll();
		} catch (err) {
			errorMessage = err instanceof ApiError ? err.message : 'Could not save the backup target.';
		} finally {
			busy = null;
		}
	}

	function remove() {
		dialog.confirm({
			title: 'Remove the backup target?',
			description:
				'The stored credentials are deleted and scheduled backups stop. Snapshots already in the ' +
				'bucket are untouched; setting the same target again makes them listable and restorable.',
			confirmLabel: 'Remove target',
			variant: 'danger',
			onConfirm: async () => {
				busy = 'remove';
				try {
					await api.del('/v1/system/backup-target');
					toast.success('Backup target removed');
					await invalidateAll();
				} catch (err) {
					toast.error(err instanceof ApiError ? err.message : 'Could not remove the backup target');
					throw err;
				} finally {
					busy = null;
				}
			}
		});
	}
</script>

<svelte:head>
	<title>Backup target — skali</title>
</svelte:head>

<PageHeader title="Backup target">
	{#snippet subtitle()}
		<span>The external S3 location every snapshot goes to</span>
	{/snippet}
	{#snippet actions()}
		{#if stored}
			<Button variant="ghost" onclick={remove} busy={busy === 'remove'} disabled={busy != null}>
				Remove target
			</Button>
		{/if}
		<Button
			variant="primary"
			onclick={save}
			busy={busy === 'save'}
			disabled={busy != null || !dirty || !complete}
			title={saveTitle}
		>
			{stored ? 'Save changes' : 'Set target'}
		</Button>
	{/snippet}
</PageHeader>

<div class="grid grid-cols-1 gap-3.5 pb-6 @4xl:grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)]">
	<Card class="p-5">
		{#if errorMessage}
			<div
				class="border-status-danger/40 bg-status-danger/10 text-status-danger mb-4 rounded-[11px] border px-4 py-3 text-base"
			>
				{errorMessage}
			</div>
		{/if}
		<form
			class="flex flex-col gap-4"
			onsubmit={(e) => {
				e.preventDefault();
				void save();
			}}
		>
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
			<div class="grid grid-cols-1 gap-4 @xl:grid-cols-2">
				<Field label="Bucket">
					<TextInput bind:value={form.bucket} mono placeholder="skali-backups" autocomplete="off" />
				</Field>
				<Field label="Region" description="Leave empty when the provider does not use regions.">
					<TextInput bind:value={form.region} mono placeholder="eu-central-1" autocomplete="off" />
				</Field>
			</div>
			<Field
				label="Prefix"
				description="Optional key prefix inside the bucket; snapshots live under <prefix>/skali/v1/<project>/<environment>/."
			>
				<TextInput bind:value={form.prefix} mono placeholder="prod" autocomplete="off" />
			</Field>
			<div class="grid grid-cols-1 gap-4 @xl:grid-cols-2">
				<Field label="Access key id">
					<TextInput
						bind:value={form.access_key_id}
						mono
						placeholder="AKIA..."
						autocomplete="off"
					/>
				</Field>
				<Field
					label="Secret access key"
					description={stored
						? 'Stored and write-only; enter it again to save any change.'
						: 'Stored encrypted; never shown again.'}
				>
					<SecretInput
						label="Secret access key"
						bind:value={form.secret_access_key}
						placeholder={stored ? '•••••••••••• stored' : 'secret access key'}
					/>
				</Field>
			</div>
			<button type="submit" class="hidden" aria-hidden="true" tabindex="-1"></button>
		</form>
	</Card>

	<Card class="p-5">
		<div class="flex items-center gap-3">
			<div
				class="grid size-9 flex-none place-items-center rounded-[11px] {stored
					? 'bg-status-success/12 text-status-success'
					: 'bg-white/6 text-text-tertiary'}"
			>
				{#if stored}
					<CircleCheck size={17} strokeWidth={1.75} />
				{:else}
					<CircleDashed size={17} strokeWidth={1.75} />
				{/if}
			</div>
			<div class="min-w-0">
				<h2 class="text-text-primary text-lg font-semibold">
					{stored ? 'Target configured' : 'No target yet'}
				</h2>
				<p class="text-text-muted text-md">
					{#if stored}
						<span class="font-mono">{stored.endpoint}/{stored.bucket}</span>
						· updated {relativeTime(stored.updated_at)}
					{:else}
						backups and schedules idle until one is set
					{/if}
				</p>
			</div>
		</div>
		<div class="text-text-muted mt-4 flex flex-col gap-2.5 text-md">
			<p>
				Every project's snapshots, manual and scheduled, are written here under
				<span class="font-mono">skali/v1/&lt;project&gt;/&lt;environment&gt;/</span>. The layout is
				self-describing: a fresh installation pointed at the same bucket lists and restores them.
			</p>
			<p>
				Manifest <span class="font-mono">backups</span> policies run on their cron schedule (UTC) for
				every active environment and delete their own snapshots once past retention. Manual snapshots
				stay until someone deletes them.
			</p>
			{#if stored}
				<p class="text-text-faint text-sm">
					Set {formatDateTime(stored.created_at)} · credentials are stored encrypted and never returned.
				</p>
			{/if}
		</div>
		<div class="mt-4 flex items-center gap-2">
			<Archive size={14} class="text-text-ghost" />
			<span class="text-text-muted text-sm"
				>Snapshots are listed and restored per project on its Backups tab.</span
			>
		</div>
	</Card>
</div>
