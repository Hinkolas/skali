<script lang="ts">
	import Plus from '@lucide/svelte/icons/plus';
	import Container from '@lucide/svelte/icons/container';
	import type { StatCardData } from '$lib/models/view';
	import { envStatus } from '$lib/stores/envstatus.svelte';
	import { HEALTH_META } from '$lib/service-types';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
	import StatCard from '$lib/components/ui/StatCard.svelte';
	import ServiceCard from '$lib/components/service/ServiceCard.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	const status = $derived(envStatus.doc ?? data.status);
	const title = $derived(data.project.display_name || data.project.name);

	const subtitleText = $derived.by(() => {
		if (!data.env) return 'no environments yet';
		const parts = [`${data.services.length} service${data.services.length === 1 ? '' : 's'}`];
		if (status) {
			const healthy = status.services.filter((s) => s.health === 'healthy').length;
			if (status.services.length > 0) parts.push(`${healthy}/${status.services.length} healthy`);
			parts.push(status.state);
		}
		return parts.join(' · ');
	});
	const subtitleDot = $derived.by(() => {
		if (!status || status.services.length === 0) return HEALTH_META.unknown.dot;
		if (status.services.every((s) => s.health === 'healthy')) return HEALTH_META.healthy.dot;
		if (status.services.some((s) => s.health === 'unhealthy')) return HEALTH_META.unhealthy.dot;
		return HEALTH_META.degraded.dot;
	});

	// Placeholder telemetry: no metrics backend exists yet, so these are
	// static sample values, marked as such.
	const stats: StatCardData[] = [
		{ label: 'REQUESTS', value: '1.4k', unit: '/min', chip: { text: 'sample', tone: 'neutral' } },
		{ label: 'CLUSTER CPU', value: '34', unit: '%', chip: { text: 'sample', tone: 'neutral' } },
		{ label: 'MEMORY', value: '6.1', unit: 'GiB', chip: { text: 'sample', tone: 'neutral' } },
		{ label: 'EGRESS', value: '18', unit: 'GiB/d', chip: { text: 'sample', tone: 'neutral' } }
	];
</script>

<svelte:head>
	<title>{title} — skali</title>
</svelte:head>

<PageHeader {title}>
	{#snippet subtitle()}
		<span class="size-[8px] flex-none rounded-full {subtitleDot}"></span>
		{subtitleText}
	{/snippet}
	{#snippet actions()}
		<span title="Deploys run from the CLI for now: skali deploy">
			<Button variant="primary" disabled>Deploy</Button>
		</span>
	{/snippet}
</PageHeader>

<div class="mb-6.5 grid grid-cols-4 gap-3.5">
	{#each stats as stat (stat.label)}
		<StatCard {stat} />
	{/each}
</div>

<div class="mb-3.5 flex items-baseline gap-2.5">
	<h2 class="text-text-primary text-xl font-semibold">Services</h2>
	<div class="text-text-ghost text-md">
		{data.services.length === 0 ? 'defined in skali.yaml' : `env ${data.env?.name ?? 'none'}`}
	</div>
</div>

{#if data.services.length > 0}
	<div class="grid grid-cols-3 gap-3.5 pb-6">
		{#each data.services as service (`${service.type}:${service.key}`)}
			<ServiceCard project={data.project} {service} />
		{/each}
		<button
			type="button"
			disabled
			title="Services are defined in skali.yaml; adding them here is coming soon"
			class="border-border-strong text-text-faint grid min-h-[132px] cursor-default place-items-center rounded-[15px] border border-dashed text-base opacity-60"
		>
			<span class="flex items-center gap-1.5"><Plus size={15} /> Add a service</span>
		</button>
	</div>
{:else}
	<div class="pb-6">
		<EmptyState
			icon={Container}
			title="No services defined"
			description="Add services to skali.yaml and run `skali deploy`."
		/>
	</div>
{/if}
