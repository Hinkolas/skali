<script lang="ts">
	import { page } from '$app/state';
	import { api, ApiError } from '$lib/api/client';
	import { requiredTitle, roleAtLeast } from '$lib/access';
	import type { Environment } from '$lib/types/project';
	import type { BucketView } from '$lib/models/service';
	import type { BucketConnection, BucketCredentials } from '$lib/types/connections';
	import { modal } from '$lib/stores/modal.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Card from '$lib/components/ui/Card.svelte';
	import CopyField from '$lib/components/ui/CopyField.svelte';
	import RevealCredentialsModal, {
		modalOptions as revealModalOptions
	} from './RevealCredentialsModal.svelte';

	// How to reach the bucket over S3: endpoint, bucket name, and region as
	// copy fields, the keypair behind a sudo-gated one-time reveal.
	let {
		service,
		connection,
		envId
	}: {
		service: BucketView;
		connection: BucketConnection | null;
		envId: string | null;
	} = $props();

	let revealing = $state(false);

	// Credentials are configuration: maintain on the environment reveals them.
	const env = $derived(page.data.env as Environment | null);
	const mayReveal = $derived(roleAtLeast(env?.access, 'maintain'));
	const revealTitle = $derived(
		mayReveal ? undefined : requiredTitle('maintain', 'environment', env?.name ?? '')
	);

	async function reveal() {
		if (!envId) return;
		revealing = true;
		try {
			const credentials = await api.post<BucketCredentials>(
				`/v1/environments/${envId}/buckets/${service.key}/credentials/reveal`
			);
			modal.open(
				RevealCredentialsModal,
				{
					title: `${service.name} S3 keypair`,
					fields: [
						{ label: 'Access key', value: credentials.access_key },
						{ label: 'Secret key', value: credentials.secret_key, masked: true }
					]
				},
				revealModalOptions
			);
		} catch (err) {
			toast.error(err instanceof ApiError ? err.message : 'Could not reveal the keypair');
		} finally {
			revealing = false;
		}
	}

	const publicReads = $derived(service.config.visibility === 'public');
</script>

<Card class="flex flex-col p-5">
	<div class="mb-4 flex items-center gap-2.5">
		<h3 class="text-text-primary text-xl font-semibold">S3 connection</h3>
		<div class="ml-auto">
			<Button
				size="sm"
				busy={revealing}
				disabled={!connection?.endpoint || !mayReveal}
				title={revealTitle}
				onclick={reveal}
			>
				Reveal keypair
			</Button>
		</div>
	</div>
	{#if connection?.endpoint}
		<div class="flex flex-1 flex-col gap-2.25">
			<CopyField label="Endpoint" value={connection.endpoint} />
			<CopyField label="Bucket" value={connection.bucket ?? ''} />
			<CopyField label="Region" value={connection.region ?? ''} />
		</div>
	{:else}
		<div
			class="border-border-strong grid min-h-[132px] flex-1 place-items-center rounded-[11px] border border-dashed"
		>
			<div class="flex flex-col gap-2 p-5 text-center">
				<div class="text-text-muted text-base">Not provisioned yet</div>
				<div class="font-mono text-text-faint text-md">
					phase {connection?.phase ?? 'unknown'} · connection facts appear once the claim settles
				</div>
			</div>
		</div>
	{/if}
	<!-- Visibility is set in skali.yaml; the footer states what it means for
	     the wire so the reader need not look the term up. -->
	<div class="border-border-subtle mt-4 flex items-center gap-2.5 border-t pt-3.5">
		<span class="text-text-muted text-md">Public reads</span>
		<span class="font-mono text-text-faint text-sm">
			{publicReads
				? 'enabled · objects are readable without credentials'
				: 'disabled · every request needs the keypair'}
		</span>
	</div>
</Card>
