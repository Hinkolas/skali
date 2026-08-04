<script lang="ts">
	import ExternalLink from '@lucide/svelte/icons/external-link';
	import type { ServiceView } from '$lib/models/service';
	import { renderExpression } from '$lib/types/definition';
	import { envStatus } from '$lib/stores/envstatus.svelte';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import StatusPill from '$lib/components/ui/StatusPill.svelte';

	// Shared header for every service page: rendered by the service layout so
	// name, status and actions persist while the tabs below switch content.
	let { service }: { service: ServiceView } = $props();

	const health = $derived(envStatus.service(service.type, service.key)?.health ?? 'unknown');

	// The first route whose domain renders to a plain literal becomes the
	// "Open app" target; expression-typed domains cannot be resolved here.
	const appDomain = $derived.by(() => {
		if (service.type !== 'application') return null;
		for (const route of Object.values(service.config.routes ?? {})) {
			const domain = renderExpression(route.domain);
			if (domain && !domain.includes('${')) return domain;
		}
		return null;
	});

	const subtitleText = $derived.by(() => {
		switch (service.type) {
			case 'application': {
				const source =
					service.config.source.kind === 'image'
						? renderExpression(service.config.source.image)
						: `build ${service.config.source.build?.context ?? '.'}`;
				return [source, appDomain].filter(Boolean).join(' · ');
			}
			case 'database':
				return `${service.config.engine} ${service.config.version} · ${service.config.isolation} · ${service.config.availability}`;
			case 'bucket':
				return `bucket · ${service.config.visibility} · versioning ${service.config.versioning}`;
		}
	});
</script>

<PageHeader title={service.name}>
	{#snippet titleTrailing()}
		<StatusPill status={health} pill />
	{/snippet}
	{#snippet subtitle()}
		<span class="font-mono text-text-faint text-md">{subtitleText}</span>
	{/snippet}
	{#snippet actions()}
		{#if service.type === 'application'}
			{#if appDomain}
				<Button href="https://{appDomain}">
					Open app <ExternalLink size={14} />
				</Button>
			{/if}
			<span title="Deploys run from the CLI for now: skali deploy">
				<Button variant="primary" disabled>Deploy</Button>
			</span>
		{:else if service.type === 'database'}
			<span title="The database studio is coming soon">
				<Button disabled>Open studio</Button>
			</span>
			<span title="On-demand backups are coming soon">
				<Button disabled>Back up now</Button>
			</span>
		{/if}
	{/snippet}
</PageHeader>
