<script lang="ts">
	import type { ApplicationView, ServiceView } from '$lib/models/service';
	import type { Run } from '$lib/types/runs';
	import ConnectedServicesPanel from './ConnectedServicesPanel.svelte';
	import RoutesPanel from './RoutesPanel.svelte';
	import RunsSection from '$lib/components/run/RunsSection.svelte';
	import WebProcessPanel from './WebProcessPanel.svelte';

	let {
		service,
		services,
		envId,
		runs
	}: {
		service: ApplicationView;
		services: ServiceView[];
		envId: string | null;
		runs: Run[] | null;
	} = $props();
</script>

<div class="mb-6 grid grid-cols-[1.5fr_1fr] gap-3.5">
	<WebProcessPanel {service} />
	<ConnectedServicesPanel {service} {services} />
</div>

<div class="mb-6 empty:hidden">
	<RoutesPanel serviceKey={service.key} />
</div>

<div class="mb-3.5 flex items-baseline gap-2.5">
	<h2 class="text-text-primary text-xl font-semibold">Recent runs</h2>
	<div class="text-text-ghost text-md">deploys land here from the CLI</div>
</div>

<div class="pb-6">
	<RunsSection {envId} seed={runs} />
</div>
