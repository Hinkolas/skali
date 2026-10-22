<script lang="ts">
	import { resolve } from '$app/paths';
	import { formatDateTime, relativeTime } from '$lib/format';
	import type { DatabasePoolDetail } from '$lib/types/pools';
	import Card from '$lib/components/ui/Card.svelte';
	import CopyId from '$lib/components/ui/CopyId.svelte';

	// The pool's connection check when it needs attention, and the recent
	// stretches of time it refused connections. A pool that accepts
	// connections and never refused shows nothing here.
	let { pool }: { pool: DatabasePoolDetail } = $props();

	const connections = $derived(pool.connections);
	const attention = $derived(connections != null && connections.state !== 'available');

	const tone = $derived(
		connections?.state === 'refusing'
			? 'border-status-danger/25 bg-status-danger/10'
			: connections?.state === 'saturated'
				? 'border-status-warning/25 bg-status-warning/10'
				: 'border-border-subtle bg-white/3'
	);
	const titleTone = $derived(
		connections?.state === 'refusing'
			? 'text-status-danger'
			: connections?.state === 'saturated'
				? 'text-status-warning'
				: 'text-text-secondary'
	);
	const title = $derived.by(() => {
		switch (connections?.state) {
			case 'refusing':
				return 'Refusing new connections';
			case 'saturated':
				return 'Close to the connection limit';
			default:
				return 'Connections unchecked';
		}
	});
	const advice = $derived.by(() => {
		switch (connections?.state) {
			case 'refusing':
				return 'Applications on this pool cannot open database connections. Release connections held by the databases below, or raise max_connections under Tuning (the instances restart).';
			case 'saturated':
				return 'New connections are refused once the limit is reached. Check which databases hold the connections, or raise max_connections under Tuning.';
			default:
				return 'Whether applications can connect is not known right now; the pool does not count as recovered until the check logs in again.';
		}
	});
	const tuning = $derived(resolve('/(app)/system/databases/[pool]/tuning', { pool: pool.name }));
</script>

{#if attention || pool.incidents.length > 0}
	<Card class="mb-6.5 flex flex-col gap-4 p-5">
		{#if attention && connections}
			<div class="rounded-[11px] border px-3.5 py-3 {tone}">
				<div class="flex flex-wrap items-baseline gap-x-3 gap-y-1">
					<p class="font-medium {titleTone}">{title}</p>
					{#if connections.since}
						<span
							class="text-text-faint font-mono text-xs"
							title={formatDateTime(connections.since)}
						>
							since {relativeTime(connections.since)}
						</span>
					{/if}
				</div>
				{#if connections.reason}
					<p class="text-text-secondary mt-1 font-mono text-md break-words">{connections.reason}</p>
				{/if}
				<p class="text-text-secondary mt-2 text-md">
					{advice}
					{#if connections.state !== 'unknown'}
						<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- resolved above -->
						<a href={tuning} class="underline">Open Tuning</a>
					{/if}
				</p>
				{#if connections.incident}
					<div class="text-text-faint mt-2 flex items-center gap-2 font-mono text-xs">
						incident <CopyId id={connections.incident.id} label="Incident id" />
					</div>
				{/if}
			</div>
		{/if}
		{#if pool.incidents.length > 0}
			<div>
				<h3 class="text-text-primary mb-2 text-lg font-semibold">Connection incidents</h3>
				<div class="flex flex-col">
					{#each pool.incidents as incident (incident.id)}
						<div
							class="border-border-subtle flex flex-wrap items-baseline gap-x-4 gap-y-1 border-b py-2 last:border-0"
						>
							<span class="text-text-secondary font-mono text-md" title={incident.id}>
								{formatDateTime(incident.opened_at)}
							</span>
							<span class="text-text-faint font-mono text-xs">
								{incident.closed_at ? `recovered ${relativeTime(incident.closed_at)}` : 'ongoing'}
							</span>
							<span
								class="text-text-muted min-w-0 flex-1 truncate text-md"
								title={incident.message}
							>
								{incident.message}
							</span>
						</div>
					{/each}
				</div>
			</div>
		{/if}
	</Card>
{/if}
