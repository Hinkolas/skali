<script lang="ts">
	import ExternalLink from '@lucide/svelte/icons/external-link';
	import type { Service } from '$lib/mock/types';
	import { toast } from '$lib/stores/toast.svelte';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import StatusPill from '$lib/components/ui/StatusPill.svelte';

	// Shared header for every service page: rendered by the service layout so
	// name, status and actions persist while the tabs below switch content.
	let { service }: { service: Service } = $props();

	function backupNow() {
		if (service.type !== 'database') return;
		const size = service.storage_used;
		void toast.promise(new Promise((resolve) => setTimeout(resolve, 1800)), {
			loading: `Creating snapshot of ${service.name}…`,
			success: {
				title: 'Backup complete',
				description: `${size}G snapshot stored — mock only.`
			},
			error: 'Backup failed'
		});
	}
</script>

<PageHeader title={service.name}>
	{#snippet titleTrailing()}
		<StatusPill status={service.status} pill />
	{/snippet}
	{#snippet subtitle()}
		<span class="font-mono text-text-faint text-md">
			{#if service.type === 'application'}
				{service.repo} · {service.branch} · {service.domain ?? service.endpoint}
			{:else if service.type === 'database'}
				{service.engine}
				{service.version} · created {service.created_at} · id {service.short_id}
			{:else}
				{service.kind_label} · on {service.node}
			{/if}
		</span>
	{/snippet}
	{#snippet actions()}
		{#if service.type === 'application'}
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
		{:else if service.type === 'database'}
			<Button onclick={() => toast.info('The database studio is coming soon')}>Open studio</Button>
			<Button onclick={backupNow}>Back up now</Button>
		{/if}
	{/snippet}
</PageHeader>
