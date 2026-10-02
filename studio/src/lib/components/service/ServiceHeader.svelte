<script lang="ts">
	import Archive from '@lucide/svelte/icons/archive';
	import ExternalLink from '@lucide/svelte/icons/external-link';
	import RotateCw from '@lucide/svelte/icons/rotate-cw';
	import { page } from '$app/state';
	import type { ServiceView } from '$lib/models/service';
	import type { Environment } from '$lib/types/project';
	import { renderExpression, type ProjectDefinition } from '$lib/types/definition';
	import { api, ApiError } from '$lib/api/client';
	import { requiredTitle, roleAtLeast } from '$lib/access';
	import { backupRefusal, confirmBackup } from '$lib/backups';
	import { envStatus } from '$lib/stores/envstatus.svelte';
	import { dialog } from '$lib/stores/dialog.svelte';
	import { sidepanel } from '$lib/stores/sidepanel.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Skeleton from '$lib/components/ui/Skeleton.svelte';
	import StatusPill from '$lib/components/ui/StatusPill.svelte';
	import RunDetailPanel from '$lib/components/run/RunDetailPanel.svelte';

	// Shared header for every service page: rendered by the service layout so
	// name, status and actions persist while the tabs below switch content.
	let { service }: { service: ServiceView } = $props();

	const live = $derived(envStatus.service(service.type, service.key));
	const health = $derived(live?.health ?? 'unknown');
	// Health speaks for the workload; a route whose domain does not reach
	// this installation yet is a separate, quieter signal beside it.
	const deferredRoutes = $derived((live?.routes ?? []).filter((r) => r.edge?.deferred === true));

	// Restarting one service is scoped here; the topbar Actions menu holds
	// the environment-wide operations (promote, redeploy, restart all).
	// Deploy on the environment is the rung the server checks; it also
	// refuses when nothing runs.
	const env = $derived((page.data as { env?: Environment | null }).env ?? null);
	const definition = $derived(
		(page.data as { definition?: ProjectDefinition | null }).definition ?? null
	);
	const restartTitle = $derived.by(() => {
		if (!env) return 'no environment selected';
		if (!roleAtLeast(env.access, 'deploy')) return requiredTitle('deploy', 'environment', env.name);
		return undefined;
	});
	// A snapshot covers the whole environment, this database included; the
	// button lives here because this is where someone looks for it.
	const backupTitle = $derived(backupRefusal(env, definition));

	function confirmRestart() {
		const target = env;
		if (!target) return;
		dialog.confirm({
			title: `Restart ${service.name}?`,
			description:
				'Recreates the pods with the same revision, values, and image. ' +
				'The rollout follows the deployment strategy; stored data is untouched.',
			confirmLabel: 'Restart',
			onConfirm: async () => {
				try {
					const res = await api.post<{ run_id: string }>(
						`/v1/environments/${target.id}/applications/${service.key}/restart`
					);
					toast.success(`Restarting ${service.name}`);
					sidepanel.open(RunDetailPanel, { runId: res.run_id }, { label: 'Run details' });
				} catch (err) {
					toast.error(err instanceof ApiError ? err.message : 'Could not start the restart');
				}
			}
		});
	}

	// The first route whose domain renders to a plain literal becomes the
	// "Open app" target; for expression-typed domains the live status
	// carries the resolved value once a certificate observed it.
	const appDomain = $derived.by(() => {
		if (service.type !== 'application') return null;
		for (const route of Object.values(service.config.routes ?? {})) {
			const domain = renderExpression(route.domain);
			if (domain && !domain.includes('${')) return domain;
		}
		for (const route of live?.routes ?? []) {
			if (route.domain && !route.domain.includes('${')) return route.domain;
		}
		return null;
	});

	const subtitleText = $derived.by(() => {
		switch (service.type) {
			case 'application': {
				const source =
					service.config.source.kind === 'image'
						? service.config.source.image
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
		{#if envStatus.pending}
			<Skeleton variant="pill" class="h-7 w-24" />
		{:else}
			<StatusPill status={health} pill diagnostics={live?.diagnostics ?? []} />
		{/if}
		{#if deferredRoutes.length > 0}
			<span
				class="text-status-warning bg-status-warning/10 flex items-center gap-1.5 rounded-full px-2.5 py-1 text-md"
				title={deferredRoutes
					.map((r) => `${r.domain} does not reach this installation yet`)
					.join(' · ')}
			>
				<span class="size-[8px] rounded-full bg-status-warning"></span>DNS pending
			</span>
		{/if}
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
			<Button disabled={!!restartTitle} title={restartTitle} onclick={confirmRestart}>
				<RotateCw size={14} /> Restart
			</Button>
		{:else if service.type === 'database'}
			<span title="The database studio is coming soon">
				<Button disabled>Open studio</Button>
			</span>
			<Button
				disabled={!!backupTitle}
				title={backupTitle}
				onclick={() => env && confirmBackup(env)}
			>
				<Archive size={14} /> Back up now
			</Button>
		{/if}
	{/snippet}
</PageHeader>
