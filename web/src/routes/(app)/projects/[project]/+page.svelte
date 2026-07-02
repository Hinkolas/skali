<script lang="ts">
	import Plus from '@lucide/svelte/icons/plus';
	import { modal } from '$lib/stores/modal.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import SearchButton from '$lib/components/shell/SearchButton.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import StatCard from '$lib/components/ui/StatCard.svelte';
	import ServiceCard from '$lib/components/service/ServiceCard.svelte';
	import NewServiceModal, {
		modalOptions as newServiceModalOptions
	} from '$lib/components/service/NewServiceModal.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	const statusDot: Record<string, string> = {
		healthy: 'bg-status-success',
		building: 'bg-status-warning',
		degraded: 'bg-status-danger'
	};
</script>

<svelte:head>
	<title>{data.project.name} — skali</title>
</svelte:head>

<PageHeader title={data.project.name}>
	{#snippet subtitle()}
		<span class="size-[7px] flex-none rounded-full {statusDot[data.project.status]}"></span>
		{data.project.subtitle}
	{/snippet}
	{#snippet actions()}
		<SearchButton />
		<Button
			variant="primary"
			onclick={() =>
				toast.info('Deploy triggered', { description: 'Mock only — nothing was deployed.' })}
		>
			Deploy
		</Button>
	{/snippet}
</PageHeader>

<div class="mb-6.5 grid grid-cols-4 gap-3.5">
	{#each data.project.stats as stat (stat.label)}
		<StatCard {stat} />
	{/each}
</div>

<div class="mb-3.5 flex items-baseline gap-2.5">
	<h2 class="text-text-primary text-[16px] font-semibold">Services</h2>
	<div class="text-text-ghost text-[12px]">{data.project.services_summary}</div>
</div>

<div class="grid grid-cols-3 gap-3.5 pb-6">
	{#each data.services as service (service.slug)}
		<ServiceCard {service} />
	{/each}
	<button
		type="button"
		onclick={() =>
			modal.open(NewServiceModal, { projectName: data.project.name }, newServiceModalOptions)}
		class="border-border-strong text-text-faint hover:text-text-secondary grid min-h-[120px] cursor-pointer place-items-center rounded-[14px] border border-dashed text-[13px] transition-colors hover:border-white/20"
	>
		<span class="flex items-center gap-1.5"><Plus size={14} /> Add a service</span>
	</button>
</div>
