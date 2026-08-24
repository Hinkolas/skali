<script lang="ts">
	import type { ApplicationView, ServiceView } from '$lib/models/service';
	import type { ServiceStorage } from '$lib/types/metrics';
	import type { Run } from '$lib/types/runs';
	import { formatBytes } from '$lib/format';
	import ConnectedServicesPanel from './ConnectedServicesPanel.svelte';
	import RoutesPanel from './RoutesPanel.svelte';
	import RunsSection from '$lib/components/run/RunsSection.svelte';
	import WebProcessPanel from './WebProcessPanel.svelte';

	let {
		service,
		services,
		envId,
		runs,
		storage = null,
		temporaryStorage = null
	}: {
		service: ApplicationView;
		services: ServiceView[];
		envId: string | null;
		runs: Run[] | null;
		storage?: ServiceStorage | null;
		temporaryStorage?: ServiceStorage | null;
	} = $props();
</script>

<div class="mb-6 grid grid-cols-[1.5fr_1fr] gap-3.5">
	<WebProcessPanel {service} />
	<ConnectedServicesPanel {service} {services} />
</div>

{#if storage || temporaryStorage}
	<div class="border-border-subtle mb-6 rounded-[15px] border px-4.5">
		{#if storage}
			<div
				class="border-border-subtle flex items-center justify-between border-b py-3 last:border-0"
			>
				<span class="text-text-muted text-md">Volume storage</span>
				<span class="font-mono text-text-primary text-sm">
					{#if storage.used_bytes != null}
						{formatBytes(storage.used_bytes)} of {formatBytes(storage.capacity_bytes)} used
					{:else}
						{formatBytes(storage.capacity_bytes)} reserved
					{/if}
				</span>
			</div>
		{/if}
		{#if temporaryStorage}
			<div
				class="border-border-subtle flex items-center justify-between border-b py-3 last:border-0"
			>
				<span class="text-text-muted text-md">Temporary storage</span>
				<span class="font-mono text-text-primary text-sm">
					{#if temporaryStorage.used_bytes != null && temporaryStorage.capacity_bytes > 0}
						{formatBytes(temporaryStorage.used_bytes)} of {formatBytes(
							temporaryStorage.capacity_bytes
						)} limit
					{:else if temporaryStorage.used_bytes != null}
						{formatBytes(temporaryStorage.used_bytes)} used
					{:else}
						{formatBytes(temporaryStorage.capacity_bytes)} limit
					{/if}
				</span>
			</div>
		{/if}
	</div>
{/if}

<div class="mb-6 empty:hidden">
	<RoutesPanel serviceKey={service.key} />
</div>

<div class="mb-3.5 flex items-baseline gap-2.5">
	<h2 class="text-text-primary text-xl font-semibold">Recent runs</h2>
	<div class="text-text-muted text-md">deploys land here from the CLI</div>
</div>

<div class="pb-6">
	<RunsSection {envId} seed={runs} />
</div>
