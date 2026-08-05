<script lang="ts">
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import type { ServiceView } from '$lib/models/service';
	import type { Project } from '$lib/types/project';
	import { envStatus } from '$lib/stores/envstatus.svelte';
	import { formatBytes } from '$lib/format';
	import { withEnv } from '$lib/urls';
	import StatusPill from '$lib/components/ui/StatusPill.svelte';
	import TypeBadge from '$lib/components/ui/TypeBadge.svelte';

	let { project, service }: { project: Project; service: ServiceView } = $props();

	const env = $derived((page.data.env as { name: string } | null)?.name ?? null);
	const live = $derived(envStatus.service(service.type, service.key));
	const health = $derived(live?.health ?? 'unknown');

	const kindLabel = $derived.by(() => {
		switch (service.type) {
			case 'application':
				return service.config.source.kind === 'image'
					? 'application · image'
					: 'application · build';
			case 'database':
				return `database · ${service.config.engine} ${service.config.version}`;
			case 'bucket':
				return `bucket · ${service.config.visibility}`;
		}
	});
</script>

<!-- eslint-disable svelte/no-navigation-without-resolve -- path built with resolve(), env appended by $lib/urls -->
<a
	href={withEnv(
		resolve('/(app)/projects/[project]/services/[service]', {
			project: project.name,
			service: service.key
		}),
		env
	)}
	class="bg-surface-raised border-border-default hover:border-accent/35 flex flex-col gap-3 rounded-[15px] border px-4.5 py-4 transition-colors"
>
	<div class="flex items-center gap-2.5">
		<TypeBadge kind={service.type} form="tile" size="md" />
		<div class="min-w-0">
			<div class="text-text-primary truncate text-lg font-semibold">{service.name}</div>
			<div class="font-mono text-text-faint truncate text-xs">{kindLabel}</div>
		</div>
		<span class="ml-auto flex-none">
			<!-- Hover-only: the card is one big link, so the pill cannot take a
			     tab stop of its own. Keyboard readers get the same diagnostics
			     from the service header this card links to. -->
			<StatusPill
				status={health}
				diagnostics={live?.diagnostics ?? []}
				align="end"
				focusable={false}
			/>
		</span>
	</div>

	{#if service.type === 'application'}
		<div class="font-mono text-text-muted truncate text-sm">
			{service.config.source.image || service.config.source.build?.context || 'source'}
		</div>
		<div class="font-mono text-text-faint border-border-subtle flex gap-3 border-t pt-2.75 text-xs">
			<span>
				replicas
				<span class="text-text-secondary">
					{live?.pods.filter((p) => p.ready).length ?? 0}/{service.config.scaling.maxReplicas}
				</span>
			</span>
			<span>
				ports
				<span class="text-text-secondary">
					{Object.keys(service.config.ports ?? {}).length}
				</span>
			</span>
			{#if Object.keys(service.config.routes ?? {}).length > 0}
				<span>routed</span>
			{/if}
		</div>
	{:else if service.type === 'database'}
		<div class="font-mono text-text-muted truncate text-sm">
			{service.config.storageBytes ? formatBytes(service.config.storageBytes) : 'default storage'}
		</div>
		<div class="font-mono text-text-faint border-border-subtle flex gap-3 border-t pt-2.75 text-xs">
			<span>isolation <span class="text-text-secondary">{service.config.isolation}</span></span>
			<span>tier <span class="text-text-secondary">{service.config.availability}</span></span>
		</div>
	{:else}
		<div class="font-mono text-text-muted truncate text-sm">
			{service.config.storageQuotaBytes
				? `${formatBytes(service.config.storageQuotaBytes)} quota`
				: 'no quota'}
		</div>
		<div class="font-mono text-text-faint border-border-subtle flex gap-3 border-t pt-2.75 text-xs">
			<span>visibility <span class="text-text-secondary">{service.config.visibility}</span></span>
			<span>versioning <span class="text-text-secondary">{service.config.versioning}</span></span>
		</div>
	{/if}
</a>
