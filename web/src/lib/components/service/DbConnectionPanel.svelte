<script lang="ts">
	import { page } from '$app/state';
	import { api, ApiError } from '$lib/api/client';
	import { requiredTitle, roleAtLeast } from '$lib/access';
	import type { Environment } from '$lib/types/project';
	import type { DatabaseView } from '$lib/models/service';
	import type { DatabaseConnection, DatabaseCredentials } from '$lib/types/connections';
	import { modal } from '$lib/stores/modal.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Card from '$lib/components/ui/Card.svelte';
	import CopyField from '$lib/components/ui/CopyField.svelte';
	import RevealCredentialsModal, {
		modalOptions as revealModalOptions
	} from './RevealCredentialsModal.svelte';

	let {
		service,
		connection,
		envId
	}: {
		service: DatabaseView;
		connection: DatabaseConnection | null;
		envId: string | null;
	} = $props();

	let revealing = $state(false);

	// Credentials are configuration: maintain on the environment reveals them.
	const env = $derived(page.data.env as Environment | null);
	const mayReveal = $derived(roleAtLeast(env?.access, 'maintain'));
	const revealTitle = $derived(
		mayReveal ? undefined : requiredTitle('maintain', 'environment', env?.name ?? '')
	);

	// Sudo-gated one-time reveal; the interceptor in the api client handles
	// the reauth prompt. Values go straight into the modal props and nowhere
	// else.
	async function reveal() {
		if (!envId) return;
		revealing = true;
		try {
			const credentials = await api.post<DatabaseCredentials>(
				`/v1/environments/${envId}/databases/${service.key}/credentials/reveal`
			);
			modal.open(
				RevealCredentialsModal,
				{
					title: `${service.name} credentials`,
					fields: [
						{ label: 'User', value: credentials.username },
						{ label: 'Password', value: credentials.password, masked: true },
						{ label: 'URL', value: credentials.url, masked: true }
					]
				},
				revealModalOptions
			);
		} catch (err) {
			toast.error(err instanceof ApiError ? err.message : 'Could not reveal the credentials');
		} finally {
			revealing = false;
		}
	}
</script>

<Card class="flex flex-col p-5">
	<div class="mb-4 flex items-center gap-2.5">
		<h3 class="text-text-primary text-xl font-semibold">Connection</h3>
		<span
			class="font-mono text-status-success bg-status-success/10 rounded-full px-2 py-0.5 text-2xs"
		>
			private network
		</span>
		<div class="ml-auto">
			<Button
				size="sm"
				busy={revealing}
				disabled={!connection?.host || !mayReveal}
				title={revealTitle}
				onclick={reveal}
			>
				Reveal credentials
			</Button>
		</div>
	</div>
	{#if connection?.host}
		<div class="flex flex-1 flex-col gap-2.25">
			<CopyField label="Host" value={connection.host} />
			<CopyField label="Port" value={String(connection.port ?? '')} />
			<CopyField label="Database" value={connection.database ?? ''} />
		</div>
	{:else}
		<div
			class="border-border-strong grid min-h-[132px] place-items-center rounded-[11px] border border-dashed"
		>
			<div class="flex flex-col gap-2 p-5 text-center">
				<div class="text-text-muted text-base">Not provisioned yet</div>
				<div class="font-mono text-text-faint text-md">
					phase {connection?.phase ?? 'unknown'} · connection facts appear once the claim settles
				</div>
			</div>
		</div>
	{/if}
	<!-- Public access is a planned feature; until it exists the fact that the
	     database is private is one line, not a card of its own. -->
	<div class="border-border-subtle mt-4 flex items-center gap-2.5 border-t pt-3.5">
		<span class="text-text-muted text-md">Public access</span>
		<span class="font-mono text-text-faint text-sm"
			>disabled · reachable only inside the cluster</span
		>
		<span class="ml-auto" title="Public database access is coming soon">
			<Button size="sm" variant="ghost" disabled>Enable</Button>
		</span>
	</div>
</Card>
