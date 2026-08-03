<script lang="ts">
	import type { ApplicationService, Service } from '$lib/mock/types';
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
