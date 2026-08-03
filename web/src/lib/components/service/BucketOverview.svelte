<script lang="ts">
	import { api, ApiError } from '$lib/api/client';
	import type { BucketView, ServiceView } from '$lib/models/service';
	import type { StatCardData } from '$lib/models/view';
	import type { BucketConnection, BucketCredentials } from '$lib/types/connections';
	import { formatBytes } from '$lib/format';
	import { modal } from '$lib/stores/modal.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Card from '$lib/components/ui/Card.svelte';
	import CopyField from '$lib/components/ui/CopyField.svelte';
	import StatCard from '$lib/components/ui/StatCard.svelte';
	import ConnectedAppsList from './ConnectedAppsList.svelte';
	import RevealCredentialsModal, {
		modalOptions as revealModalOptions
	} from './RevealCredentialsModal.svelte';

	let {
		service,
		services,
		connection,
		envId
	}: {
		service: BucketView;
		services: ServiceView[];
		connection: BucketConnection | null;
		envId: string | null;
	} = $props();

	let revealing = $state(false);

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

	const stats = $derived.by((): StatCardData[] => [
		{
			label: 'QUOTA',
			value: service.config.storageQuotaBytes
				? formatBytes(service.config.storageQuotaBytes)
				: 'none',
			note: 'requested in skali.yaml'
		},
		{ label: 'VISIBILITY', value: service.config.visibility },
		{ label: 'VERSIONING', value: service.config.versioning },
		{
			label: 'PHASE',
			value: connection?.phase ?? 'unknown',
			chip:
				connection?.phase === 'provisioned'
					? { text: 'ready', tone: 'success' }
					: { text: 'settling', tone: 'neutral' }
		}
	]);
</script>

<div class="mb-6 grid grid-cols-4 gap-3.5">
	{#each stats as stat (stat.label)}
		<StatCard {stat} />
	{/each}
</div>

<div class="mb-6">
	<Card class="p-5">
		<div class="mb-4 flex items-center gap-2.5">
			<h3 class="text-text-primary text-xl font-semibold">S3 connection</h3>
			<div class="ml-auto">
				<Button size="sm" busy={revealing} disabled={!connection?.endpoint} onclick={reveal}>
					Reveal keypair
				</Button>
			</div>
		</div>
		{#if connection?.endpoint}
			<div class="flex flex-col gap-2.25">
				<CopyField label="Endpoint" value={connection.endpoint} />
				<CopyField label="Bucket" value={connection.bucket ?? ''} />
				<CopyField label="Region" value={connection.region ?? ''} />
			</div>
		{:else}
			<div
				class="border-border-strong grid min-h-[132px] place-items-center rounded-[11px] border border-dashed"
			>
				<div class="flex flex-col gap-2 p-5 text-center">
					<div class="text-text-muted text-base">Not provisioned yet</div>
					<div class="font-mono text-text-ghost text-xs">
						phase {connection?.phase ?? 'unknown'} · connection facts appear once the claim settles
					</div>
				</div>
			</div>
		{/if}
	</Card>
</div>

<div class="mb-3.5 flex items-baseline gap-2.5">
	<h2 class="text-text-primary text-xl font-semibold">Connected applications</h2>
	<div class="text-text-ghost text-md">keys injected as env values</div>
</div>

<div class="pb-6">
	<ConnectedAppsList {service} {services} />
</div>
