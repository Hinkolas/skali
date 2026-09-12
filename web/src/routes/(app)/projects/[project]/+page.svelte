<script lang="ts">
	import Plus from '@lucide/svelte/icons/plus';
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import Container from '@lucide/svelte/icons/container';
	import { slide } from 'svelte/transition';
	import { envStatus } from '$lib/stores/envstatus.svelte';
	import { HEALTH_META, STORAGE_KIND_META } from '$lib/service-types';
	import { formatBytes } from '$lib/format';
	import { usageLimits, usageStats } from '$lib/models/usage';
	import { storageByKind, storageFootprint, storageForEnvironment } from '$lib/types/metrics';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Card from '$lib/components/ui/Card.svelte';
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
	import ProgressBar from '$lib/components/ui/ProgressBar.svelte';
	import StackedBar from '$lib/components/ui/StackedBar.svelte';
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

	// Usage tiles for every application of the environment, summed; the
	// limits are the declared per-replica limits scaled by the live pod
	// count (see $lib/models/usage).
	const limits = $derived(
		usageLimits(data.definition?.applications ?? {}, (key) =>
			status?.services.find((s) => s.type === 'application' && s.key === key)
		)
	);
	const stats = $derived(usageStats(data.metrics, data.metrics?.applications ?? [], limits));

	// Storage rows of the selected environment, largest footprint first.
	const envStorage = $derived(
		storageForEnvironment(data.storage, data.env?.id ?? null).toSorted(
			(a, b) => storageFootprint(b) - storageFootprint(a)
		)
	);
	const storageKinds = $derived(storageByKind(envStorage));
	const storageTotal = $derived(envStorage.reduce((acc, s) => acc + storageFootprint(s), 0));
	// Declared capacity across the environment's services: the summary bar's
	// full width, so the unfilled track is what is still free. Falls back to
	// the footprint (a full bar) where nothing declares a size.
	const storageCapacity = $derived(envStorage.reduce((acc, s) => acc + s.capacity_bytes, 0));

	// The per-service breakdown is folded away by default: the summary bar
	// answers the common question, and with many services the row list
	// would otherwise push the service cards below the fold.
	let storageOpen = $state(false);
</script>

<svelte:head>
	<title>{title} — skali</title>
</svelte:head>

<!-- Promote and Redeploy are environment-wide and live in the topbar next
     to the environment breadcrumb. -->
<PageHeader {title}>
	{#snippet subtitle()}
		<span class="size-[8px] flex-none rounded-full {subtitleDot}"></span>
		{subtitleText}
	{/snippet}
</PageHeader>

<div class="mb-6.5 grid grid-cols-2 gap-3.5 @4xl:grid-cols-4">
	{#each stats as stat (stat.label)}
		<StatCard {stat} />
	{/each}
</div>

{#if envStorage.length > 0}
	<div class="mb-3.5 flex flex-wrap items-baseline gap-x-2.5 gap-y-0.5">
		<h2 class="text-text-primary text-xl font-semibold">Storage</h2>
		<div class="text-text-muted text-md">env {data.env?.name ?? 'none'}</div>
	</div>
	<Card class="mb-6.5">
		<button
			type="button"
			onclick={() => (storageOpen = !storageOpen)}
			aria-expanded={storageOpen}
			aria-controls="storage-breakdown"
			class="flex w-full cursor-pointer items-center gap-4 px-4.5 py-4 text-left"
		>
			<div class="min-w-0 flex-1">
				<StackedBar
					segments={Object.entries(STORAGE_KIND_META).map(([kind, meta]) => ({
						label: `${meta.label} ${formatBytes(storageKinds[kind as keyof typeof storageKinds] ?? 0)}`,
						value: storageKinds[kind as keyof typeof storageKinds] ?? 0,
						class: meta.class
					}))}
					total={storageCapacity > 0 ? storageCapacity : storageTotal}
					class="h-2.5"
				/>
				<div class="mt-2.5 flex flex-wrap items-center gap-x-4 gap-y-1">
					{#each Object.entries(STORAGE_KIND_META) as [kind, meta] (kind)}
						{#if (storageKinds[kind as keyof typeof storageKinds] ?? 0) > 0}
							<span class="text-text-faint flex items-center gap-1.5 font-mono text-xs">
								<span class="size-[8px] rounded-full {meta.class}"></span>
								{meta.label}
								{formatBytes(storageKinds[kind as keyof typeof storageKinds])}
							</span>
						{/if}
					{/each}
					{#if storageCapacity > 0}
						<span class="text-text-muted ml-auto font-mono text-xs">
							{formatBytes(storageTotal)} of {formatBytes(storageCapacity)}
						</span>
					{/if}
				</div>
			</div>
			<span class="text-text-muted flex flex-none items-center gap-1.5 text-md">
				{envStorage.length} service{envStorage.length === 1 ? '' : 's'}
				<ChevronDown
					size={16}
					class="text-text-faint transition-transform duration-200 {storageOpen
						? 'rotate-180'
						: ''}"
				/>
			</span>
		</button>
		{#if storageOpen}
			<div
				id="storage-breakdown"
				transition:slide={{ duration: 180 }}
				class="border-border-subtle border-t px-4.5 pt-1 pb-1.5"
			>
				{#each envStorage as entry (`${entry.kind}:${entry.service_key}`)}
					{@const meta = STORAGE_KIND_META[entry.kind]}
					<div
						class="border-border-subtle grid grid-cols-[1.6fr_1fr_1.4fr] items-center gap-3 border-b py-2.5 last:border-0"
					>
						<div class="flex items-center gap-2">
							<span class="size-[8px] flex-none rounded-full {meta.class}"></span>
							<span class="text-text-primary truncate font-mono text-sm">{entry.service_key}</span>
						</div>
						<div class="text-text-muted font-mono text-sm">
							{#if entry.used_bytes != null}
								{formatBytes(entry.used_bytes)}
							{:else}
								reserved {formatBytes(entry.capacity_bytes)}
							{/if}
						</div>
						<div class="flex items-center gap-2.5">
							{#if entry.used_bytes != null && entry.capacity_bytes > 0}
								<div class="min-w-0 flex-1">
									<ProgressBar
										pct={Math.min(100, (entry.used_bytes / entry.capacity_bytes) * 100)}
										class={meta.class}
									/>
								</div>
								<span class="text-text-faint flex-none font-mono text-xs">
									of {formatBytes(entry.capacity_bytes)}
								</span>
							{/if}
						</div>
					</div>
				{/each}
			</div>
		{/if}
	</Card>
{/if}

<div class="mb-3.5 flex flex-wrap items-baseline gap-x-2.5 gap-y-0.5">
	<h2 class="text-text-primary text-xl font-semibold">Services</h2>
	<div class="text-text-muted text-md">
		{data.services.length === 0 ? 'defined in skali.yaml' : `env ${data.env?.name ?? 'none'}`}
	</div>
</div>

{#if data.services.length > 0}
	<div class="grid grid-cols-1 gap-3.5 pb-6 @2xl:grid-cols-2 @5xl:grid-cols-3">
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
