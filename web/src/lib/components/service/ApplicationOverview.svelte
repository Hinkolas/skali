<script lang="ts">
	import ExternalLink from '@lucide/svelte/icons/external-link';
	import type { ApplicationService, Service } from '$lib/mock/types';
	import { toast } from '$lib/stores/toast.svelte';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import StatusPill from '$lib/components/ui/StatusPill.svelte';
	import ConnectedServicesPanel from './ConnectedServicesPanel.svelte';
	import DeploymentsTable from './DeploymentsTable.svelte';
	import WebProcessPanel from './WebProcessPanel.svelte';

	let {
		service,
		services
	}: {
		service: ApplicationService;
		services: Service[];
	} = $props();
</script>

<PageHeader title={service.name}>
	{#snippet titleTrailing()}
		<StatusPill status={service.status} pill />
	{/snippet}
	{#snippet subtitle()}
		<span class="font-mono text-text-faint text-md">
			{service.repo} · {service.branch} · {service.domain ?? service.endpoint}
		</span>
	{/snippet}
	{#snippet actions()}
		{#if service.domain}
			<Button
				onclick={() =>
					toast.info(`This would open ${service.domain}`, {
						description: 'Mock only — the domain does not resolve.'
					})}
			>
				Open app <ExternalLink size={14} />
			</Button>
		{/if}
		<Button
			variant="primary"
			onclick={() =>
				toast.info('Deploy triggered', { description: 'Mock only — nothing was deployed.' })}
		>
			Deploy
		</Button>
	{/snippet}
</PageHeader>

<div class="mb-6 grid grid-cols-[1.5fr_1fr] gap-3.5">
	<WebProcessPanel {service} />
	<ConnectedServicesPanel {service} {services} />
</div>

<div class="mb-3.5 flex items-baseline gap-2.5">
	<h2 class="text-text-primary text-xl font-semibold">Recent deployments</h2>
	<div class="text-text-ghost text-md">auto-deploy on push to {service.branch}</div>
</div>

<div class="pb-6">
	<DeploymentsTable deployments={service.deployments} />
</div>
